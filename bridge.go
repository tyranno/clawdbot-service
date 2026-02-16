package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Package-level vars for handlers
var (
	pkgBridgeServer  string
	pkgBridgeToken   string
	pkgBridgeName    string
	pkgOpenclawURL   string
	pkgOpenclawToken string
)

// getBridgeConfig returns bridge settings from central config
func getBridgeConfig() (serverAddr, token, name, clawURL, clawToken string) {
	cfg := GetConfig()
	return cfg.BridgeServer, cfg.BridgeToken, cfg.BridgeName, cfg.OpenclawURL, cfg.OpenclawToken
}

// TCP Protocol: 4-byte big-endian length + JSON body
type BridgeMessage struct {
	Type      string          `json:"type"`
	Name      string          `json:"name,omitempty"`
	Token     string          `json:"token,omitempty"`
	RequestID string          `json:"requestId,omitempty"`
	Messages  json.RawMessage `json:"messages,omitempty"`
	Delta     string          `json:"delta,omitempty"`
	Done      bool            `json:"done,omitempty"`
	Error     string          `json:"error,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	FileURL   string          `json:"url,omitempty"`
	FileSize  int64           `json:"size,omitempty"`
	MimeType  string          `json:"mimeType,omitempty"`
	User      string          `json:"user,omitempty"`
}

func sendMessage(conn net.Conn, msg *BridgeMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	// 4-byte length header
	header := make([]byte, 4)
	binary.BigEndian.PutUint32(header, uint32(len(data)))
	if _, err := conn.Write(header); err != nil {
		return err
	}
	_, err = conn.Write(data)
	return err
}

func readMessage(conn net.Conn) (*BridgeMessage, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return nil, err
	}
	length := binary.BigEndian.Uint32(header)
	if length > 10*1024*1024 { // 10MB max
		return nil, fmt.Errorf("message too large: %d", length)
	}
	data := make([]byte, length)
	if _, err := io.ReadFull(conn, data); err != nil {
		return nil, err
	}
	var msg BridgeMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// StartBridge connects to GCP server and handles requests
func StartBridge(ctx context.Context) {
	bridgeServerAddr, bridgeToken, bridgeName, openclawURL, openclawToken := getBridgeConfig()

	if bridgeServerAddr == "" {
		log.Println("[Bridge] No BRIDGE_SERVER configured, bridge disabled")
		return
	}

	log.Printf("[Bridge] Connecting to %s as '%s'", bridgeServerAddr, bridgeName)
	
	// Store in package vars for use in handlers
	pkgBridgeServer = bridgeServerAddr
	pkgBridgeToken = bridgeToken
	pkgBridgeName = bridgeName
	pkgOpenclawURL = openclawURL
	pkgOpenclawToken = openclawToken

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		err := bridgeSession(ctx)
		if err != nil {
			log.Printf("[Bridge] Session error: %v", err)
		}

		// Wait before reconnect
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
			log.Println("[Bridge] Reconnecting...")
		}
	}
}

func bridgeSession(ctx context.Context) error {
	// Use TLS connection
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	conn, err := tls.DialWithDialer(dialer, "tcp", pkgBridgeServer, &tls.Config{
		MinVersion: tls.VersionTLS12,
	})
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	log.Printf("[Bridge] Connected to %s (TLS)", pkgBridgeServer)

	// Register
	err = sendMessage(conn, &BridgeMessage{
		Type:  "register",
		Name:  pkgBridgeName,
		Token: pkgBridgeToken,
	})
	if err != nil {
		return fmt.Errorf("register: %w", err)
	}

	// Start heartbeat
	heartbeatCtx, heartbeatCancel := context.WithCancel(ctx)
	defer heartbeatCancel()
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				sendMessage(conn, &BridgeMessage{Type: "heartbeat"})
			}
		}
	}()

	// Read loop
	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		msg, err := readMessage(conn)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}

		switch msg.Type {
		case "heartbeat":
			// Server heartbeat ack, ignore
		case "chat_request":
			go handleChatRequest(conn, msg)
		default:
			log.Printf("[Bridge] Unknown message type: %s", msg.Type)
		}
	}
}

var connMu sync.Mutex

// voiceChatSystemPrompt is injected before user messages so the AI knows
// how to send files back to the VoiceChat app.
const voiceChatSystemPrompt = `You are Rex (렉스 🦖), a helpful voice assistant running on the user's computer.
You speak Korean (존댓말). Be concise — this is a voice interface.

## File Sending
When the user asks you to create, send, or download a file:
1. Create the file on disk (e.g. C:\temp\filename.ext)
2. Include the marker [[FILE:C:\full\path\to\file]] in your response
3. The app will automatically detect this and show a download button
Example: "파일을 만들었어요! [[FILE:C:\temp\report.txt]]"
The marker will be hidden from the user — they only see the download card.
Always use C:\temp\ as the default directory for created files.`

func handleChatRequest(conn net.Conn, req *BridgeMessage) {
	log.Printf("[Bridge] Chat request: %s", req.RequestID)

	// Prepend system prompt to messages
	var userMessages []json.RawMessage
	if err := json.Unmarshal(req.Messages, &userMessages); err != nil {
		log.Printf("[Bridge] Failed to parse messages: %v", err)
		userMessages = nil
	}
	sysMsg := map[string]string{"role": "system", "content": voiceChatSystemPrompt}
	sysMsgBytes, _ := json.Marshal(sysMsg)
	allMessages := append([]json.RawMessage{sysMsgBytes}, userMessages...)
	allMessagesBytes, _ := json.Marshal(allMessages)

	// Build OpenClaw request — user field determines session
	user := req.User
	if user == "" {
		user = "voicechat-app"
	}
	body := map[string]interface{}{
		"model":    "openclaw",
		"stream":   true,
		"user":     user,
		"messages": json.RawMessage(allMessagesBytes),
	}
	bodyData, _ := json.Marshal(body)

	headers := map[string]string{
		"Content-Type":         "application/json",
		"x-openclaw-agent-id":  "main",
	}
	if pkgOpenclawToken != "" {
		headers["Authorization"] = "Bearer " + pkgOpenclawToken
	}

	httpReq, _ := http.NewRequest("POST", pkgOpenclawURL+"/v1/chat/completions", strings.NewReader(string(bodyData)))
	for k, v := range headers {
		httpReq.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		connMu.Lock()
		sendMessage(conn, &BridgeMessage{
			Type:      "chat_error",
			RequestID: req.RequestID,
			Error:     fmt.Sprintf("OpenClaw error: %v", err),
		})
		connMu.Unlock()
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		errBody, _ := io.ReadAll(resp.Body)
		connMu.Lock()
		sendMessage(conn, &BridgeMessage{
			Type:      "chat_error",
			RequestID: req.RequestID,
			Error:     fmt.Sprintf("OpenClaw HTTP %d: %s", resp.StatusCode, string(errBody)),
		})
		connMu.Unlock()
		return
	}

	// Stream SSE response back through TCP, collecting full response
	var fullResponse strings.Builder
	scanner := NewSSEScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}

		var parsed struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}
		if len(parsed.Choices) > 0 && parsed.Choices[0].Delta.Content != "" {
			delta := parsed.Choices[0].Delta.Content
			fullResponse.WriteString(delta)
			connMu.Lock()
			sendMessage(conn, &BridgeMessage{
				Type:      "chat_response",
				RequestID: req.RequestID,
				Delta:     delta,
			})
			connMu.Unlock()
		}
	}

	// Send done first so the app knows text streaming is complete
	connMu.Lock()
	sendMessage(conn, &BridgeMessage{
		Type:      "chat_response",
		RequestID: req.RequestID,
		Done:      true,
	})
	connMu.Unlock()

	// Scan for file markers: [[FILE:/path/to/file]]
	filePattern := regexp.MustCompile(`\[\[FILE:(.+?)\]\]`)
	matches := filePattern.FindAllStringSubmatch(fullResponse.String(), -1)
	for _, match := range matches {
		filePath := match[1]
		log.Printf("[Bridge] Found file marker: %s", filePath)
		go func(fp string) {
			uploadAndNotify(conn, req.RequestID, fp)
		}(filePath)
	}

	log.Printf("[Bridge] Chat complete: %s", req.RequestID)
}

// uploadAndNotify uploads a local file to the GCP server and sends a file_response message
func uploadAndNotify(conn net.Conn, requestID, filePath string) {
	cfg := GetConfig()
	serverURL := cfg.BridgeServer // e.g. "34.64.164.13:9443"
	// Derive HTTPS upload URL from bridge server address
	host := serverURL
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		host = host[:idx]
	}
	uploadURL := fmt.Sprintf("https://%s/api/files/upload", host)

	// Read file
	f, err := os.Open(filePath)
	if err != nil {
		log.Printf("[Bridge] Cannot open file %s: %v", filePath, err)
		return
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		log.Printf("[Bridge] Cannot stat file %s: %v", filePath, err)
		return
	}

	// Build multipart form
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	part, err := writer.CreateFormFile("file", filepath.Base(filePath))
	if err != nil {
		log.Printf("[Bridge] Multipart error: %v", err)
		return
	}
	if _, err := io.Copy(part, f); err != nil {
		log.Printf("[Bridge] Copy error: %v", err)
		return
	}
	writer.Close()

	// Upload with TLS (skip verify for self-signed certs)
	client := &http.Client{
		Timeout: 60 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
	}
	uploadReq, _ := http.NewRequest("POST", uploadURL, &buf)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())

	uploadResp, err := client.Do(uploadReq)
	if err != nil {
		log.Printf("[Bridge] Upload failed: %v", err)
		return
	}
	defer uploadResp.Body.Close()

	if uploadResp.StatusCode != 200 {
		body, _ := io.ReadAll(uploadResp.Body)
		log.Printf("[Bridge] Upload HTTP %d: %s", uploadResp.StatusCode, string(body))
		return
	}

	var result struct {
		ID          string `json:"id"`
		Filename    string `json:"filename"`
		Size        int64  `json:"size"`
		DownloadURL string `json:"downloadUrl"`
	}
	if err := json.NewDecoder(uploadResp.Body).Decode(&result); err != nil {
		log.Printf("[Bridge] Upload response parse error: %v", err)
		return
	}

	log.Printf("[Bridge] File uploaded: %s -> %s", filePath, result.DownloadURL)

	// Send file_response to server
	connMu.Lock()
	sendMessage(conn, &BridgeMessage{
		Type:      "file_response",
		RequestID: requestID,
		Delta:     "",
		Filename:  result.Filename,
		FileURL:   result.DownloadURL,
		FileSize:  fi.Size(),
	})
	connMu.Unlock()
}

// SSEScanner reads SSE lines from a reader
type SSEScanner struct {
	reader *io.Reader
	buf    []byte
	line   string
}

func NewSSEScanner(r io.Reader) *bufioSSEScanner {
	return &bufioSSEScanner{reader: r}
}

type bufioSSEScanner struct {
	reader  io.Reader
	buf     []byte
	line    string
	err     error
}

func (s *bufioSSEScanner) Scan() bool {
	var line []byte
	one := make([]byte, 1)
	for {
		n, err := s.reader.Read(one)
		if n > 0 {
			if one[0] == '\n' {
				s.line = string(line)
				return true
			}
			if one[0] != '\r' {
				line = append(line, one[0])
			}
		}
		if err != nil {
			if len(line) > 0 {
				s.line = string(line)
				return true
			}
			s.err = err
			return false
		}
	}
}

func (s *bufioSSEScanner) Text() string {
	return s.line
}
