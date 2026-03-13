package main

import (
	"fmt"
	"os"
	"os/exec"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

func updateOpenclaw() error {
	fmt.Println("=== OpenClaw Update ===")

	// 1. 서비스 중단
	fmt.Println("[1/3] Stopping service...")
	if err := stopServiceForUpdate(); err != nil {
		return fmt.Errorf("stop: %v", err)
	}

	// 서비스가 완전히 멈출 때까지 대기 (최대 15초)
	if err := waitForServiceState(svc.Stopped, 15*time.Second); err != nil {
		fmt.Printf("Warning: %v (continuing anyway)\n", err)
	} else {
		fmt.Println("Service stopped.")
	}

	// 2. npm i -g openclaw@latest
	fmt.Println("[2/3] Running: npm i -g openclaw@latest")

	npmPath := findNpm()
	if npmPath == "" {
		// 서비스 재시작 후 에러 반환
		fmt.Println("npm not found, restarting service...")
		_ = startService()
		return fmt.Errorf("npm not found in PATH or common locations")
	}
	fmt.Printf("  npm: %s\n", npmPath)

	cmd := exec.Command(npmPath, "i", "-g", "openclaw@latest")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("npm install failed: %v\n", err)
		fmt.Println("Restarting service anyway...")
		_ = startService()
		return fmt.Errorf("npm install: %v", err)
	}

	// 3. 서비스 재시작
	fmt.Println("[3/3] Starting service...")
	if err := startService(); err != nil {
		return fmt.Errorf("start: %v", err)
	}

	fmt.Println("Done! OpenClaw updated and service restarted.")
	return nil
}

// findNpm: PATH 또는 NVM 기본 경로에서 npm.cmd 탐색
func findNpm() string {
	// 1. PATH에서 찾기
	if p, err := exec.LookPath("npm"); err == nil {
		return p
	}
	if p, err := exec.LookPath("npm.cmd"); err == nil {
		return p
	}

	// 2. NVM / 기본 Node 경로 후보
	home := os.Getenv("USERPROFILE")
	if home == "" {
		home = os.Getenv("HOME")
	}
	candidates := []string{
		home + `\AppData\Roaming\nvm\v22.19.0\npm.cmd`,
		home + `\AppData\Roaming\nvm\v22.11.0\npm.cmd`,
		home + `\AppData\Roaming\nvm\v20.19.0\npm.cmd`,
		`C:\Program Files\nodejs\npm.cmd`,
		`C:\Program Files (x86)\nodejs\npm.cmd`,
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// stopServiceForUpdate: 서비스 중단 신호만 보냄 (stopService 와 동일하지만 에러 무시 포함)
func stopServiceForUpdate() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to SCM: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("open service: %v", err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("query: %v", err)
	}
	if status.State == svc.Stopped {
		fmt.Println("Service is already stopped.")
		return nil
	}

	_, err = s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("send stop: %v", err)
	}
	return nil
}

// waitForServiceState: 지정 상태가 될 때까지 폴링
func waitForServiceState(target svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		m, err := mgr.Connect()
		if err != nil {
			time.Sleep(500 * time.Millisecond)
			continue
		}
		s, err := m.OpenService(serviceName)
		if err != nil {
			m.Disconnect()
			time.Sleep(500 * time.Millisecond)
			continue
		}
		status, err := s.Query()
		s.Close()
		m.Disconnect()

		if err == nil && status.State == target {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for service state %d", target)
}
