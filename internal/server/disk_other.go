//go:build !unix

package server

// freeDiskMB is a no-op on non-unix (dev on Windows); production runs on Linux.
func freeDiskMB(path string) (int64, bool) { return 0, false }
