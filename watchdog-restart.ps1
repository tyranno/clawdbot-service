$scriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$triggerFile = Join-Path $scriptDir "restart.trigger"
$logFile = Join-Path $scriptDir "watchdog-restart.log"
$maintenanceFlag = Join-Path $scriptDir "maintenance.flag"
$serviceName = "OpenClawGateway"

function Write-Log {
    param($msg)
    $line = "$(Get-Date -Format 'yyyy-MM-dd HH:mm:ss') $msg"
    Add-Content -Path $logFile -Value $line -Encoding UTF8
}

function Restart-OCG {
    param($reason)
    Write-Log "[$reason] Restarting $serviceName..."
    Stop-Service $serviceName -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 3
    Start-Service $serviceName -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 2
    $status = (Get-Service $serviceName -ErrorAction SilentlyContinue).Status
    Write-Log "[$reason] Done. Status: $status"
}

Write-Log "Watchdog v2 started. Health check every 30s + trigger file."

$checkCount = 0

while ($true) {
    if (Test-Path $triggerFile) {
        Remove-Item $triggerFile -Force
        if (Test-Path $maintenanceFlag) { Remove-Item $maintenanceFlag -Force }
        Restart-OCG "Trigger"
    }

    $checkCount++
    if ($checkCount -ge 15) {
        $checkCount = 0
        $svc = Get-Service $serviceName -ErrorAction SilentlyContinue
        if ($svc -and $svc.Status -ne "Running") {
            if (Test-Path $maintenanceFlag) {
                Write-Log "[Health] Service stopped (maintenance mode). Skipping restart."
            } else {
                Write-Log "[Health] Service is '$($svc.Status)'. Restarting..."
                Start-Service $serviceName -ErrorAction SilentlyContinue
                Start-Sleep -Seconds 2
                $st = (Get-Service $serviceName -ErrorAction SilentlyContinue).Status
                Write-Log "[Health] Done. Status: $st"
            }
        }
    }

    Start-Sleep -Seconds 2
}
