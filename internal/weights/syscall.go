// Package weights provides syscall utilities for filesystem stats.
package weights

import "syscall"

type filesystemStats syscall.Statfs_t

func readFilesystemStats(path string, stat *filesystemStats) error {
	var fs syscall.Statfs_t
	if err := syscall.Statfs(path, &fs); err != nil {
		return err
	}
	*stat = filesystemStats(fs)
	return nil
}
