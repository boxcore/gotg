//go:build darwin

package internal

import (
	"os"
	"syscall"
	"time"
)

// getBirthTime 获取 macOS 平台的文件创建时间 (Birthtime)
func getBirthTime(info os.FileInfo) time.Time {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(stat.Birthtimespec.Sec, stat.Birthtimespec.Nsec)
	}
	return info.ModTime()
}
