package download

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ButterFuture/GoQuark/internal/api"
)

func aFile(fid, name string) api.FileEntry { return api.FileEntry{FID: fid, Name: name} }
func aDir(fid, name string) api.FileEntry  { return api.FileEntry{FID: fid, Name: name, Dir: true} }

// fixedTree serves a canned structure keyed by folder FID, all on one page.
func fixedTree(tree map[string][]api.FileEntry) ListPage {
	return func(fid string, page, size int) ([]api.FileEntry, error) {
		if page > 1 {
			return nil, nil
		}
		return tree[fid], nil
	}
}

// shape renders a tree as "D rel" / "F rel" lines so order is part of the
// assertion: a parent has to come before everything inside it.
func shape(tr Tree) []string {
	out := make([]string, 0, len(tr.Items))
	for _, it := range tr.Items {
		kind := "F"
		if it.Entry.Dir {
			kind = "D"
		}
		out = append(out, kind+" "+filepath.ToSlash(it.Rel))
	}
	return out
}

func TestExpandKeepsTheTree(t *testing.T) {
	list := fixedTree(map[string][]api.FileEntry{
		"资料": {aFile("f1", "说明.txt"), aDir("图片", "图片"), aDir("空", "空目录")},
		"图片": {aFile("f2", "a.png")},
		"空":  {},
	})
	tr, err := Expand(context.Background(), []api.FileEntry{aDir("资料", "资料")}, list)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{
		"D 资料",
		"F 资料/说明.txt",
		"D 资料/图片",
		"F 资料/图片/a.png",
		"D 资料/空目录",
	}
	if got := shape(tr); !equalStrings(got, want) {
		t.Fatalf("tree shape\n got %q\nwant %q", got, want)
	}
	if n := len(tr.Files()); n != 2 {
		t.Fatalf("Files() = %d, want 2", n)
	}
	if n := tr.Dirs(); n != 3 {
		t.Fatalf("Dirs() = %d, want 3", n)
	}
}

