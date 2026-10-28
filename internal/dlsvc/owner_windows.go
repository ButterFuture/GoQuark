//go:build windows

package dlsvc

import "os"

// The temp directory on Windows is already per user.
func ownedByMe(os.FileInfo) bool { return true }
