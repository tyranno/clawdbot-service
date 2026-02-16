package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
)

type gatewayService struct{}

func (s *gatewayService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Setup logging
	logDir := filepath.Join(userHome, ".openclaw", "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "service.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
	} else {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	log.Println("OpenClaw Gateway service starting...")
	log.Printf("[Service] userHome=%s", userHome)
	log.Printf("[Service] USERPROFILE=%s", os.Getenv("USERPROFILE"))

	// Load configuration
	CreateDefaultConfig()
	LoadConfig()
	cfg := GetConfig()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Start gateway process (always enabled)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runGateway(ctx)
	}()

	// Start worktime monitor (if enabled)
	if cfg.WorktimeEnabled {
		log.Println("[Service] Worktime monitor: enabled")
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartWorkTimeMonitor(ctx)
		}()

		// Start idle pipe server (required for worktime)
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartIdlePipeServer(ctx)
		}()
	} else {
		log.Println("[Service] Worktime monitor: disabled")
	}

	// Start ClawBridge (if enabled)
	if cfg.BridgeEnabled && cfg.BridgeServer != "" {
		log.Printf("[Service] ClawBridge: enabled (server=%s)", cfg.BridgeServer)
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartBridge(ctx)
		}()
	} else {
		log.Println("[Service] ClawBridge: disabled")
	}

	// Watch config for hot-reload (triggers service restart)
	WatchConfig(func() {
		log.Println("[Service] Config changed, requesting restart...")
		// Signal the service to stop and restart
		go func() {
			time.Sleep(1 * time.Second)
			// Send stop signal to ourselves
			sendTelegramNotification("🔄 <b>Config changed</b> — restarting service...")
		}()
	})

	changes <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	log.Println("OpenClaw Gateway service is running.")

	// Send startup notification (non-blocking)
	go notifyStartup()

	// Monitor power events (sleep/resume)
	powerDone := make(chan struct{})
	go monitorPowerEvents(powerDone)

	for {
		select {
		case c := <-r:
			switch c.Cmd {
			case svc.Stop, svc.Shutdown:
				log.Println("OpenClaw Gateway service stopping...")
				notifyShutdown()
				close(powerDone)
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				wg.Wait()
				log.Println("OpenClaw Gateway service stopped.")
				return false, 0
			case svc.Interrogate:
				changes <- c.CurrentStatus
			}
		}
	}
}

func runService() {
	err := svc.Run(serviceName, &gatewayService{})
	if err != nil {
		log.Fatalf("Service failed: %v", err)
	}
}

