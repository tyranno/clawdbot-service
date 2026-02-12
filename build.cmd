@echo off
setlocal

:: Use go from PATH, or override with GO env var
if "%GO%"=="" set GO=go

echo Building clawdbot-service.exe...
%GO% build -buildvcs=false -o clawdbot-service.exe .
if errorlevel 1 goto :error

echo Building idle-detector.exe...
%GO% build -buildvcs=false -o tool\idle-detector\idle-detector.exe .\tool\idle-detector
if errorlevel 1 goto :error

echo.
echo === Build Complete ===
echo   clawdbot-service.exe
echo   tool\idle-detector\idle-detector.exe
goto :end

:error
echo Build failed!
exit /b 1

:end
