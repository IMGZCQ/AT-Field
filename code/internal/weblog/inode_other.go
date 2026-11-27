//go:build !linux

package weblog

import "os"

// fileID 非 Linux 平台不提供 inode，轮转只靠文件变短来判断
func fileID(os.FileInfo) uint64 { return 0 }
