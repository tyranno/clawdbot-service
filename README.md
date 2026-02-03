# clawdbot-service

Windows Service wrapper for Clawdbot Gateway.

## Features
- 🖥 Runs as a Windows Service (no console window)
- 🚀 Auto-start on boot (no login required)
- 🔄 Auto-restart on crash (5s/10s/30s delays)
- 💤 Detects sleep/resume events
- 📱 Telegram notifications for:
  - Service start/stop
  - Gateway crash & restart
  - Resume from sleep

## Install

```powershell
# Run as Administrator
clawdbot-service.exe install
clawdbot-service.exe start
```

## Commands

```
clawdbot-service install    # Install Windows service
clawdbot-service uninstall  # Remove Windows service
clawdbot-service start      # Start service
clawdbot-service stop       # Stop service
clawdbot-service status     # Check service status
clawdbot-service run        # Run gateway in foreground (testing)
```

## Configuration

Create `C:\Users\<user>\.clawdbot\service-config.txt`:
```
TELEGRAM_BOT_TOKEN=your_bot_token_here
```

## Build

```powershell
go build -o clawdbot-service.exe .
```
