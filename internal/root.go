package internal

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var (
	task     string
	tokenStr string // 接收单个或多个逗号隔开的 Token
	chatID   string
	apiURL   string
)

// 💡 升级后的通用参数检查器：支持直接检查切片([]string)或普通字符串(string)
func ensureFlagsPresent(taskName string, requiredFlags map[string]any) {
	var missing []string

	for flagName, flagValue := range requiredFlags {
		switch v := flagValue.(type) {
		case string:
			if strings.TrimSpace(v) == "" {
				missing = append(missing, flagName)
			}
		case []string: // 如果传入的是解析后的切片，检查切片长度是否为 0
			if len(v) == 0 {
				missing = append(missing, flagName)
			}
		}
	}

	// 如果有任何一个参数不满足，打印汇总报错并退出
	if len(missing) > 0 {
		fmt.Printf("❌ 错误: 执行任务 [%s] 缺少必要参数或格式不正确: %s\n", taskName, strings.Join(missing, ", "))
		os.Exit(1)
	}
}

// 将输入的字符串按逗号切分并清洗空值
func parseTokens() []string {
	if strings.TrimSpace(tokenStr) == "" {
		return nil
	}

	var list []string
	parts := strings.Split(tokenStr, ",")
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" {
			list = append(list, trimmed)
		}
	}
	return list
}

var RootCmd = &cobra.Command{
	Use:     "gotg",
	Short:   "gotg 是一个 Telegram Bot 运维及媒体辅助工具",
	Version: "v0.1.1",
	Run: func(cmd *cobra.Command, args []string) {
		// 💡 如果命令行没有传递 Flag，则从环境变量中 Fallback
		if tokenStr == "" {
			tokenStr = os.Getenv("TOKEN")
		}
		if chatID == "" {
			chatID = os.Getenv("CHAT_ID")
		}
		if apiURL == "" {
			apiURL = os.Getenv("API_URL")
		}

		// 💡 优先解析出 activeTokens，方便后续 case 直接复用校验
		activeTokens := parseTokens()

		switch task {
		case "check_auth":
			// 💡 一行代码完成所有维度的入参拦截校验，极其干净！
			ensureFlagsPresent("check_auth", map[string]any{
				"--chat_id": chatID,
				"--token":   activeTokens,
			})

			fmt.Printf("🔄 开始检测 Bot 权限与状态 (共 %d 个 Bot)...\n", len(activeTokens))
			for i, t := range activeTokens {
				fmt.Printf("\n[Bot %d/%d] (Token: %s, ChatID: %s) -----------------------\n", i+1, len(activeTokens), maskToken(t), chatID)
				if err := CheckBotAuth(t, chatID, apiURL); err != nil {
					fmt.Printf("❌ 该 Bot 检测失败: %v\n", err)
				}
			}

		case "up":
			// 💡 同样干净地复用万能校验器
			ensureFlagsPresent("up", map[string]any{
				"--chat_id": chatID,
				"--token":   activeTokens,
			})

			if len(args) < 1 {
				fmt.Println("❌ 错误: 使用 -t=up 时，必须指定要上传的目录路径！")
				os.Exit(1)
			}

			fmt.Printf("📂 开始多 Bot 轮询/并发上传 (共 %d 个 Bot)...\n", len(activeTokens))
			for i, t := range activeTokens {
				fmt.Printf("\n[Bot %d/%d 正在分发任务] (Token: %s, ChatID: %s) -----------------------\n", i+1, len(activeTokens), maskToken(t), chatID)
				if err := UploadDirectoryFiles(t, chatID, args[0], apiURL); err != nil {
					fmt.Printf("❌ 该 Bot 上传中止: %v\n", err)
				}
			}

		case "check_media":
			// 纯本地任务，不需要校验任何 Flags
			if len(args) < 1 {
				fmt.Println("❌ 错误: 使用 -t=check_media 时，必须指定一个目标目录或文件路径！")
				os.Exit(1)
			}
			if err := CheckMediaMain(args[0]); err != nil {
				fmt.Printf("❌ 媒体解析终止: %v\n", err)
				os.Exit(1)
			}

		case "":
			fmt.Println("👋 欢迎使用 gotg 工具！请指定 -t/--task 参数。")
			_ = cmd.Help()
		default:
			fmt.Printf("❌ 未知的任务类型: %s\n", task)
		}
	},
}

func Execute() {
	if err := RootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "运行出错: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	RootCmd.Flags().StringVarP(&task, "task", "t", "", "执行的任务类型 (check_auth, up, check_media)")
	RootCmd.Flags().StringVar(&tokenStr, "token", "", "Telegram Bot Token，多个可用逗号隔开")
	RootCmd.Flags().StringVar(&chatID, "chat_id", "", "目标频道的用户名(带@)或ID(如-100xxx)")
	RootCmd.Flags().StringVar(&apiURL, "api-url", "", "自定义 Telegram Bot API Server URL (如 http://149.104.4.30:8081)")

	// 💡 加载本地 .env 配置文件
	loadEnv()
}

// 💡 加载本地 .env 文件并注入环境变量
func loadEnv() {
	file, err := os.Open(".env")
	if err != nil {
		return // 如果文件不存在，忽略
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		// 忽略空行和注释
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		// 去除包裹的引号
		val = strings.Trim(val, `"'`)

		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
}

// 💡 辅助函数：对 Token 进行脱敏处理，保留 Bot ID 和密匙的前后几位，中间部分隐藏
func maskToken(token string) string {
	parts := strings.SplitN(token, ":", 2)
	if len(parts) != 2 {
		if len(token) <= 8 {
			return "***"
		}
		return token[:3] + "***" + token[len(token)-3:]
	}
	botID := parts[0]
	secret := parts[1]
	if len(secret) <= 6 {
		return botID + ":***"
	}
	return botID + ":" + secret[:3] + "***" + secret[len(secret)-3:]
}
