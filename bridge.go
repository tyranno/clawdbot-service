package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Bridge config
var (
	bridgeServerAddr string // GCP server address (host:port)
	bridgeToken      string // Authentication token
	bridgeName       string // Instance name (e.g. "회사 렉스")
	openclawURL      string // Local OpenClaw Gateway URL
	openclawToken    string // Local OpenClaw auth token
)

func loadBridgeConfig() {
	configPath := userHome + `\.openclaw\service-config.txt`
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("[Bridge] No config file: %v", err)
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "BRIDGE_SERVER=") {
			bridgeServerAddr = strings.TrimPrefix(line, "BRIDGE_SERVER=")
		} else if strings.HasPrefix(line, "BRIDGE_TOKEN=") {
			bridgeToken = strings.TrimPrefix(line, "BRIDGE_TOKEN=")
		} else if strings.HasPrefix(line, "BRIDGE_NAME=") {
			bridgeName = strings.TrimPrefix(line, "BRIDGE_NAME=")
		} else if strings.HasPrefix(line, "OPENCLAW_URL=") {
			openclawURL = strings.TrimPrefix(line, "OPENCLAW_URL=")
		} else if strings.HasPrefix(line, "OPENCLAW_TOKEN=") {
			openclawToken = strings.TrimPrefix(line, "OPENCLAW_TOKEN=")
		}
	}

	if openclawURL == "" {
		openclawURL = "http://localhost:18789"
	}
	if bridgeName == "" {
		hostname, _ := os.Hostname()
		bridgeName = hostname
	}
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
	loadBridgeConfig()

	if bridgeServerAddr == "" {
		log.Println("[Bridge] No BRIDGE_SERVER configured, bridge disabled")
		return
	}

	log.Printf("[Bridge] Connecting to %s as '%s'", bridgeServerAddr, bridgeName)

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
	conn, err := net.DialTimeout("tcp", bridgeServerAddr, 10*time.Second)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	log.Printf("[Bridge] Connected to %s", bridgeServerAddr)

	// Register
	err = sendMessage(conn, &BridgeMessage{
		Type:  "register",
		Name:  bridgeName,
		Token: bridgeToken,
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

func handleChatRequest(conn net.Conn, req *BridgeMessage) {
	log.Printf("[Bridge] Chat request: %s", req.RequestID)

	// Build OpenClaw request
	body := map[string]interface{}{
		"model":    "openclaw",
		"stream":   true,
		"user":     "voicechat-app",
		"messages": json.RawMessage(req.Messages),
	}
	bodyData, _ := json.Marshal(body)

	headers := map[string]string{
		"Content-Type":         "application/json",
		"x-openclaw-agent-id":  "main",
	}
	if openclawToken != "" {
		headers["Authorization"] = "Bearer " + openclawToken
	}

	httpReq, _ := http.NewRequest("POST", openclawURL+"/v1/chat/completions", strings.NewReader(string(bodyData)))
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

	// Stream SSE response back through TCP
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
			connMu.Lock()
			sendMessage(conn, &BridgeMessage{
				Type:      "chat_response",
				RequestID: req.RequestID,
				Delta:     parsed.Choices[0].Delta.Content,
			})
			connMu.Unlock()
		}
	}

	// Send done
	connMu.Lock()
	sendMessage(conn, &BridgeMessage{
		Type:      "chat_response",
		RequestID: req.RequestID,
		Done:      true,
	})
	connMu.Unlock()

	log.Printf("[Bridge] Chat complete: %s", req.RequestID)
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
