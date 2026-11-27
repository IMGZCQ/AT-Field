//go:build linux

package weblog

import (
	"os"
	"syscall"
)

// fileID 返回文件 inode，用于识别日志轮转（非 Linux 返回 0）
func fileID(fi os.FileInfo) uint64 {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Ino)
	}
	return 0
}
