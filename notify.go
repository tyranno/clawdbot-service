package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	telegramBotToken = "" // 아래에서 환경변수로 읽음
	telegramChatID   = "6723802240"
)

func getBotToken() string {
	// 1. 환경변수에서
	if token := os.Getenv("TELEGRAM_BOT_TOKEN"); token != "" {
		return token
	}

	// 2. 설정 파일에서
	configPath := userHome + `\.clawdbot\service-config.txt`
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
	token := getBotToken()
	if token == "" {
		return fmt.Errorf("no telegram bot token configured")
	}

	apiURL := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token)

	resp, err := http.PostForm(apiURL, url.Values{
		"chat_id":    {telegramChatID},
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

	msg := fmt.Sprintf("🦞 <b>Clawdbot Gateway Started</b>\n\n"+
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
}

func notifyShutdown() {
	hostname, _ := os.Hostname()
	now := time.Now().Format("2006-01-02 15:04:05")

	msg := fmt.Sprintf("🔴 <b>Clawdbot Gateway Stopped</b>\n\n"+
		"🖥 Host: %s\n"+
		"⏰ Time: %s", hostname, now)

	err := sendTelegramNotification(msg)
	if err != nil {
		log.Printf("Failed to send shutdown notification: %v", err)
	} else {
		log.Println("Shutdown notification sent to Telegram.")
	}
}
