package main

// Ralph autonomous task runner — Phase 3
// Spawns `claude --print` subprocess with ralph-loop plugin loaded.
// Implements its own iteration loop (Bridge-controlled) since the plugin's
// stop-hook is non-functional in --print mode. Detects <promise>X</promise>
// completion marker in result.result. Streams progress/log via TCP.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// taskRegistry tracks in-flight tasks for cancellation
var (
	taskRegistry   = make(map[string]context.CancelFunc)
	taskRegistryMu sync.Mutex
)

func registerRalphTask(taskID string, cancel context.CancelFunc) {
	taskRegistryMu.Lock()
	taskRegistry[taskID] = cancel
	taskRegistryMu.Unlock()
}

func unregisterRalphTask(taskID string) {
	taskRegistryMu.Lock()
	delete(taskRegistry, taskID)
	taskRegistryMu.Unlock()
}

func cancelRalphTask(taskID string) {
	taskRegistryMu.Lock()
	cancel, ok := taskRegistry[taskID]
	taskRegistryMu.Unlock()
	if ok && cancel != nil {
		log.Printf("[Ralph] Cancelling task %s", taskID)
		cancel()
	}
}

// ralphPluginPath returns the path to the ralph-loop Claude Code plugin.
// Currently fixed to the marketplace cache location. If missing, returns "" (plugin won't be loaded).
func ralphPluginPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	p := filepath.Join(home, ".claude", "plugins", "marketplaces", "claude-plugins-official", "plugins", "ralph-loop")
	if _, err := os.Stat(p); err == nil {
		return p
	}
	return ""
}

