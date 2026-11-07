//go:build darwin

package main

import "syscall"

// diskFreeBytes returns the free space (in bytes) on the filesystem holding
// path. Uses statfs like the desktop app.
func diskFreeBytes(path string) (int64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
