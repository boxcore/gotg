//go:build !darwin

package internal

import (
	"os"
	"time"
)

// getBirthTime 在非 macOS 平台下安全回退至修改时间
func getBirthTime(info os.FileInfo) time.Time {
	return info.ModTime()
}
