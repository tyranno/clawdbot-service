package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"golang.org/x/sys/windows/svc"
)

const userHome = `C:\Users\tyranno`

type gatewayService struct{}

func (s *gatewayService) Execute(args []string, r <-chan svc.ChangeRequest, changes chan<- svc.Status) (bool, uint32) {
	changes <- svc.Status{State: svc.StartPending}

	// Setup logging
	logDir := filepath.Join(userHome, ".clawdbot", "logs")
	os.MkdirAll(logDir, 0755)
	logFile, err := os.OpenFile(filepath.Join(logDir, "service.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("Failed to open log file: %v", err)
	} else {
		log.SetOutput(logFile)
		defer logFile.Close()
	}

	log.Println("Clawdbot Gateway service starting...")

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
	log.Println("Clawdbot Gateway service is running.")

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
				log.Println("Clawdbot Gateway service stopping...")
				notifyShutdown()
				close(powerDone)
				changes <- svc.Status{State: svc.StopPending}
				cancel()
				wg.Wait()
				log.Println("Clawdbot Gateway service stopped.")
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
		`C:\Program Files\nodejs\node_modules\clawdbot\dist\entry.js`,
		userHome + `\AppData\Roaming\nvm\v22.19.0\node_modules\clawdbot\dist\entry.js`,
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

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		log.Printf("Starting gateway: %s %s gateway --port 18789", nodeExe, entryJS)

		cmd := exec.CommandContext(ctx, nodeExe, entryJS, "gateway", "--port", "18789")
		cmd.Dir = filepath.Join(userHome, "clawd")

		// Set environment
		cmd.Env = append(os.Environ(),
			"USERPROFILE="+userHome,
			"HOME="+userHome,
			"APPDATA="+userHome+`\AppData\Roaming`,
			"LOCALAPPDATA="+userHome+`\AppData\Local`,
			"CLAWDBOT_GATEWAY_PORT=18789",
			"CLAWDBOT_GATEWAY_TOKEN=40b95b7b7ffc371428d248420e58fa486e03a2f33f4e8a32",
			"CLAWDBOT_SERVICE_MARKER=clawdbot",
			"CLAWDBOT_SERVICE_KIND=gateway",
		)

		// Redirect output to log
		logDir := filepath.Join(userHome, ".clawdbot", "logs")
		stdout, _ := os.OpenFile(filepath.Join(logDir, "gateway-stdout.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		stderr, _ := os.OpenFile(filepath.Join(logDir, "gateway-stderr.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		cmd.Stdout = stdout
		cmd.Stderr = stderr

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

		log.Printf("Gateway exited: %v. Restarting in 5s...", err)
		go sendTelegramNotification(fmt.Sprintf("⚠️ <b>Gateway crashed, restarting...</b>\n\nError: %v", err))
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
}

func runGatewayForeground() {
	nodeExe := findNodeExe()
	entryJS := findEntryJS()

	fmt.Printf("Running: %s %s gateway --port 18789\n", nodeExe, entryJS)

	cmd := exec.Command(nodeExe, entryJS, "gateway", "--port", "18789")
	cmd.Dir = filepath.Join(userHome, "clawd")
	cmd.Env = append(os.Environ(),
		"CLAWDBOT_GATEWAY_PORT=18789",
		"CLAWDBOT_GATEWAY_TOKEN=40b95b7b7ffc371428d248420e58fa486e03a2f33f4e8a32",
		"CLAWDBOT_SERVICE_MARKER=clawdbot",
		"CLAWDBOT_SERVICE_KIND=gateway",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	err := cmd.Run()
	if err != nil {
		log.Fatalf("Gateway exited: %v", err)
	}
}
