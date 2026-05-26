//go:build unix

package executor

import (
	"os"
	"syscall"
)

const syscallNoFollow = syscall.O_NOFOLLOW

// sameFile returns true when both FileInfo objects refer to the same on-disk
// inode + device. Used as the TOCTOU guard between safeReadAll and
// safeWriteReplace's pre-rename re-stat.
func sameFile(a, b os.FileInfo) bool {
	sa, ok1 := a.Sys().(*syscall.Stat_t)
	sb, ok2 := b.Sys().(*syscall.Stat_t)
	if !ok1 || !ok2 {
		// Fall back to size+mtime+mode if syscall data is unavailable.
		return a.Size() == b.Size() && a.ModTime().Equal(b.ModTime()) && a.Mode() == b.Mode()
	}
	return sa.Dev == sb.Dev && sa.Ino == sb.Ino && a.Size() == b.Size()
}
