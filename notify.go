package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const telegramBotToken = "" // 아래에서 환경변수로 읽음

func getChatID() string {
	if id := os.Getenv("TELEGRAM_CHAT_ID"); id != "" {
		return id
	}
	if id := GetConfig().TelegramChatID; id != "" {
		return id
	}
	return ""
}

func getBotToken() string {
	// 1. 환경변수에서
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		return token
	}

	// 2. 설정 파일에서
	configPath := userHome + `\.openclaw\service-config.txt`
	data, err := os.ReadFile(configPath)
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "TELEGRAM_BOT_TOKEN=") {
				return strings.TrimPrefix(line, "TELEGRAM_BOT_TOKEN=")
			}
		}
	}

	return ""
}

func sendTelegramNotification(message string) error {
	// Check if notifications are enabled
	if !GetConfig().NotifyEnabled {
		log.Println("[Notify] Notifications disabled, skipping")
		return nil
	}

	token := getBotToken()
	if token == "" {
		return fmt.Errorf("no telegram bot token configured")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	chatID := getChatID()
	if chatID == "" {
		return fmt.Errorf("no telegram chat ID configured")
	}

	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id":    {chatID},
		"text":       {message},
		"parse_mode": {"HTML"},
	})
	if err != nil {
		return fmt.Errorf("failed to send telegram message: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("telegram API error %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// sendVoiceChatNotification sends a push notification via the VoiceChat server WebSocket notify API
func sendVoiceChatNotification(title, message string) error {
	if !GetConfig().NotifyEnabled {
		return nil
	}

	// Strip HTML tags (plain text for app)
	plain := stripHTML(message)

	payload := fmt.Sprintf(`{"title":%q,"body":%q}`, title, plain)
	resp, err := http.Post(
		"https://voicechat.tyranno.xyz/api/notify",
		"application/json; charset=utf-8",
		strings.NewReader(payload),
	)
	if err != nil {
		return fmt.Errorf("VoiceChat notify failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("VoiceChat notify API error %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Sent int `json:"sent"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	log.Printf("[Notify] VoiceChat notification sent (delivered to %d clients)", result.Sent)
	return nil
}

// stripHTML removes basic HTML tags for plain text display
func stripHTML(s string) string {
	result := s
	for _, tag := range []string{"<b>", "</b>", "<i>", "</i>", "<code>", "</code>"} {
		result = strings.ReplaceAll(result, tag, "")
	}
	return result
}

func waitForNetwork(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		_, err := http.Get("https://api.telegram.org")
		if err == nil {
			return true
		}
		log.Printf("Waiting for network... (%v)", err)
		time.Sleep(5 * time.Second)
	}
	return false
}

func notifyStartup() {
	// Wait for network to be ready (up to 60s)
	if !waitForNetwork(60 * time.Second) {
		log.Println("Network not available after 60s, skipping startup notification")
		return
	}

	hostname, _ := os.Hostname()
	now := time.Now().Format("2006-01-02 15:04:05")

	msg := fmt.Sprintf("🦖 <b>OpenClaw Gateway Started</b>\n\n"+
		"🖥 Host: %s\n"+
		"⏰ Time: %s\n"+
		"🔌 Port: 18789\n"+
		"✅ Service is running!", hostname, now)

	err := sendTelegramNotification(msg)
	if err != nil {
		log.Printf("Failed to send startup notification: %v", err)
	} else {
		log.Println("Startup notification sent to Telegram.")
	}
	if err := sendVoiceChatNotification("🦖 Gateway Started", msg); err != nil {
		log.Printf("VoiceChat startup notification failed: %v", err)
	}
}

func notifyShutdown() {
	hostname, _ := os.Hostname()
	now := time.Now().Format("2006-01-02 15:04:05")

	msg := fmt.Sprintf("🔴 <b>OpenClaw Gateway Stopped</b>\n\n"+
		"🖥 Host: %s\n"+
		"⏰ Time: %s", hostname, now)

	err := sendTelegramNotification(msg)
	if err != nil {
		log.Printf("Failed to send shutdown notification: %v", err)
	} else {
		log.Println("Shutdown notification sent to Telegram.")
	}
	if err := sendVoiceChatNotification("🔴 Gateway Stopped", msg); err != nil {
		log.Printf("VoiceChat shutdown notification failed: %v", err)
	}
}
