# Plan: 서비스 구동 및 기능 문제점 파악

## 개요

OpenClaw Gateway 서비스(clawdbot-service)의 전체 코드베이스를 분석하여
보안, 안정성, 아키텍처 문제점을 식별하고 개선 우선순위를 수립한다.

## 코드베이스 현황

| 파일 | 라인 수 | 역할 |
|------|:-------:|------|
| main.go | ~70 | 서비스 진입점, 명령어 분기 |
| service.go | ~570 | 핵심 오케스트레이션, 게이트웨이 프로세스 관리 |
| config.go | ~265 | 설정 로드/감시/핫리로드 |
| bridge.go | ~474 | GCP 릴레이 브릿지 (TCP 프로토콜) |
| ecount.go | ~341 | Ecount 결재 모니터링 (chromedp) |
| worktime.go | ~1221 | 근무시간 추적, Notion API 연동 |
| notify.go | ~173 | Telegram/VoiceChat 알림 |
| power.go | ~76 | 절전/복귀 감지 |
| update.go | ~147 | openclaw 패키지 업데이트 |
| tool/idle-detector | ~93 | 유휴 시간 감지 (Named Pipe) |
| cmd/ecount-test | ~462 | Ecount 테스트 유틸 |

## 식별된 문제점 (32건)

### 보안 (CRITICAL) — 4건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| S-1 | 비밀번호 하드코딩 | cmd/ecount-test/main.go:24-27 | 평문 비밀번호 소스코드에 노출 |
| S-2 | TLS 인증서 검증 비활성화 | bridge.go:383 | `InsecureSkipVerify: true` — MITM 공격 가능 |
| S-3 | Telegram 토큰 평문 저장 | service-config.txt | 암호화 없이 설정 파일에 저장 |
| S-4 | 로그에 민감 데이터 출력 | 전체 | API 키, 파일 경로 등이 로그에 평문 기록 |

### 하드코딩 경로 — 5건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| H-1 | 근무시간 경로 하드코딩 | worktime.go:219-220 | `C:\Users\lab\업무시간기록` 고정 |
| H-2 | Notion API 키 경로 하드코딩 | worktime.go:226-227 | `C:\Users\lab\.openclaw\notion-*` 고정 |
| H-3 | Telegram Chat ID 하드코딩 | notify.go:17 | `6723802240` 고정 |
| H-4 | Notion API 버전 하드코딩 | worktime.go:218 | `2022-06-28` 고정 |
| H-5 | Node.js 경로 하드코딩 | service.go:163-179 | 특정 버전/경로만 탐색 |

### 동시성/안정성 — 6건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| C-1 | Bridge 메시지 경합조건 | bridge.go:200,307-314 | 동시 요청 시 응답 섞임 가능 |
| C-2 | WatchConfig 고루틴 누수 | config.go:138-188 | 취소 메커니즘 없음 |
| C-3 | Config 핫리로드 경합 | service.go:59-100 | 설정 변경 시 실행 중 고루틴 미취소 |
| C-4 | Bridge 하트비트 고루틴 누수 | bridge.go:159-173 | 세션 정리 전 하트비트 미종료 가능 |
| C-5 | Chromedp 컨텍스트 정리 미흡 | ecount.go:82-107 | 타임아웃 시 브라우저 프로세스 잔존 |
| C-6 | 전역 변수 비보호 접근 | bridge.go:24-29 | pkgBridgeServer 등 mutex 없음 |

### 에러 처리 — 5건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| E-1 | JSON Unmarshal 에러 무시 | ecount.go:69 | 파싱 실패 시 무시 |
| E-2 | Config 파일 없을 때 처리 미흡 | worktime.go | Notion 설정 없으면 런타임 에러 |
| E-3 | npm 미설치 시 업데이트 실패 | update.go | 에러 후 계속 진행 |
| E-4 | Ecount 모니터 재시도 없음 | ecount.go:252-276 | 실패 시 다음 주기까지 대기만 |
| E-5 | Idle 감지 폴백 비신뢰 | worktime.go:150-168 | `query user` 명령 비권장/비신뢰 |

### 네트워킹/성능 — 5건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| N-1 | Bridge 재연결 백오프 없음 | bridge.go:108-127 | 항상 5초 대기, 서버 과부하 가능 |
| N-2 | SSE 바이트 단위 읽기 | bridge.go:446-469 | 1바이트씩 읽기, 매우 비효율 |
| N-3 | OpenClaw 커넥션 풀링 없음 | bridge.go:216-338 | 매 요청 새 연결 |
| N-4 | 메시지 크기 제한 과다 | bridge.go:76 | 10MB — 메모리 문제 가능 |
| N-5 | Idle detector 재연결 백오프 없음 | tool/idle-detector | 항상 5초 대기 |

### 운영/품질 — 7건

| # | 문제 | 파일 | 설명 |
|---|------|------|------|
| O-1 | 로그 로테이션 없음 | 전체 | 디스크 가득 찰 가능성 |
| O-2 | 빌드 버전 정보 없음 | 전체 | 실행 중 버전 확인 불가 |
| O-3 | 단일 사용자 전용 | 전체 | 다른 사용자/PC에서 구동 불가 |
| O-4 | 과도한 루프 로깅 | worktime.go | 매초/분 로그 출력 → 디스크 부담 |
| O-5 | Power 감지 한계 | power.go | 타이머 기반 — 이벤트 누락 가능 |
| O-6 | stop-daemon 미연동 | service.go | daemon 모드에서 maintenance.flag 미생성 |
| O-7 | fix-prompt.txt 불필요 파일 | 루트 | 프로젝트에 불필요한 임시 파일 |

## 우선순위

### P0 — 즉시 수정 (보안)
- S-1: 하드코딩 비밀번호 제거
- H-1~H-3: 하드코딩 경로를 설정 기반으로 변경

### P1 — 단기 수정 (안정성)
- C-1: Bridge 경합조건
- C-2: WatchConfig 고루틴 누수
- E-1: JSON 에러 처리
- N-1: Bridge 재연결 백오프

### P2 — 중기 개선 (품질)
- O-1: 로그 로테이션
- O-2: 빌드 버전 정보
- N-2: SSE 읽기 효율화
- S-2: TLS 인증서 검증

### P3 — 장기 개선 (아키텍처)
- O-3: 멀티유저 지원
- N-3: 커넥션 풀링
- C-3: Config 핫리로드 안전성
