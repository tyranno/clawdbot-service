@echo off
setlocal
set SERVICE=OpenClawGateway
set EXE="%~dp0clawdbot-service.exe"

echo [restart] Stopping %SERVICE%...
sc stop %SERVICE% >nul 2>&1

:wait_stopped
timeout /t 2 /nobreak >nul
sc query %SERVICE% | findstr /i "STOPPED" >nul
if errorlevel 1 (
    echo [restart] Still stopping, waiting...
    goto wait_stopped
)

echo [restart] Stopped. Starting %SERVICE%...
sc start %SERVICE% >nul 2>&1

:wait_running
timeout /t 2 /nobreak >nul
sc query %SERVICE% | findstr /i "RUNNING" >nul
if errorlevel 1 (
    echo [restart] Still starting, waiting...
    goto wait_running
)

echo [restart] %SERVICE% is running.
endlocal
