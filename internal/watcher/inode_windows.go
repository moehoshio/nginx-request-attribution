//go:build windows

package watcher

import "os"

// fileInode returns 0 on Windows, where syscall.Stat_t is not
// available. Rotation detection then relies on the remaining file
// metadata and fingerprint checks.
func fileInode(info os.FileInfo) uint64 {
	return 0
}
