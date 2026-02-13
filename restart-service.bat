@echo off
echo Restarting OpenClawGateway service...
net stop OpenClawGateway
timeout /t 3 /nobreak >nul
net start OpenClawGateway
echo Done!
pause