// taskWorkdir returns the working directory for a given task (created if missing).
func taskWorkdir(taskID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "ralph-tasks", taskID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// ralphSystemPrompt: prepended to the user prompt for autonomous task execution
const ralphSystemPrompt = `당신은 Rex (렉스)의 자율 작업 에이전트입니다.
현재 작업 폴더(.)에서 사용자 작업을 자율적으로 수행하세요.

규칙:
- 매 iteration에 한 가지 작은 단계를 진척하세요.
- 완료되면 정확히 <promise>{{PROMISE}}</promise> 태그를 출력하세요. 거짓 promise 금지.
- 미완성이면 promise를 출력하지 마세요 — 다음 iteration이 자동 실행됩니다.
- 진행 상황과 결과물은 짧고 명확하게.
- 사용자에게 전달할 파일은 [[FILE:<절대경로>]] 마커를 응답에 포함하세요.
  마커는 사용자에게 숨겨지고 다운로드 버튼으로 변환됩니다.

작업: `

// handleTaskStart processes an incoming task_start message
func handleTaskStart(conn net.Conn, req *BridgeMessage) {
	taskID := req.TaskID
	if taskID == "" {
		log.Printf("[Ralph] task_start with empty taskID, ignoring")
		return
	}
	log.Printf("[Ralph] task_start id=%s mode=%s maxIter=%d", taskID, req.Mode, req.MaxIterations)

	ctx, cancel := context.WithCancel(context.Background())
	registerRalphTask(taskID, cancel)
	defer unregisterRalphTask(taskID)

	runRalphTask(ctx, conn, req)
}

func sendTaskEvent(conn net.Conn, msg *BridgeMessage) {
	connMu.Lock()
	defer connMu.Unlock()
	if err := sendMessage(conn, msg); err != nil {
		log.Printf("[Ralph] sendMessage failed: %v", err)
	}
}

func sendTaskProgress(conn net.Conn, taskID string, iter int, message string) {
	sendTaskEvent(conn, &BridgeMessage{
		Type:        "task_progress",
		TaskID:      taskID,
		Iteration:   iter,
		TaskMessage: message,
	})
}

func sendTaskLog(conn net.Conn, taskID, line string) {
	sendTaskEvent(conn, &BridgeMessage{
		Type:   "task_log",
		TaskID: taskID,
		Line:   line,
	})
}

func sendTaskError(conn net.Conn, taskID, errStr string) {
	sendTaskEvent(conn, &BridgeMessage{
		Type:   "task_error",
		TaskID: taskID,
		Error:  errStr,
	})
}

func sendTaskDone(conn net.Conn, taskID, summary string, iterations int, artifacts []TaskArtifactJSON) {
	sendTaskEvent(conn, &BridgeMessage{
		Type:       "task_done",
		TaskID:     taskID,
		Summary:    summary,
		Iterations: iterations,
		Artifacts:  artifacts,
	})
}

// runRalphTask executes the iteration loop for a Ralph task
func runRalphTask(ctx context.Context, conn net.Conn, req *BridgeMessage) {
	taskID := req.TaskID
	maxIter := req.MaxIterations
	if maxIter <= 0 {
		maxIter = 30
	}
	completionPromise := req.CompletionPromise
	if completionPromise == "" {
		completionPromise = "DONE"
	}

	workdir, err := taskWorkdir(taskID)
	if err != nil {
		sendTaskError(conn, taskID, fmt.Sprintf("workdir failed: %v", err))
		return
	}

	pluginArgs := []string{}
	if plugin := ralphPluginPath(); plugin != "" {
		pluginArgs = []string{"--plugin-dir", plugin}
	}

	// Build initial prompt: system instructions + user prompt
	systemPart := strings.ReplaceAll(ralphSystemPrompt, "{{PROMISE}}", completionPromise)
	currentPrompt := systemPart + req.Prompt

	// Snapshot files in workdir at start (to detect new artifacts created during task)
	preFiles := snapshotFiles(workdir)

	var finalSummary string
	var iter int

	for iter = 1; iter <= maxIter; iter++ {
		select {
		case <-ctx.Done():
			sendTaskError(conn, taskID, "cancelled")
			return
		default:
		}

		sendTaskProgress(conn, taskID, iter, fmt.Sprintf("iteration %d/%d 시작", iter, maxIter))

		args := append([]string{
			"--print",
			"--output-format", "stream-json",
			"--verbose",
			"--no-session-persistence",
			"--dangerously-skip-permissions",
		}, pluginArgs...)
		args = append(args, currentPrompt)

		cmd := exec.CommandContext(ctx, "claude", args...)
		cmd.Dir = workdir

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			sendTaskError(conn, taskID, fmt.Sprintf("stdout pipe failed: %v", err))
			return
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			sendTaskError(conn, taskID, fmt.Sprintf("stderr pipe failed: %v", err))
			return
		}

		if err := cmd.Start(); err != nil {
			sendTaskError(conn, taskID, fmt.Sprintf("claude exec failed: %v (claude CLI 설치 확인)", err))
			return
		}

		// Drain stderr in background
		go func() {
			scanner := bufio.NewScanner(stderr)
			scanner.Buffer(make([]byte, 64*1024), 1024*1024)
			for scanner.Scan() {
				log.Printf("[Ralph %s stderr] %s", taskID, scanner.Text())
			}
		}()

		// Parse stream-json from stdout
		iterResult, parseErr := parseClaudeStreamJSON(stdout, conn, taskID, iter)
		_ = cmd.Wait() // wait for process to exit
		if parseErr != nil {
			sendTaskError(conn, taskID, fmt.Sprintf("stream parse failed: %v", parseErr))
			return
		}

		select {
		case <-ctx.Done():
			sendTaskError(conn, taskID, "cancelled")
			return
		default:
		}

		// Strip bkit/user-environment hook footers from result
		cleaned := stripHookFooter(iterResult)

		// Check for completion promise
		if found := extractPromise(cleaned); found == completionPromise {
			finalSummary = cleaned
			log.Printf("[Ralph] task %s completed at iteration %d", taskID, iter)
			break
		}

		// Extract any [[FILE:...]] markers and upload immediately
		uploadFileMarkersForTask(conn, taskID, cleaned)

		// Next iteration: build prompt with prior iteration as context
		currentPrompt = fmt.Sprintf("%s\n\n--- 이전 iteration %d 결과 ---\n%s\n\n--- 계속 진행 ---\n", systemPart+req.Prompt, iter, cleaned)
	}

	if finalSummary == "" {
		sendTaskError(conn, taskID, fmt.Sprintf("max iterations (%d) reached without completion promise", maxIter))
		return
	}

	// Collect artifacts: files created in workdir during this task
	artifacts := []TaskArtifactJSON{}
	postFiles := snapshotFiles(workdir)
	for path := range postFiles {
		if _, existed := preFiles[path]; existed {
			continue
		}
		// upload the new file
		url := uploadFileForTask(conn, taskID, path)
		if url != "" {
			artifacts = append(artifacts, TaskArtifactJSON{
				Name: filepath.Base(path),
				URL:  url,
				Kind: "file",
			})
		}
	}

	// Also upload any [[FILE:...]] in final summary
	uploadFileMarkersForTask(conn, taskID, finalSummary)

	sendTaskDone(conn, taskID, finalSummary, iter, artifacts)
}

