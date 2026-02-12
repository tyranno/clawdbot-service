package main

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// Config holds all service configuration
type Config struct {
	// Feature toggles
	WorktimeEnabled bool
	BridgeEnabled   bool

	// Bridge settings
	BridgeServer string
	BridgeToken  string
	BridgeName   string

	// OpenClaw settings
	OpenclawURL   string
	OpenclawToken string
}

var (
	config     Config
	configMu   sync.RWMutex
	configPath string
	onReload   func() // callback when config changes
)

// GetConfig returns current config (thread-safe)
func GetConfig() Config {
	configMu.RLock()
	defer configMu.RUnlock()
	return config
}

// getUserHome returns the user's home directory
// Priority: OPENCLAW_USER_HOME env > USERPROFILE env > os.UserHomeDir()
func getUserHome() string {
	if home := os.Getenv("OPENCLAW_USER_HOME"); home != "" {
		return home
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		return home
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return `C:\Users\Default`
}

var userHome = getUserHome()

// LoadConfig reads configuration from service-config.txt
func LoadConfig() {
	configPath = filepath.Join(userHome, ".openclaw", "service-config.txt")

	configMu.Lock()
	defer configMu.Unlock()

	// Defaults
	config = Config{
		WorktimeEnabled: false, // disabled by default
		BridgeEnabled:   false, // disabled by default
		OpenclawURL:     "http://localhost:18789",
		BridgeName:      getHostname(),
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Printf("[Config] No config file at %s, using defaults", configPath)
		return
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		value := strings.TrimSpace(parts[1])

		switch key {
		case "WORKTIME_ENABLED":
			config.WorktimeEnabled = parseBool(value)
		case "BRIDGE_ENABLED":
			config.BridgeEnabled = parseBool(value)
		case "BRIDGE_SERVER":
			config.BridgeServer = value
			if value != "" {
				config.BridgeEnabled = true // auto-enable if server is set
			}
		case "BRIDGE_TOKEN":
			config.BridgeToken = value
		case "BRIDGE_NAME":
			config.BridgeName = value
		case "OPENCLAW_URL":
			config.OpenclawURL = value
		case "OPENCLAW_TOKEN":
			config.OpenclawToken = value
		}
	}

	log.Printf("[Config] Loaded: worktime=%v, bridge=%v, server=%s, name=%s",
		config.WorktimeEnabled, config.BridgeEnabled, config.BridgeServer, config.BridgeName)
}

func parseBool(s string) bool {
	s = strings.ToLower(s)
	return s == "true" || s == "1" || s == "yes" || s == "on"
}

func getHostname() string {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "OpenClaw"
	}
	return hostname
}

// WatchConfig monitors config file for changes and triggers reload
func WatchConfig(reloadCallback func()) {
	onReload = reloadCallback

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[Config] Failed to create watcher: %v", err)
		return
	}

	// Watch the directory (file watching can miss some changes)
	configDir := filepath.Dir(configPath)
	err = watcher.Add(configDir)
	if err != nil {
		log.Printf("[Config] Failed to watch %s: %v", configDir, err)
		return
	}

	go func() {
		log.Printf("[Config] Watching for changes: %s", configPath)
		for {
			select {
			case event, ok := <-watcher.Events:
				if !ok {
					return
				}
				// Check if it's our config file
				if filepath.Base(event.Name) == "service-config.txt" {
					if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
						log.Println("[Config] Config file changed, reloading...")
						oldConfig := GetConfig()
						LoadConfig()
						newConfig := GetConfig()

						// Check if restart is needed (feature toggles changed)
						if configChanged(oldConfig, newConfig) {
							log.Println("[Config] Feature settings changed, triggering restart...")
							if onReload != nil {
								onReload()
							}
						}
					}
				}
			case err, ok := <-watcher.Errors:
				if !ok {
					return
				}
				log.Printf("[Config] Watch error: %v", err)
			}
		}
	}()
}

func configChanged(old, new Config) bool {
	return old.WorktimeEnabled != new.WorktimeEnabled ||
		old.BridgeEnabled != new.BridgeEnabled ||
		old.BridgeServer != new.BridgeServer
}

// CreateDefaultConfig creates a default config file if it doesn't exist
func CreateDefaultConfig() {
	configPath := filepath.Join(userHome, ".openclaw", "service-config.txt")
	if _, err := os.Stat(configPath); err == nil {
		return // already exists
	}

	defaultConfig := `# OpenClaw Service Configuration
# Edit this file to enable/disable features
# Changes are auto-detected and service will restart

# === Feature Toggles ===
WORKTIME_ENABLED=false
BRIDGE_ENABLED=false

# === Bridge Settings (GCP Relay) ===
# BRIDGE_SERVER=your-server.com:9090
# BRIDGE_TOKEN=your-secret-token
# BRIDGE_NAME=My PC

# === OpenClaw Gateway ===
OPENCLAW_URL=http://localhost:18789
# OPENCLAW_TOKEN=
`
	os.MkdirAll(filepath.Dir(configPath), 0755)
	os.WriteFile(configPath, []byte(defaultConfig), 0644)
	log.Printf("[Config] Created default config: %s", configPath)
}

// GetBool helper for backward compatibility
func GetConfigBool(key string) bool {
	cfg := GetConfig()
	switch key {
	case "WORKTIME_ENABLED":
		return cfg.WorktimeEnabled
	case "BRIDGE_ENABLED":
		return cfg.BridgeEnabled
	}
	return false
}

func GetConfigString(key string) string {
	cfg := GetConfig()
	switch key {
	case "BRIDGE_SERVER":
		return cfg.BridgeServer
	case "BRIDGE_TOKEN":
		return cfg.BridgeToken
	case "BRIDGE_NAME":
		return cfg.BridgeName
	case "OPENCLAW_URL":
		return cfg.OpenclawURL
	case "OPENCLAW_TOKEN":
		return cfg.OpenclawToken
	}
	return ""
}

// FormatBool for logging
func FormatBool(b bool) string {
	if b {
		return "enabled"
	}
	return "disabled"
}

// Unused import prevention
var _ = strconv.Atoi
