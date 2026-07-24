//go:build !demo

package api

func mockListDir(pdirFID string) []FileEntry {
	_ = pdirFID
	return nil
}
