package internal

import (
	"context" // 💡 引入 context 包
	"fmt"

	"github.com/mymmrac/telego"
)

// CheckBotAuth 验证 Token 并检查 Bot 是否为频道的管理员
func CheckBotAuth(token string, chatIDStr string, apiURL string) error {
	var opts []telego.BotOption
	opts = append(opts, telego.WithDefaultLogger(false, false))
	if apiURL != "" {
		opts = append(opts, telego.WithAPIServer(apiURL))
	}
	bot, err := telego.NewBot(token, opts...)
	if err != nil {
		return fmt.Errorf("Token 格式不正确: %w", err)
	}

	// 💡 传入 context.Background() 解决编译报错
	botUser, err := bot.GetMe(context.Background())
	if err != nil {
		return fmt.Errorf("Token 验证失败 (getMe 报错): %w", err)
	}
	fmt.Printf("✅ Token 正常！Bot 用户名: @%s (ID: %d)\n", botUser.Username, botUser.ID)

	chatID := telego.ChatID{Username: chatIDStr}

	// 💡 传入 context.Background() 解决编译报错
	member, err := bot.GetChatMember(context.Background(), &telego.GetChatMemberParams{
		ChatID: chatID,
		UserID: botUser.ID,
	})
	if err != nil {
		return fmt.Errorf("无法获取频道成员信息（可能 Bot 不在该频道中，或 chat_id 错误）: %w", err)
	}

	status := member.MemberStatus()
	switch status {
	case "creator":
		fmt.Println("👑 检查完毕：该 Bot 是该频道的【创建者/所有者】。")
	case "administrator":
		fmt.Println("🛠️ 检查完毕：该 Bot 是该频道的【管理员】。")
	default:
		return fmt.Errorf("❌ 检查完毕：该 Bot 处于该频道中，但【不是管理员】(当前状态: %s)", status)
	}

	return nil
}
