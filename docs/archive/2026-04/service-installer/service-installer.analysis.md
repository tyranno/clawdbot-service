# Gap Analysis: service-installer

> Design: [service-installer.design.md](../02-design/features/service-installer.design.md)
> Date: 2026-04-03

## Overall Match Rate: 100% (31/31 items)

## 1. 워치독 수동 중지 개선 — 6/6 = 100%

| # | Design 요구사항 | 구현 위치 | 상태 |
|---|----------------|----------|------|
| 1-1 | `exeDir()` 헬퍼 함수 | `service.go:22-24` | PASS |
| 1-2 | Execute() 시작: maintenance.flag 삭제 | `service.go:31` | PASS |
| 1-3 | Stop/Shutdown: maintenance.flag 생성 (cancel 전) | `service.go:134-140` | PASS |
| 1-4 | 워치독 헬스체크: maintenance.flag 시 스킵 | `watchdog-restart.ps1:40-41` | PASS |
| 1-5 | 워치독 트리거 재시작: maintenance.flag 삭제 | `watchdog-restart.ps1:31` | PASS |
| 1-6 | build-and-restart.bat: maintenance.flag 삭제 | `build-and-restart.bat:25` | PASS |

## 2. BAT 파일 정리 — 6/6 = 100%

| # | Design 요구사항 | 상태 | 비고 |
|---|----------------|------|------|
| 2-1 | `commit.bat` 삭제 | PASS | 파일 없음 확인 |
| 2-2 | `push.bat` 삭제 | PASS | 파일 없음 확인 |
| 2-3 | `push-all.bat` 삭제 | PASS | 파일 없음 확인 |
| 2-4 | `do-restart.bat` 삭제 | PASS | 파일 없음 확인 |
| 2-5 | restart-service.bat: maintenance.flag 삭제 추가 | PASS | `restart-service.bat:6-7` |
| 2-6 | build-and-restart.bat: maintenance.flag 삭제 추가 | PASS | `build-and-restart.bat:25` |

## 3. NSIS 인스톨러 — 19/19 = 100%

| # | Design 요구사항 | 구현 위치 | 상태 |
|---|----------------|----------|------|
| 3-1 | `installer/openclaw-setup.nsi` 생성 | 파일 존재 (167줄) | PASS |
| 3-2 | 제품명: OpenClaw Gateway | `nsi:9` | PASS |
| 3-3 | 설치경로: `$PROGRAMFILES\OpenClaw` | `nsi:19` | PASS |
| 3-4 | 산출물: `OpenClawGateway-Setup.exe` | `nsi:18` | PASS |
| 3-5 | `RequestExecutionLevel admin` | `nsi:21` | PASS |
| 3-6 | clawdbot-service.exe 복사 | `nsi:70` | PASS |
| 3-7 | idle-detector.exe 복사 | `nsi:76` | PASS |
| 3-8 | ecount-test.exe 복사 | `nsi:77` | PASS |
| 3-9 | watchdog-restart.ps1 복사 | `nsi:71` | PASS |
| 3-10 | 서비스 등록 (nsExec install) | `nsi:87` | PASS |
| 3-11 | 워치독 등록 (Register-ScheduledTask) | `nsi:96-101` | PASS |
| 3-12 | 서비스 시작 | `nsi:106` | PASS |
| 3-13 | 언인스톨러 생성 | `nsi:113` | PASS |
| 3-14 | 레지스트리 (프로그램 추가/제거) | `nsi:116-121` | PASS |
| 3-15 | 제거: maintenance.flag 생성 | `nsi:129-131` | PASS |
| 3-16 | 제거: 서비스 중지+삭제 | `nsi:135-139` | PASS |
| 3-17 | 제거: 워치독 삭제 | `nsi:143-148` | PASS |
| 3-18 | 제거: 파일 삭제 | `nsi:152-160` | PASS |
| 3-19 | 제거: 레지스트리 삭제 | `nsi:163` | PASS |

## Design 외 추가 구현 (7건)

Design에 명시되지 않았으나 Design 의도를 보강하는 추가 구현:

| # | 항목 | 위치 | 설명 |
|---|------|------|------|
| A-1 | 기존 서비스 사전 중지 | `nsi:53-66` | 설치 전 maintenance.flag + stop |
| A-2 | 재설치 안전장치 | `nsi:85-86` | uninstall 후 install |
| A-3 | 플래그 생성 에러 핸들링 | `service.go:136-138` | WriteFile 에러 로깅 |
| A-4 | Welcome/Finish 페이지 | `nsi:30-33` | UX 개선 |
| A-5 | 버전 정보 메타데이터 | `nsi:42-46` | VIProductVersion |
| A-6 | Unicode 지원 | `nsi:22` | `Unicode true` |
| A-7 | 설치 후 워치독 즉시 시작 | `nsi:109-110` | schtasks /run |

## 결론

| 항목 | 점수 |
|------|:----:|
| Design 일치율 | 100% |
| 누락 기능 | 0 |
| 변경 기능 | 0 |
| **Overall Match Rate** | **100%** |

Match Rate >= 90% — **Check phase PASSED.**
