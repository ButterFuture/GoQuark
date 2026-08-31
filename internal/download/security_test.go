package download

import "testing"

func TestSanitizeFilenameBlocksTraversal(t *testing.T) {
	cases := map[string]string{
		"../../etc/passwd": ".._.._etc_passwd",
		`..\..\Windows\x`:  ".._.._Windows_x",
		"..":               "download.bin",
		".":                "download.bin",
		"":                 "download.bin",
		"   ":              "download.bin",
		"a/b":              "a_b",
		"C:\\evil.exe":     "C__evil.exe",
		"ok\x00.mkv":       "ok.mkv",
		"trailing...":      "trailing",
		"movie.mkv":        "movie.mkv",
		"中文 文件.zip":        "中文 文件.zip",
	}
	for in, want := range cases {
		if got := SanitizeFilename(in); got != want {
			t.Errorf("SanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
}
