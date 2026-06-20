package internal

import (
	"context" // 💡 引入 context 包
	"fmt"
	"os"
	"path/filepath"

	"github.com/mymmrac/telego"
)

// UploadDirectoryFiles 读取 targetPath 下的一级文件并上传到指定频道
func UploadDirectoryFiles(token string, chatIDStr string, targetPath string, apiURL string) error {
	cleanPath := filepath.Clean(targetPath)
	fileInfo, err := os.Stat(cleanPath)
	if err != nil {
		return fmt.Errorf("路径不存在或无法访问: %w", err)
	}

	if !fileInfo.IsDir() {
		return fmt.Errorf("指定的路径 '%s' 不是一个目录", targetPath)
	}

	entries, err := os.ReadDir(cleanPath)
	if err != nil {
		return fmt.Errorf("读取目录失败: %w", err)
	}

	var opts []telego.BotOption
	opts = append(opts, telego.WithDefaultLogger(false, false))
	if apiURL != "" {
		opts = append(opts, telego.WithAPIServer(apiURL))
	}
	bot, err := telego.NewBot(token, opts...)
	if err != nil {
		return fmt.Errorf("初始化 Bot 失败: %w", err)
	}
	chatID := telego.ChatID{Username: chatIDStr}

	fmt.Printf("📂 开始扫描目录: %s\n", cleanPath)

	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		info, err := entry.Info()
		if err != nil {
			fmt.Printf("❌ 获取文件信息失败 (%s): %v\n", entry.Name(), err)
			continue
		}

		limit := int64(50 * 1024 * 1024) // 50MB 官方限制
		limitStr := "50MB"
		if apiURL != "" {
			limit = 2000 * 1024 * 1024 // 2GB 自定义限制
			limitStr = "2GB"
		}

		if info.Size() > limit {
			fmt.Printf("❌ 跳过 %s (大小 %s 超过 %s 限制)\n", entry.Name(), formatSize(info.Size()), limitStr)
			continue
		}

		filePath := filepath.Join(cleanPath, entry.Name())
		fmt.Printf("🚀 正在上传: %s ... ", entry.Name())

		file, err := os.Open(filePath)
		if err != nil {
			fmt.Printf("❌ 打开失败: %v\n", err)
			continue
		}

		// 💡 传入 context.Background() 解决编译报错
		_, err = bot.SendDocument(context.Background(), &telego.SendDocumentParams{
			ChatID:   chatID,
			Document: telego.InputFile{File: file},
		})
		_ = file.Close()

		if err != nil {
			fmt.Printf("❌ 上传失败: %v\n", err)
		} else {
			fmt.Println("✅ 成功")
			count++
		}
	}

	fmt.Printf("🎉 上传任务结束，成功上传了 %d 个文件。\n", count)
	return nil
}

func formatSize(bytes int64) string {
	if bytes >= 1024*1024*1024 {
		return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
	}
	return fmt.Sprintf("%.2f MB", float64(bytes)/(1024*1024))
}
