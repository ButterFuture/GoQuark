package download

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/text/unicode/norm"

	"github.com/ButterFuture/GoQuark/internal/api"
)

// ListPage reads one page of a cloud directory. The caller supplies it so the
// expansion below has no opinion about the API client, and so tests can hand it
// a fixed tree.
type ListPage func(fid string, page, size int) ([]api.FileEntry, error)

const (
	// treePageSize is the page size asked of the directory API.
	treePageSize = 200
	// treeMaxItems bounds one expansion. Nothing legitimate on a personal drive
	// comes close; a listing that keeps inventing names cannot grow the plan
	// without end.
	treeMaxItems = 50000
)

// TreeItem is one cloud node placed at a path under the download root.
type TreeItem struct {
	Entry api.FileEntry
	// Rel is where the node goes, relative to the download root. It is built by
	// joining each name component after sanitising that component on its own.
	Rel string
}

// Tree is a selection expanded in pre-order: a directory always comes before
// the nodes inside it. Creating the directories in this order therefore never
// needs MkdirAll, which would happily follow a symlink partway down.
type Tree struct {
	Items []TreeItem
}

// Files returns the items that are files, in expansion order.
func (t Tree) Files() []TreeItem {
	out := make([]TreeItem, 0, len(t.Items))
	for _, it := range t.Items {
		if !it.Entry.Dir {
			out = append(out, it)
		}
	}
	return out
}

// Dirs reports how many items are directories.
func (t Tree) Dirs() int {
	n := 0
	for _, it := range t.Items {
		if it.Entry.Dir {
			n++
		}
	}
	return n
}

// Expand turns a selection of files and folders into the tree to fetch,
// descending into every folder. Every name in the result came from the remote
// API, so each component is sanitised before it is joined: whatever the server
// sends can only ever name something inside the download root.
//
// It refuses rather than guesses. A missing FID, a folder that contains itself,
// two different nodes that would land on one local path, or a listing that
// stops making progress all abort the whole expansion, because a half-expanded
// selection is worse than none.
func Expand(ctx context.Context, sel []api.FileEntry, list ListPage) (Tree, error) {
	x := &expander{list: list, claimed: make(map[string]api.FileEntry)}
	for _, e := range sel {
		if err := x.visit(ctx, e, "", nil); err != nil {
			return Tree{}, err
		}
	}
	return Tree{Items: x.items}, nil
}

type expander struct {
	list    ListPage
	items   []TreeItem
	claimed map[string]api.FileEntry // local path key -> node holding it
}

// visit places one node under parent and, for a folder, descends into it.
// ancestors holds the FIDs of the folders we are currently inside, so a folder
// that lists itself is caught before it recurses.
func (x *expander) visit(ctx context.Context, e api.FileEntry, parent string, ancestors []string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.FID == "" {
		return fmt.Errorf("云端条目缺少 FID：%s", e.Name)
	}
	if len(x.items) >= treeMaxItems {
		return fmt.Errorf("待下载条目超过 %d 个，已停止扫描", treeMaxItems)
	}
	rel := filepath.Join(parent, SanitizeFilename(e.Name))
	fresh, err := x.claim(rel, e)
	if err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	x.items = append(x.items, TreeItem{Entry: e, Rel: rel})
	if !e.Dir {
		return nil
	}
	return x.descend(ctx, e.FID, rel, append(ancestors, e.FID))
}

// claim records that rel belongs to e. It reports whether this is the first
// time rel has been claimed; the same node listed twice is not an error, two
// different nodes on one path is.
func (x *expander) claim(rel string, e api.FileEntry) (bool, error) {
	key := localPathKey(rel)
	held, taken := x.claimed[key]
	if !taken {
		x.claimed[key] = e
		return true, nil
	}
	if held.FID == e.FID && held.Dir == e.Dir {
		return false, nil
	}
	return false, fmt.Errorf("两个云端条目会写到同一个位置 %s：%s / %s", rel, held.Name, e.Name)
}

// descend pages through one folder. Each page has to contribute at least one
// new node: that is the termination condition. A listing that returns the same
// page over and over would otherwise be walked forever, since re-seeing a node
// we already have is deliberately not an error.
func (x *expander) descend(ctx context.Context, fid, rel string, ancestors []string) error {
	for page := 1; ; page++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		entries, err := x.list(fid, page, treePageSize)
		if err != nil {
			return fmt.Errorf("读取目录 %s 第 %d 页：%w", displayRel(rel), page, err)
		}
		if len(entries) == 0 {
			return nil
		}
		before := len(x.items)
		for _, child := range entries {
			if slices.Contains(ancestors, child.FID) {
				return fmt.Errorf("目录 %s 包含自己，已停止扫描", displayRel(rel))
			}
			if err := x.visit(ctx, child, rel, ancestors); err != nil {
				return err
			}
		}
		if len(x.items) == before {
			return fmt.Errorf("目录 %s 第 %d 页没有新内容，分页异常", displayRel(rel), page)
		}
		if len(entries) < treePageSize {
			return nil
		}
	}
}

// MakeDirs creates the tree's folders under root, including the empty ones.
// Items order puts a parent before its children, so one Mkdir per level is
// enough. A path that is already there has to be a real directory: the check is
// Lstat, not Stat, so a symlink is refused instead of followed somewhere else.
func (t Tree) MakeDirs(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("创建下载目录：%w", err)
	}
	for _, it := range t.Items {
		if !it.Entry.Dir {
			continue
		}
		dir := filepath.Join(root, it.Rel)
		info, err := os.Lstat(dir)
		switch {
		case err == nil && info.IsDir():
			// already there, reuse it
		case err == nil:
			return fmt.Errorf("目标位置已被%s占用：%s", pathKind(info), dir)
		case os.IsNotExist(err):
			if err := os.Mkdir(dir, 0o755); err != nil {
				return fmt.Errorf("创建目录 %s：%w", dir, err)
			}
		default:
			return fmt.Errorf("检查目录 %s：%w", dir, err)
		}
	}
	return nil
}

// localPathKey folds a relative path the way the filesystem underneath is
// likely to. APFS and NTFS are both case-insensitive, and APFS also ignores
// Unicode composition, so "Café" and "café" name one file there. Folding
// both before comparing is what keeps two such nodes from being handed the same
// destination and then racing each other over it.
func localPathKey(rel string) string {
	return strings.ToLower(norm.NFC.String(rel))
}

// pathKind describes what is sitting at a path, for an error the user can act
// on rather than a mode bitmask.
func pathKind(info os.FileInfo) string {
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return "符号链接"
	case info.IsDir():
		return "目录"
	default:
		return "文件"
	}
}

// displayRel names a folder in an error. The top-level selection has no
// relative path yet, so give it something readable.
func displayRel(rel string) string {
	if rel == "" {
		return "根目录"
	}
	return rel
}