func TestExpandMixedSelection(t *testing.T) {
	list := fixedTree(map[string][]api.FileEntry{"d": {aFile("f2", "inner.bin")}})
	tr, err := Expand(context.Background(), []api.FileEntry{aFile("f1", "top.bin"), aDir("d", "folder")}, list)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	want := []string{"F top.bin", "D folder", "F folder/inner.bin"}
	if got := shape(tr); !equalStrings(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Every name below comes from the drive, so each path component is sanitised on
// its own. Joining first and sanitising after would let a crafted name walk out
// of the download directory.
func TestExpandSanitisesEveryComponent(t *testing.T) {
	list := fixedTree(map[string][]api.FileEntry{
		"d": {aFile("f1", "../../etc/passwd"), aFile("f2", `..\..\x.exe`), aFile("f3", "..")},
	})
	tr, err := Expand(context.Background(), []api.FileEntry{aDir("d", "../evil")}, list)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	root := t.TempDir()
	for _, it := range tr.Items {
		rel, err := filepath.Rel(root, filepath.Join(root, it.Rel))
		if err != nil {
			t.Fatalf("Rel(%q): %v", it.Rel, err)
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("%q escapes the download dir (rel %q)", it.Rel, rel)
		}
	}
	want := []string{"D .._evil", "F .._evil/.._.._etc_passwd", "F .._evil/.._.._x.exe", "F .._evil/download.bin"}
	if got := shape(tr); !equalStrings(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestExpandRefuses(t *testing.T) {
	full := make([]api.FileEntry, treePageSize)
	for i := range full {
		full[i] = aFile(fmt.Sprintf("f%d", i), fmt.Sprintf("%d.bin", i))
	}
	cases := []struct {
		name string
		sel  []api.FileEntry
		list ListPage
		want string
	}{
		{
			name: "条目没有 FID",
			sel:  []api.FileEntry{{Name: "无主文件"}},
			want: "缺少 FID",
		},
		{
			name: "目录包含自己",
			sel:  []api.FileEntry{aDir("d", "环")},
			list: fixedTree(map[string][]api.FileEntry{"d": {aDir("d", "又是我")}}),
			want: "包含自己",
		},
		{
			name: "清理后撞到同一个位置",
			sel:  []api.FileEntry{aFile("f1", "a/b"), aFile("f2", `a\b`)},
			want: "同一个位置",
		},
		{
			name: "只差大小写",
			sel:  []api.FileEntry{aFile("f1", "Report.TXT"), aFile("f2", "report.txt")},
			want: "同一个位置",
		},
		{
			// APFS stores one file for both spellings, so two tasks would end up
			// writing the same inode and interleaving their parts.
			name: "只差 Unicode 组合形式",
			sel:  []api.FileEntry{aFile("f1", "café.txt"), aFile("f2", "café.txt")},
			want: "同一个位置",
		},
		{
			name: "分页一直返回同一页",
			sel:  []api.FileEntry{aDir("d", "鬼打墙")},
			list: func(fid string, page, size int) ([]api.FileEntry, error) { return full, nil },
			want: "分页异常",
		},
		{
			name: "列目录失败",
			sel:  []api.FileEntry{aDir("d", "读不了")},
			list: func(fid string, page, size int) ([]api.FileEntry, error) { return nil, errors.New("boom") },
			want: "读取目录",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			list := tc.list
			if list == nil {
				list = fixedTree(nil)
			}
			_, err := Expand(context.Background(), tc.sel, list)
			if err == nil {
				t.Fatalf("想要一个错误，却成功了")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("错误是 %q，想要包含 %q", err, tc.want)
			}
		})
	}
}

// The same node listed twice is the drive repeating itself, not a conflict.
func TestExpandToleratesRepeatedNode(t *testing.T) {
	sel := []api.FileEntry{aFile("f1", "same.bin"), aFile("f1", "same.bin")}
	tr, err := Expand(context.Background(), sel, fixedTree(nil))
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if got := shape(tr); !equalStrings(got, []string{"F same.bin"}) {
		t.Fatalf("got %q, want one item", got)
	}
}

func TestExpandPagesThroughDirectory(t *testing.T) {
	var pages []int
	list := func(fid string, page, size int) ([]api.FileEntry, error) {
		pages = append(pages, page)
		if page > 2 {
			t.Fatalf("多读了一页：%d", page)
		}
		out := make([]api.FileEntry, 0, treePageSize)
		n := treePageSize
		if page == 2 {
			n = 3
		}
		for i := 0; i < n; i++ {
			out = append(out, aFile(fmt.Sprintf("p%d-%d", page, i), fmt.Sprintf("p%d-%d.bin", page, i)))
		}
		return out, nil
	}
	tr, err := Expand(context.Background(), []api.FileEntry{aDir("d", "很多文件")}, list)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	if n := len(tr.Files()); n != treePageSize+3 {
		t.Fatalf("取到 %d 个文件，想要 %d", n, treePageSize+3)
	}
	if len(pages) != 2 {
		t.Fatalf("读了 %v 页，想要正好两页", pages)
	}
}

func TestExpandStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Expand(ctx, []api.FileEntry{aFile("f1", "a.bin")}, fixedTree(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("错误是 %v，想要 context.Canceled", err)
	}
}

func TestMakeDirsCreatesEmptyFolders(t *testing.T) {
	list := fixedTree(map[string][]api.FileEntry{
		"d":  {aDir("d2", "里层"), aFile("f1", "x.bin")},
		"d2": {},
	})
	tr, err := Expand(context.Background(), []api.FileEntry{aDir("d", "外层")}, list)
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}
	root := filepath.Join(t.TempDir(), "下载")
	if err := tr.MakeDirs(root); err != nil {
		t.Fatalf("MakeDirs: %v", err)
	}
	for _, rel := range []string{"外层", filepath.Join("外层", "里层")} {
		info, err := os.Stat(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s 没建出来：%v", rel, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s 不是目录", rel)
		}
	}
}

// A path that already exists has to be a real directory. Writing through a
// symlink would put the download wherever it points, and the name it was built
// from came from the drive.
func TestMakeDirsRefusesOccupiedPath(t *testing.T) {
	for _, kind := range []string{"symlink", "file"} {
		t.Run(kind, func(t *testing.T) {
			tr, err := Expand(context.Background(), []api.FileEntry{aDir("d", "目标")}, fixedTree(nil))
			if err != nil {
				t.Fatalf("Expand: %v", err)
			}
			root := t.TempDir()
			occupied := filepath.Join(root, "目标")
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), occupied); err != nil {
					t.Skipf("这个系统建不了符号链接：%v", err)
				}
			} else if err := os.WriteFile(occupied, []byte("占位"), 0o644); err != nil {
				t.Fatal(err)
			}
			err = tr.MakeDirs(root)
			if err == nil {
				t.Fatalf("想要一个错误，却成功了")
			}
			if !strings.Contains(err.Error(), "占用") {
				t.Fatalf("错误是 %q，想要说明被占用", err)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
