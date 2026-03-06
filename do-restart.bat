@echo off
sc stop OpenClawGateway
timeout /t 5 /nobreak
sc start OpenClawGateway
