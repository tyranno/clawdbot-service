@echo off
setlocal
set SERVICE=OpenClawGateway
set DIR=%~dp0
set GO=C:\Users\lab\scoop\apps\go\current\bin\go.exe

echo [build] Building...
cd /d "%DIR%"
%GO% build -o clawdbot-service.exe .
if errorlevel 1 ( echo BUILD FAILED & pause & exit /b 1 )
echo [build] OK.

echo [deploy] Disabling auto-restart temporarily...
sc failureflag %SERVICE% 0 >nul 2>&1

echo [deploy] Stopping service...
sc stop %SERVICE% >nul 2>&1

:wait_stopped
timeout /t 2 /nobreak >nul
sc query %SERVICE% | findstr /i "STOPPED" >nul
if errorlevel 1 goto wait_stopped

echo [deploy] Starting new version...
sc start %SERVICE% >nul 2>&1

:wait_running
timeout /t 2 /nobreak >nul
sc query %SERVICE% | findstr /i "RUNNING" >nul
if errorlevel 1 goto wait_running

echo [deploy] Re-enabling auto-restart...
sc failureflag %SERVICE% 1 >nul 2>&1

echo [deploy] Done! Service is running.
endlocal
