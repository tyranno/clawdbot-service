package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const serviceName = "ClawdbotGateway"
const serviceDisplay = "Clawdbot Gateway"
const serviceDesc = "Clawdbot AI Gateway Service"

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: clawdbot-service <install|uninstall|start|stop|status|run>\n")
	os.Exit(1)
}

func main() {
	// Check if running as Windows service
	isService, err := svc.IsWindowsService()
	if err != nil {
		log.Fatalf("Failed to determine if running as service: %v", err)
	}
	if isService {
		runService()
		return
	}

	if len(os.Args) < 2 {
		usage()
	}

	cmd := strings.ToLower(os.Args[1])
	switch cmd {
	case "install":
		err = installService()
	case "uninstall":
		err = uninstallService()
	case "start":
		err = startService()
	case "stop":
		err = stopService()
	case "status":
		err = queryService()
	case "run":
		// Run gateway in foreground (for testing)
		runGatewayForeground()
		return
	default:
		usage()
	}

	if err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func installService() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not get executable path: %v", err)
	}

	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %v (run as administrator)", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err == nil {
		s.Close()
		return fmt.Errorf("service %s already exists", serviceName)
	}

	s, err = m.CreateService(serviceName, exePath, mgr.Config{
		DisplayName: serviceDisplay,
		Description: serviceDesc,
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("could not create service: %v", err)
	}
	defer s.Close()

	// Set recovery actions: restart on failure
	err = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 5000},  // 5s delay
		{Type: mgr.ServiceRestart, Delay: 10000}, // 10s delay
		{Type: mgr.ServiceRestart, Delay: 30000}, // 30s delay
	}, 86400) // reset after 24h
	if err != nil {
		log.Printf("Warning: could not set recovery actions: %v", err)
	}

	fmt.Printf("Service '%s' installed successfully.\n", serviceDisplay)
	fmt.Println("Start with: clawdbot-service start")
	return nil
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %v (run as administrator)", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %v", serviceName, err)
	}
	defer s.Close()

	err = s.Delete()
	if err != nil {
		return fmt.Errorf("could not delete service: %v", err)
	}

	fmt.Printf("Service '%s' uninstalled successfully.\n", serviceDisplay)
	return nil
}

func startService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("could not open service: %v", err)
	}
	defer s.Close()

	err = s.Start()
	if err != nil {
		return fmt.Errorf("could not start service: %v", err)
	}

	fmt.Printf("Service '%s' started.\n", serviceDisplay)
	return nil
}

func stopService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("could not open service: %v", err)
	}
	defer s.Close()

	status, err := s.Control(svc.Stop)
	if err != nil {
		return fmt.Errorf("could not stop service: %v", err)
	}

	fmt.Printf("Service '%s' stopping (state: %d).\n", serviceDisplay, status.State)
	return nil
}

func queryService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("could not connect to service manager: %v", err)
	}
	defer m.Disconnect()

	s, err := m.OpenService(serviceName)
	if err != nil {
		return fmt.Errorf("service %s not found: %v", serviceName, err)
	}
	defer s.Close()

	status, err := s.Query()
	if err != nil {
		return fmt.Errorf("could not query service: %v", err)
	}

	stateStr := "Unknown"
	switch status.State {
	case svc.Stopped:
		stateStr = "Stopped"
	case svc.StartPending:
		stateStr = "Starting"
	case svc.StopPending:
		stateStr = "Stopping"
	case svc.Running:
		stateStr = "Running"
	}

	fmt.Printf("Service: %s\n", serviceDisplay)
	fmt.Printf("Status:  %s\n", stateStr)
	fmt.Printf("PID:     %d\n", status.ProcessId)
	return nil
}
