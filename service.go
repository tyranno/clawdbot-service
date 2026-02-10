package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
)

// getUserHome returns the user's home directory
// Priority: OPENCLAW_USER_HOME env > USERPROFILE env > os.UserHomeDir()
func getUserHome() string {
	// 1. Check custom env var (for service mode)
	if home := os.Getenv("OPENCLAW_USER_HOME"); home != "" {
		return home
	}
	// 2. Check USERPROFILE (Windows standard)
	if home := os.Getenv("USERPROFILE"); home != "" {
		return home
	}
	// 3. Fallback to os.UserHomeDir()
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	// 4. Last resort
	return `C:\Users\Default`
}

var userHome = getUserHome()

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

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Start gateway process
	wg.Add(1)
	go func() {
		defer wg.Done()
		runGateway(ctx)
	}()

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

func runGateway(ctx context.Context) {
	nodeExe := findNodeExe()
	entryJS := findEntryJS()
	logDir := filepath.Join(userHome, ".openclaw", "logs")
	consecutiveFails := 0

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Quick lock file cleanup (fast, no network)
		cleanupLockFiles()

		log.Printf("Starting gateway: %s %s gateway", nodeExe, entryJS)

		cmd := exec.CommandContext(ctx, nodeExe, entryJS, "gateway")
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

		// Truncate stderr each start to avoid stale lock-conflict detection
		stdout, _ := os.OpenFile(filepath.Join(logDir, "gateway-stdout.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		stderr, _ := os.OpenFile(filepath.Join(logDir, "gateway-stderr.log"), os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
		cmd.Stdout = stdout
		cmd.Stderr = stderr

		startTime := time.Now()
		err := cmd.Run()

		if stdout != nil {
			stdout.Close()
		}
		if stderr != nil {
			stderr.Close()
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
		stderrPath := filepath.Join(logDir, "gateway-stderr.log")
		if isLockConflict(stderrPath) {
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

		log.Printf("Gateway exited: %v (ran %v, fails=%d). Restarting in %v...", err, runDuration.Round(time.Second), consecutiveFails, delay)
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
	// Check last 1KB for lock conflict message
	content := string(data)
	if len(content) > 1024 {
		content = content[len(content)-1024:]
	}
	return strings.Contains(content, "gateway already running") || strings.Contains(content, "lock timeout")
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
