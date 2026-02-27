@echo off
echo Stopping OpenClaw Gateway daemon gracefully...
"%~dp0clawdbot-service.exe" stop-daemon
timeout /t 5 /nobreak >nul
echo Done!
