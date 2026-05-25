//go:build !windows

package watcher

import (
	"os"
	"syscall"
)

// fileInode returns the file's inode number on platforms that expose
// it via syscall.Stat_t.
func fileInode(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0
	}
	return uint64(stat.Ino)
}
