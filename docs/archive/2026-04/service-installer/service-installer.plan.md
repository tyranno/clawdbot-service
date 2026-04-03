# Plan: 서비스 설치/관리 개선

## 개요

OpenClaw Gateway 서비스의 설치, 관리, 배포를 개선한다.
현재 수동 명령어 기반의 설치/관리 방식을 NSIS 인스톨러로 자동화하고,
워치독의 수동 중지 불가 문제를 해결하며, 산재한 BAT 파일을 정리한다.

## 배경

### 현재 문제점

1. **워치독 수동 중지 불가**: 서비스 관리자에서 수동으로 서비스를 중지해도 워치독이 30초 내에 자동 재시작함. 크래시와 의도적 중지를 구분하지 못함.

2. **설치 과정이 복잡**: 서비스 설치에 아래 단계를 수동으로 수행해야 함:
   - `build.cmd` 실행
   - `clawdbot-service.exe install` (관리자 권한)
   - `register-watchdog.ps1` 실행
   - 서비스 시작

3. **BAT 파일 난립**: 일회성 커밋용 bat, 중복 기능 bat 등이 정리되지 않음

### 현재 구성

| 구성요소 | 등록 방식 | 시작 방식 |
|----------|-----------|-----------|
| OpenClawGateway 서비스 | `mgr.CreateService()` (Windows Service) | `StartType: Automatic` |
| OpenClawWatchdog | `Register-ScheduledTask` (AtStartup) | SYSTEM 계정, 부팅 시 자동 |

### 빌드 산출물

| 파일 | 소스 | 용도 |
|------|------|------|
| `clawdbot-service.exe` | `.` (루트) | 메인 서비스 |
| `idle-detector.exe` | `tool/idle-detector/` | 유휴 감지 |
| `ecount-test.exe` | `cmd/ecount-test/` | Ecount 자동화 |

## 작업 범위

### Task 1: 워치독 수동 중지 개선

**목표**: 서비스 관리자에서 수동 중지 시 워치독이 재시작하지 않도록 함

**방식**: 유지보수 플래그 파일 (`maintenance.flag`)

**흐름**:
```
[정상 종료 - svc.Stop/Shutdown]
  Go 서비스 Stop 핸들러 → maintenance.flag 생성 → 서비스 종료
  워치독 헬스체크 → maintenance.flag 존재 → 재시작 스킵

[비정상 종료 - 크래시]
  서비스 크래시 → maintenance.flag 없음
  워치독 헬스체크 → 플래그 없음 → 자동 재시작

[서비스 시작]
  Go 서비스 Execute 시작 → maintenance.flag 삭제
```

**변경 파일**:
- `service.go` — Stop 핸들러에서 플래그 파일 생성, Execute 시작 시 삭제
- `watchdog-restart.ps1` — 헬스체크에서 플래그 확인 로직 추가

### Task 2: BAT 파일 정리

**삭제 대상** (일회성, 하드코딩된 커밋용):
- `commit.bat` — 특정 커밋 하드코딩
- `push.bat` — 특정 파일 하드코딩  
- `push-all.bat` — 특정 파일 하드코딩

**통합 대상**:
- `restart-service.bat` + `do-restart.bat` → `restart-service.bat` 하나로 통합

**유지 대상**:
- `build.cmd` — 전체 빌드
- `build-and-restart.bat` — 개발용 빌드+재시작
- `stop-daemon.bat` — daemon 모드 중지
- `start-watchdog.bat` — 워치독 실행
- `register-watchdog.ps1` — 워치독 등록
- `watchdog-restart.ps1` — 워치독 루프

### Task 3: NSIS 인스톨러

**목표**: 더블클릭 한 번으로 전체 설치/제거 가능

**설치 시 수행 작업**:
1. 파일 복사 → `C:\Program Files\OpenClaw\`
   - `clawdbot-service.exe`
   - `tool\idle-detector\idle-detector.exe`
   - `cmd\ecount-test\ecount-test.exe`
   - `watchdog-restart.ps1`
2. Windows 서비스 등록 (`clawdbot-service.exe install`)
3. 워치독 스케줄 작업 등록 (`Register-ScheduledTask`)
4. 서비스 시작

**제거 시 수행 작업**:
1. 서비스 중지 + 삭제 (`clawdbot-service.exe uninstall`)
2. 워치독 스케줄 작업 삭제 (`Unregister-ScheduledTask`)
3. 파일 제거
4. 설치 디렉토리 삭제

**NSIS 스크립트 위치**: `installer/openclaw-setup.nsi`

**인스톨러 산출물**: `OpenClawGateway-Setup.exe`

## 우선순위

| 순서 | 작업 | 이유 |
|------|------|------|
| 1 | 워치독 수동 중지 개선 | 현재 운영 문제 해결 |
| 2 | BAT 파일 정리 | 코드베이스 정리, 인스톨러 작업 전 선행 |
| 3 | NSIS 인스톨러 | 정리된 파일 기반으로 패키징 |

## 구현 시 주의사항

- `maintenance.flag` 경로는 exe와 같은 디렉토리 사용 (서비스 실행 경로)
- NSIS 인스톨러는 관리자 권한 필요 (`RequestExecutionLevel admin`)
- 기존 설치가 있을 경우 업그레이드 지원 (서비스 중지 → 파일 교체 → 재시작)
- `idle-detector.exe`는 서비스가 아닌 서비스 내부에서 실행하는 도구
