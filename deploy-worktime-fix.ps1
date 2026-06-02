# deploy-worktime-fix.ps1
# worktime fail-safe(#3) + idle-detector 자가복구 예약작업(#2) 배포
# ★ 반드시 관리자 권한 PowerShell 에서 실행 ★ (서비스 제어 + 예약작업 수정에 admin 필요)
#
# 안전장치:
#  - maintenance.flag 로 워치독 자동 재시작 차단 후 교체
#  - 기존 exe 는 *.bak.exe 로 백업 (롤백용)
#  - 새 exe(*.new.exe) 가 없으면 중단

$ErrorActionPreference = 'Stop'
$root    = 'C:\Project\88.MyProject\clawdbot-service'
$svc     = 'OpenClawGateway'
$flag    = Join-Path $root 'maintenance.flag'
$svcExe  = Join-Path $root 'clawdbot-service.exe'
$idlExe  = Join-Path $root 'tool\idle-detector\idle-detector.exe'
$startupDup = 'C:\Users\lab\AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\idle-detector.exe'
$log     = Join-Path $root 'deploy-worktime-fix.log'

function Log($m) { $l = "$(Get-Date -Format 'HH:mm:ss') $m"; Write-Host $l; Add-Content -Path $log -Value $l -Encoding UTF8 }

# admin 체크
if (-not ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
  Write-Host "ERROR: 관리자 권한으로 실행하세요 (Run as Administrator)"; exit 1
}

Log "=== deploy 시작 ==="

# 0) 새 빌드 존재 확인
if (-not (Test-Path "$svcExe.new.exe")) { Log "ERROR: $svcExe.new.exe 없음 (build.cmd 먼저 실행)"; exit 1 }
if (-not (Test-Path "$idlExe.new.exe")) { Log "ERROR: $idlExe.new.exe 없음"; exit 1 }

# 1) 워치독 자동 재시작 차단
New-Item -ItemType File -Path $flag -Force | Out-Null
Log "1. maintenance.flag 생성 (워치독 차단)"

# 2) 서비스 중지
Log "2. 서비스 중지: $svc"
Stop-Service -Name $svc -Force
(Get-Service $svc).WaitForStatus('Stopped','00:00:30')
Log "   stopped"

# 3) exe 교체 (백업 후)
Log "3. exe 교체"
Copy-Item $svcExe "$svcExe.bak.exe" -Force
Move-Item "$svcExe.new.exe" $svcExe -Force
Get-Process idle-detector -ErrorAction SilentlyContinue | Stop-Process -Force
Start-Sleep -Milliseconds 500
Copy-Item $idlExe "$idlExe.bak.exe" -Force
Move-Item "$idlExe.new.exe" $idlExe -Force
Log "   clawdbot-service.exe / idle-detector.exe 교체 완료 (.bak.exe 백업)"

# 3-1) 시작프로그램 중복 idle-detector 제거 (이제 예약작업이 단독 소유)
if (Test-Path $startupDup) { Remove-Item $startupDup -Force; Log "   시작프로그램 중복 idle-detector.exe 제거" }

# 4) IdleDetector 예약작업 자가복구 재설정 (로그온 + 5분마다 무기한 반복, 배터리 중단 해제)
Log "4. IdleDetector 예약작업 재설정"
$xml = Export-ScheduledTask -TaskName 'IdleDetector'
$xml = $xml.Replace('<DisallowStartIfOnBatteries>true</DisallowStartIfOnBatteries>','<DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>')
$xml = $xml.Replace('<StopIfGoingOnBatteries>true</StopIfGoingOnBatteries>','<StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>')
$newTriggers = '<LogonTrigger><Enabled>true</Enabled></LogonTrigger><TimeTrigger><StartBoundary>2026-06-01T00:00:00</StartBoundary><Enabled>true</Enabled><Repetition><Interval>PT5M</Interval><StopAtDurationEnd>false</StopAtDurationEnd></Repetition></TimeTrigger>'
$xml = $xml.Replace('<LogonTrigger />', $newTriggers)
Register-ScheduledTask -TaskName 'IdleDetector' -Xml $xml -Force | Out-Null
Log "   재설정 완료 (5분 반복)"

# 5) 서비스 시작 (새 exe 부팅 시 maintenance.flag 자동 제거)
Log "5. 서비스 시작"
Start-Service -Name $svc
(Get-Service $svc).WaitForStatus('Running','00:00:30')
Log "   running"

# 6) idle-detector 즉시 기동 + 검증
Log "6. idle-detector 기동 + 검증"
Start-ScheduledTask -TaskName 'IdleDetector'
Start-Sleep -Seconds 6
$idl = Get-Process idle-detector -ErrorAction SilentlyContinue
if ($idl) { Log "   idle-detector 실행중 PID=$($idl.Id) (session 1 확인은 아래 로그)" } else { Log "   ⚠ idle-detector 미실행 — 예약작업 상태 확인 필요" }

# maintenance.flag 잔존 시 제거 (서비스가 지웠어야 정상)
if (Test-Path $flag) { Remove-Item $flag -Force; Log "   (maintenance.flag 수동 제거)" }

Log "=== deploy 완료 ==="
Log "검증: Get-Content '$($env:USERPROFILE)\.openclaw\logs\service.log' -Tail 15  →  'Helper connected' 확인"