// parseClaudeStreamJSON parses the stream-json output, emits log events,
// and returns the final result.result text.
func parseClaudeStreamJSON(r io.Reader, conn net.Conn, taskID string, iter int) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 256*1024), 4*1024*1024)
	var finalText string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var evt map[string]any
		if err := json.Unmarshal([]byte(line), &evt); err != nil {
			continue
		}
		t, _ := evt["type"].(string)
		switch t {
		case "result":
			if s, ok := evt["result"].(string); ok {
				finalText = s
			}
		case "assistant":
			// stream partial text via log events (optional debug visibility)
			if iter <= 1 {
				sendTaskLog(conn, taskID, "[iter "+fmt.Sprint(iter)+"] assistant turn")
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return finalText, err
	}
	return finalText, nil
}

// extractPromise extracts the content of <promise>X</promise> tag, returns "" if not found.
var promiseRE = regexp.MustCompile(`(?s)<promise>(.*?)</promise>`)

func extractPromise(text string) string {
	m := promiseRE.FindStringSubmatch(text)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// stripHookFooter removes known user-environment hook footers (bkit etc.)
// from the result text so iteration parsing isn't confused.
var bkitFooterRE = regexp.MustCompile(`(?s)─+\s*\n?📊\s*bkit Feature Usage.*$`)

func stripHookFooter(text string) string {
	cleaned := bkitFooterRE.ReplaceAllString(text, "")
	return strings.TrimSpace(cleaned)
}

// snapshotFiles returns a map of file paths in dir with modtime stamps
func snapshotFiles(dir string) map[string]int64 {
	out := make(map[string]int64)
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		out[path] = info.ModTime().Unix()
		return nil
	})
	return out
}

// uploadFileMarkersForTask scans text for [[FILE:...]] markers and uploads each.
func uploadFileMarkersForTask(conn net.Conn, taskID, text string) {
	matches := taskFileMarkerRE.FindAllStringSubmatch(text, -1)
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		go uploadFileForTask(conn, taskID, m[1])
	}
}

var taskFileMarkerRE = regexp.MustCompile(`\[\[FILE:(.+?)\]\]`)

// uploadFileForTask uploads a file and sends file_response with RequestID == taskID.
// Returns the download URL, or "" on failure.
func uploadFileForTask(conn net.Conn, taskID, filePath string) string {
	url, size := uploadFileToServer(filePath)
	if url == "" {
		return ""
	}
	sendTaskEvent(conn, &BridgeMessage{
		Type:      "file_response",
		RequestID: taskID, // server routes file by RequestID; task channel falls back
		Filename:  filepath.Base(filePath),
		FileURL:   url,
		FileSize:  size,
	})
	return url
}
