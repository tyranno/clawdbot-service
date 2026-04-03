# Design: 서비스 설치/관리 개선

> Plan: [service-installer.plan.md](../../01-plan/features/service-installer.plan.md)

## 1. 워치독 수동 중지 개선

### 1.1 플래그 파일 설계

| 항목 | 값 |
|------|-----|
| 파일명 | `maintenance.flag` |
| 경로 | exe와 같은 디렉토리 (`exeDir`) |
| 내용 | 타임스탬프 (디버깅용) |
| 생성 시점 | `svc.Stop` 또는 `svc.Shutdown` 수신 시 |
| 삭제 시점 | 서비스 `Execute()` 시작 시 |

### 1.2 service.go 변경

**Execute() 시작부에 추가**:
```go
// 서비스 시작 시 maintenance 플래그 제거
flagPath := filepath.Join(exeDir(), "maintenance.flag")
os.Remove(flagPath)
```

**Stop/Shutdown 핸들러에 추가** (line 119~143 사이, `cancel()` 호출 전):
```go
case svc.Stop, svc.Shutdown:
    // 정상 종료 플래그 생성 → 워치독이 재시작하지 않음
    flagPath := filepath.Join(exeDir(), "maintenance.flag")
    os.WriteFile(flagPath, []byte(time.Now().Format(time.RFC3339)), 0644)
    log.Printf("[Service] Created maintenance.flag (graceful %s)", cmdName)
    
    // ... 기존 종료 로직 ...
```

**exeDir 헬퍼 함수**:
```go
func exeDir() string {
    exe, _ := os.Executable()
    return filepath.Dir(exe)
}
```

### 1.3 watchdog-restart.ps1 변경

헬스체크 블록에 플래그 확인 추가:

```powershell
$exeDir = "C:\Program Files\OpenClaw"  # 또는 현재 스크립트 경로
$maintenanceFlag = Join-Path $exeDir "maintenance.flag"

# 헬스체크 (기존 15카운트 블록 내부)
if ($svc -and $svc.Status -ne "Running") {
    # 유지보수 플래그가 있으면 수동 중지로 판단 → 스킵
    if (Test-Path $maintenanceFlag) {
        Write-Log "[Health] Service stopped (maintenance mode). Skipping restart."
    } else {
        Write-Log "[Health] Service is '$($svc.Status)'. Restarting..."
        Start-Service $serviceName -ErrorAction SilentlyContinue
        # ...
    }
}
```

### 1.4 build-and-restart.bat 변경

빌드 후 재시작 시 플래그 파일 삭제 추가:

```bat
echo [deploy] Removing maintenance flag...
del /q "%~dp0maintenance.flag" >nul 2>&1
```

## 2. BAT 파일 정리

### 2.1 삭제 대상

| 파일 | 이유 |
|------|------|
| `commit.bat` | 일회성 커밋, 하드코딩된 파일 목록 |
| `push.bat` | 일회성 커밋, 하드코딩된 파일 목록 |
| `push-all.bat` | 일회성 커밋, 하드코딩된 파일 목록 |
| `do-restart.bat` | `restart-service.bat`과 중복 |

### 2.2 통합: restart-service.bat

`restart-service.bat` 유지 (상세 대기 로직 포함), `do-restart.bat` 삭제.

기존 `restart-service.bat`에 **maintenance.flag 삭제** 추가:

```bat
@echo off
setlocal
set SERVICE=OpenClawGateway
set EXE="%~dp0clawdbot-service.exe"

echo [restart] Removing maintenance flag...
del /q "%~dp0maintenance.flag" >nul 2>&1

echo [restart] Stopping %SERVICE%...
sc stop %SERVICE% >nul 2>&1
:: ... (기존 대기 로직 유지)
```

### 2.3 최종 파일 구조

```
clawdbot-service/
├── build.cmd                  # 전체 빌드
├── build-and-restart.bat      # 빌드 + 서비스 재시작 (개발용)
├── restart-service.bat        # 서비스 재시작
├── stop-daemon.bat            # daemon 모드 중지
├── start-watchdog.bat         # 워치독 수동 실행
├── register-watchdog.ps1      # 워치독 스케줄 작업 등록
├── watchdog-restart.ps1       # 워치독 루프
└── installer/
    └── openclaw-setup.nsi     # NSIS 인스톨러 스크립트
```

## 3. NSIS 인스톨러

### 3.1 기본 정보

| 항목 | 값 |
|------|-----|
| 제품명 | OpenClaw Gateway |
| 설치경로 | `$PROGRAMFILES\OpenClaw` |
| 산출물 | `OpenClawGateway-Setup.exe` |
| 권한 | 관리자 필수 (`RequestExecutionLevel admin`) |
| 언인스톨러 | `$INSTDIR\uninstall.exe` |

### 3.2 설치 흐름

```
1. 관리자 권한 확인
2. 기존 설치 확인
   ├─ 있으면: 서비스 중지 → 워치독 중지 → 파일 교체
   └─ 없으면: 새 설치
3. 파일 복사
   ├─ $INSTDIR\clawdbot-service.exe
   ├─ $INSTDIR\tool\idle-detector.exe
   ├─ $INSTDIR\tool\ecount-test.exe
   └─ $INSTDIR\watchdog-restart.ps1
4. 서비스 등록: nsExec clawdbot-service.exe install
5. 워치독 등록: PowerShell Register-ScheduledTask
6. 서비스 시작: nsExec clawdbot-service.exe start
7. 언인스톨러 생성
8. 레지스트리 등록 (프로그램 추가/제거)
```

### 3.3 제거 흐름

```
1. maintenance.flag 생성 (워치독 재시작 방지)
2. 서비스 중지 + 삭제: nsExec clawdbot-service.exe uninstall
3. 워치독 스케줄 작업 삭제: Unregister-ScheduledTask
4. 파일 삭제
5. 레지스트리 정리
6. 설치 디렉토리 삭제
```

### 3.4 NSIS 스크립트 구조

```nsi
!include "MUI2.nsh"

Name "OpenClaw Gateway"
OutFile "OpenClawGateway-Setup.exe"
InstallDir "$PROGRAMFILES\OpenClaw"
RequestExecutionLevel admin

; Pages
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_LANGUAGE "Korean"

Section "Install"
  ; 기존 서비스 중지
  ; 파일 복사
  ; 서비스 등록 + 시작
  ; 워치독 등록
  ; 언인스톨러 생성
  ; 레지스트리 등록
SectionEnd

Section "Uninstall"
  ; maintenance.flag 생성
  ; 서비스 중지 + 삭제
  ; 워치독 삭제
  ; 파일 삭제
  ; 레지스트리 삭제
SectionEnd
```

## 4. 구현 순서

```
[Step 1] 워치독 수동 중지 개선
  ├─ service.go: exeDir() 함수 추가
  ├─ service.go: Stop 핸들러에 maintenance.flag 생성
  ├─ service.go: Execute 시작에 maintenance.flag 삭제
  └─ watchdog-restart.ps1: 헬스체크에 플래그 확인

[Step 2] BAT 파일 정리
  ├─ commit.bat, push.bat, push-all.bat, do-restart.bat 삭제
  ├─ restart-service.bat에 maintenance.flag 삭제 추가
  └─ build-and-restart.bat에 maintenance.flag 삭제 추가

[Step 3] NSIS 인스톨러
  ├─ installer/ 디렉토리 생성
  ├─ openclaw-setup.nsi 작성
  └─ 빌드 테스트
```
