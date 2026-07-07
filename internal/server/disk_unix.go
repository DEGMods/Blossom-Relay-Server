//go:build unix

package server

import "syscall"

// freeDiskMB reports free space (MB) at path. ok=false if it can't be determined.
func freeDiskMB(path string) (int64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return (int64(st.Bavail) * int64(st.Bsize)) / (1024 * 1024), true
}