func findNodeExe() string {
	candidates := []string{
		`C:\Program Files\nodejs\node.exe`,
		userHome + `\AppData\Roaming\nvm\v22.19.0\node.exe`,
		userHome + `\AppData\Roaming\fnm\node-versions\v22.19.0\installation\node.exe`,
	}

	if p, err := exec.LookPath("node.exe"); err == nil {
		candidates = append([]string{p}, candidates...)
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

func findEntryJS() string {
	candidates := []string{
		`C:\Program Files\nodejs\node_modules\openclaw\dist\entry.js`,
		userHome + `\AppData\Roaming\nvm\v22.19.0\node_modules\openclaw\dist\entry.js`,
		userHome + `\AppData\Roaming\fnm\node-versions\v22.19.0\installation\node_modules\openclaw\dist\entry.js`,
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

// gatewayCmd holds the current gateway process for cleanup
var (
	gatewayCmd   *exec.Cmd
	gatewayCmdMu sync.Mutex
)

// killGatewayProcess forcefully kills the gateway process and all its children
func killGatewayProcess() {
	gatewayCmdMu.Lock()
	cmd := gatewayCmd
	gatewayCmdMu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return
	}

	pid := cmd.Process.Pid
	log.Printf("Killing gateway process tree (PID %d)...", pid)

	// taskkill /T kills the entire process tree
	killCmd := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	killCmd.Run()

	// Wait a moment for port release
	time.Sleep(1 * time.Second)
}

func runGateway(ctx context.Context) {
	nodeExe := findNodeExe()
	entryJS := findEntryJS()
	logDir := filepath.Join(userHome, ".openclaw", "logs")
	consecutiveFails := 0

	log.Printf("[Gateway] node=%s", nodeExe)
	log.Printf("[Gateway] entry=%s", entryJS)
	log.Printf("[Gateway] workdir=%s", filepath.Join(userHome, ".openclaw", "workspace"))

	// Kill gateway on context cancellation (service shutdown)
	go func() {
		<-ctx.Done()
		killGatewayProcess()
	}()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Quick lock file cleanup (fast, no network)
		cleanupLockFiles()

		log.Printf("Starting gateway: %s %s gateway", nodeExe, entryJS)

		cmd := exec.Command(nodeExe, entryJS, "gateway")
		cmd.Dir = filepath.Join(userHome, ".openclaw", "workspace")

		cmd.Env = append(os.Environ(),
			"USERPROFILE="+userHome,
			"HOME="+userHome,
			"APPDATA="+userHome+`\AppData\Roaming`,
			"LOCALAPPDATA="+userHome+`\AppData\Local`,
			"TEMP="+userHome+`\AppData\Local\Temp`,
			"TMP="+userHome+`\AppData\Local\Temp`,
			"OPENCLAW_SERVICE=1",
		)

		// Append stdout; capture stderr in memory to log on failure
		stdout, _ := os.OpenFile(filepath.Join(logDir, "gateway-stdout.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		var stderrBuf bytes.Buffer
		cmd.Stdout = stdout
		cmd.Stderr = &stderrBuf

		// Store reference for cleanup
		gatewayCmdMu.Lock()
		gatewayCmd = cmd
		gatewayCmdMu.Unlock()

		startTime := time.Now()
		err := cmd.Run()

		// Clear reference
		gatewayCmdMu.Lock()
		gatewayCmd = nil
		gatewayCmdMu.Unlock()

		if stdout != nil {
			stdout.Close()
		}

		// Write stderr to file (append with timestamp) AND log it
		stderrStr := strings.TrimSpace(stderrBuf.String())
		if stderrStr != "" {
			log.Printf("[Gateway STDERR] %s", stderrStr)
			if sf, err2 := os.OpenFile(filepath.Join(logDir, "gateway-stderr.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644); err2 == nil {
				fmt.Fprintf(sf, "\n=== %s ===\n%s\n", time.Now().Format("2006-01-02 15:04:05"), stderrStr)
				sf.Close()
			}
		}

		if ctx.Err() != nil {
			return
		}

		runDuration := time.Since(startTime)

		// If it ran for more than 30 seconds, it was a real run (not an immediate crash)
		if runDuration > 30*time.Second {
			consecutiveFails = 0
		} else {
			consecutiveFails++
		}

		// Check for lock conflict (another gateway already has the port)
		if isLockConflictStr(stderrStr) {
			log.Println("Gateway lock conflict detected. Cleaning up orphans and retrying...")
			cleanupOrphanedGateway(ctx)
			// Remove lock files
			cleanupLockFiles()
			// Wait a bit then retry (but not forever)
			if consecutiveFails > 3 {
				log.Println("Too many consecutive lock conflicts. Waiting 60s...")
				go sendTelegramNotification("⚠️ <b>Gateway lock conflict persists</b> — waiting 60s before retry.")
				select {
				case <-ctx.Done():
					return
				case <-time.After(60 * time.Second):
				}
				consecutiveFails = 0
			} else {
				select {
				case <-ctx.Done():
					return
				case <-time.After(5 * time.Second):
				}
			}
			continue
		}

		// Backoff: if crashing repeatedly, slow down
		delay := 5 * time.Second
		if consecutiveFails > 5 {
			delay = 30 * time.Second
		} else if consecutiveFails > 2 {
			delay = 10 * time.Second
		}

		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		log.Printf("Gateway exited: %v (exit=%d, ran %v, fails=%d, node=%s, entry=%s). Restarting in %v...", err, exitCode, runDuration.Round(time.Second), consecutiveFails, nodeExe, entryJS, delay)
		if consecutiveFails <= 1 {
			go sendTelegramNotification(fmt.Sprintf("⚠️ <b>Gateway crashed, restarting...</b>\n\nError: %v", err))
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

// cleanupOrphanedGateway kills any node processes listening on the gateway port (18789)
func cleanupOrphanedGateway(ctx context.Context) {
	// Use netstat to find PID on port 18789
	out, err := exec.CommandContext(ctx, "cmd", "/c", "netstat -ano | findstr :18789 | findstr LISTENING").Output()
	if err != nil || len(out) == 0 {
		return
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	for _, line := range lines {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 5 {
			continue
		}
		pid := fields[len(fields)-1]
		if pid == "0" {
			continue
		}
		log.Printf("Killing orphaned gateway process on port 18789 (PID %s)", pid)
		_ = exec.CommandContext(ctx, "taskkill", "/F", "/PID", pid).Run()
	}

	// Wait for port to be released
	time.Sleep(1 * time.Second)
}

// cleanupLockFiles removes stale gateway lock files from all possible locations
func cleanupLockFiles() {
	lockDirs := []string{
		filepath.Join(userHome, "AppData", "Local", "Temp", "openclaw-locks"),
		filepath.Join(os.TempDir(), "openclaw-locks"),
		filepath.Join(userHome, ".openclaw"),
		`C:\Windows\Temp\openclaw-locks`,
	}
	for _, lockDir := range lockDirs {
		entries, err := os.ReadDir(lockDir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if strings.HasPrefix(e.Name(), "gateway") && strings.HasSuffix(e.Name(), ".lock") {
				p := filepath.Join(lockDir, e.Name())
				log.Printf("Removing lock file: %s", p)
				os.Remove(p)
			}
		}
	}
}

func isLockConflict(stderrPath string) bool {
	data, err := os.ReadFile(stderrPath)
	if err != nil {
		return false
	}
	content := string(data)
	if len(content) > 1024 {
		content = content[len(content)-1024:]
	}
	return strings.Contains(content, "gateway already running") || strings.Contains(content, "lock timeout")
}

func isLockConflictStr(stderr string) bool {
	return strings.Contains(stderr, "gateway already running") || strings.Contains(stderr, "lock timeout") || strings.Contains(stderr, "already in use")
}

func runDaemon() {
	// Setup logging
	logDir := filepath.Join(userHome, ".openclaw", "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "service.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
	} else {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	log.Println("OpenClaw Gateway daemon starting...")

	// Load configuration
	CreateDefaultConfig()
	LoadConfig()
	cfg := GetConfig()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Start gateway process (always enabled)
	wg.Add(1)
	go func() {
		defer wg.Done()
		runGateway(ctx)
	}()

	// Start worktime monitor (if enabled)
	if cfg.WorktimeEnabled {
		log.Println("[Daemon] Worktime monitor: enabled")
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartWorkTimeMonitor(ctx)
		}()

		wg.Add(1)
		go func() {
			defer wg.Done()
			StartIdlePipeServer(ctx)
		}()
	} else {
		log.Println("[Daemon] Worktime monitor: disabled")
	}

	// Start ClawBridge (if enabled)
	if cfg.BridgeEnabled && cfg.BridgeServer != "" {
		log.Printf("[Daemon] ClawBridge: enabled (server=%s)", cfg.BridgeServer)
		wg.Add(1)
		go func() {
			defer wg.Done()
			StartBridge(ctx)
		}()
	} else {
		log.Println("[Daemon] ClawBridge: disabled")
	}

	// Watch config for hot-reload
	WatchConfig(func() {
		log.Println("[Daemon] Config changed, restarting...")
		cancel()
	})

	log.Println("OpenClaw Gateway daemon is running.")
	go notifyStartup()

	// Monitor power events
	powerDone := make(chan struct{})
	go monitorPowerEvents(powerDone)

	// Wait for interrupt signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Println("OpenClaw Gateway daemon stopping...")
	notifyShutdown()
	close(powerDone)
	cancel()
	wg.Wait()
	log.Println("OpenClaw Gateway daemon stopped.")
}

func runGatewayForeground() {
	nodeExe := findNodeExe()
	entryJS := findEntryJS()

	fmt.Printf("Running: %s %s gateway\n", nodeExe, entryJS)

	cmd := exec.Command(nodeExe, entryJS, "gateway")
	cmd.Dir = filepath.Join(userHome, ".openclaw", "workspace")
	cmd.Env = append(os.Environ())
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		log.Fatalf("Gateway exited: %v", err)
	}
}
