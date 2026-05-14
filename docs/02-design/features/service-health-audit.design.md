# Design: 서비스 구동 및 기능 문제점 수정

> Plan: [service-health-audit.plan.md](../../01-plan/features/service-health-audit.plan.md)
> 범위: P0(보안) + P1(안정성) — 총 14건

## 1. 하드코딩 비밀번호 제거 (S-1)

### cmd/ecount-test/main.go

**현재**: line 23-27 에 평문 비밀번호 하드코딩
```go
const (
    comCode  = "23580"
    userID   = "YIKIM"
    password = "rladyddlf0115#"
    zone     = "CC"
    tgChatID = "6723802240"
)
```

**변경**: 설정 파일에서 로드하도록 변경
```go
// const 블록에서 credentials 제거
// ecount-config.json에서 로드 (메인 서비스와 동일한 설정 공유)
func loadConfig() (*EcountTestConfig, error) {
    home, _ := os.UserHomeDir()
    path := filepath.Join(home, ".openclaw", "ecount-config.json")
    data, err := os.ReadFile(path)
    // ...
}
```

**참고**: ecount.go의 `getEcountConfig()`가 이미 `ecount-config.json`에서 credentials를 로드함. cmd/ecount-test도 같은 파일 사용.

## 2. Telegram Chat ID 설정 기반 (H-3)

### notify.go

**현재**: line 17 에 하드코딩
```go
const telegramChatID = "6723802240"
```

**변경**: `service-config.txt`에서 로드

```go
// const 제거, getChatID() 함수 추가
func getChatID() string {
    if id := os.Getenv("TELEGRAM_CHAT_ID"); id != "" {
        return id
    }
    // service-config.txt에서 TELEGRAM_CHAT_ID= 읽기
    // getBotToken()과 동일한 패턴
    configPath := userHome + `\.openclaw\service-config.txt`
    data, err := os.ReadFile(configPath)
    if err == nil {
        for _, line := range strings.Split(string(data), "\n") {
            line = strings.TrimSpace(line)
            if strings.HasPrefix(line, "TELEGRAM_CHAT_ID=") {
                return strings.TrimPrefix(line, "TELEGRAM_CHAT_ID=")
            }
        }
    }
    return ""  // 설정 없으면 빈 문자열 → 알림 스킵
}
```

**config.go 변경**: Config 구조체에 추가
```go
type Config struct {
    // ...기존 필드...
    TelegramChatID string
}
```

**LoadConfig() switch 추가**:
```go
case "TELEGRAM_CHAT_ID":
    config.TelegramChatID = value
```

**CreateDefaultConfig() 추가**:
```
# === Telegram ===
TELEGRAM_CHAT_ID=6723802240
```

## 3. worktime.go 하드코딩 경로 제거 (H-1, H-2)

### worktime.go

**현재**: line 219-227
```go
const (
    worktimeDir         = `C:\Users\lab\업무시간기록`
    worktimeFile        = `C:\Users\lab\업무시간기록\worktime2.txt`
    notionAPIKeyFile    = `C:\Users\lab\.openclaw\notion-api-key.txt`
    notionParentIDFile  = `C:\Users\lab\.openclaw\notion-parent-id.txt`
)
```

**변경**: `userHome` 기반 동적 경로
```go
var (
    worktimeDir        = filepath.Join(userHome, "업무시간기록")
    worktimeFile       = filepath.Join(userHome, "업무시간기록", "worktime2.txt")
    notionAPIKeyFile   = filepath.Join(userHome, ".openclaw", "notion-api-key.txt")
    notionParentIDFile = filepath.Join(userHome, ".openclaw", "notion-parent-id.txt")
)
```

**참고**: `const` → `var`로 변경 (filepath.Join은 컴파일타임 상수 불가)

## 4. Node.js 경로 탐색 개선 (H-5)

### service.go

**현재**: `findNodeExe()` line 163-180, `findEntryJS()` line 182-196 — 특정 버전 하드코딩

**변경**: PATH에서 찾기 우선 + 동적 버전 스캔
```go
func findNodeExe() string {
    // 1. PATH에서 먼저 찾기
    if p, err := exec.LookPath("node.exe"); err == nil {
        return p
    }
    // 2. 알려진 위치 스캔 (nvm, fnm — 버전 무관)
    scanDirs := []string{
        filepath.Join(userHome, "AppData", "Roaming", "nvm"),
        filepath.Join(userHome, "AppData", "Roaming", "fnm", "node-versions"),
        `C:\Program Files\nodejs`,
    }
    for _, dir := range scanDirs {
        matches, _ := filepath.Glob(filepath.Join(dir, "*", "node.exe"))
        if len(matches) > 0 {
            return matches[len(matches)-1]  // 최신 버전
        }
        // 직접 node.exe 확인
        p := filepath.Join(dir, "node.exe")
        if _, err := os.Stat(p); err == nil {
            return p
        }
    }
    return "node.exe"  // fallback to PATH
}
```

`findEntryJS()`도 동일 패턴 적용.

## 5. JSON Unmarshal 에러 처리 (E-1)

### ecount.go:69

**현재**:
```go
json.Unmarshal(data, &s)  // 에러 무시
```

**변경**:
```go
if err := json.Unmarshal(data, &s); err != nil {
    log.Printf("[Ecount] Failed to parse state file: %v", err)
    return EcountState{}
}
```

## 6. WatchConfig 고루틴 누수 (C-2)

### config.go:137-188

**현재**: `WatchConfig()`에 취소 메커니즘 없음. watcher.Close() 미호출.

**변경**: context 기반 취소 추가
```go
func WatchConfig(ctx context.Context, reloadCallback func()) {
    // ...watcher 생성...
    go func() {
        defer watcher.Close()
        for {
            select {
            case <-ctx.Done():
                log.Println("[Config] Watcher stopped")
                return
            case event, ok := <-watcher.Events:
                // ...기존 로직...
            case err, ok := <-watcher.Errors:
                // ...기존 로직...
            }
        }
    }()
}
```

**호출부 변경** (service.go, daemon 모드 모두):
```go
WatchConfig(ctx, func() { ... })
```

## 7. Bridge 재연결 백오프 (N-1)

### bridge.go:108-127

**현재**: 항상 5초 대기
```go
case <-time.After(5 * time.Second):
```

**변경**: 지수 백오프 (5s → 10s → 20s → 40s → 60s max)
```go
backoff := 5 * time.Second
maxBackoff := 60 * time.Second

for {
    // ...
    err := bridgeSession(ctx)
    if err != nil {
        log.Printf("[Bridge] Session error: %v", err)
    }

    select {
    case <-ctx.Done():
        return
    case <-time.After(backoff):
        log.Printf("[Bridge] Reconnecting (backoff=%v)...", backoff)
    }

    // 실패 시 백오프 증가
    backoff *= 2
    if backoff > maxBackoff {
        backoff = maxBackoff
    }

    // 성공적 세션 후 리셋 (bridgeSession이 정상 종료 시)
    // → bridgeSession에서 성공 여부 반환 필요
}
```

**bridgeSession 반환값 변경**:
```go
func bridgeSession(ctx context.Context) (connected bool, err error)
```
`connected=true`이면 백오프 리셋.

## 8. daemon 모드 maintenance.flag 연동 (O-6)

### service.go — runDaemon() 함수

**현재**: daemon 모드(SIGTERM)에서 maintenance.flag 미생성

**변경**: SIGTERM 핸들러에 플래그 생성 추가
```go
// Wait for interrupt signal
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
<-sigCh

log.Println("OpenClaw Gateway daemon stopping...")

// 정상 종료 플래그 생성
flagPath := filepath.Join(exeDir(), "maintenance.flag")
os.WriteFile(flagPath, []byte(time.Now().Format(time.RFC3339)), 0644)

go notifyShutdown()
```

## 9. TLS InsecureSkipVerify 제거 (S-2)

### bridge.go:380-385

**현재**:
```go
TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
```

**변경**: 설정 기반 선택
```go
tlsConfig := &tls.Config{}
if os.Getenv("OPENCLAW_INSECURE_TLS") == "1" {
    tlsConfig.InsecureSkipVerify = true
    log.Println("[Bridge] WARNING: TLS verification disabled")
}
client := &http.Client{
    Timeout:   60 * time.Second,
    Transport: &http.Transport{TLSClientConfig: tlsConfig},
}
```

## 10. 불필요 파일 정리 (O-7)

- `fix-prompt.txt` 삭제
- `.gitignore`에 `OpenClawGateway-Setup.exe` 추가 (빌드 산출물)

## 구현 순서

```
[Step 1] 보안 — 하드코딩 제거
  ├─ cmd/ecount-test/main.go: credentials → config 파일 로드
  ├─ notify.go: telegramChatID → config 기반
  └─ config.go: TelegramChatID 필드 추가

[Step 2] 하드코딩 경로 제거
  ├─ worktime.go: const → var (userHome 기반)
  └─ service.go: findNodeExe/findEntryJS 동적 탐색

[Step 3] 안정성 개선
  ├─ ecount.go: JSON Unmarshal 에러 처리
  ├─ config.go: WatchConfig context 추가
  └─ bridge.go: 재연결 지수 백오프

[Step 4] 운영 개선
  ├─ service.go: daemon 모드 maintenance.flag
  ├─ bridge.go: TLS 설정 기반
  └─ fix-prompt.txt 삭제, .gitignore 업데이트
```

## 변경 파일 요약

| 파일 | 변경 항목 |
|------|----------|
| cmd/ecount-test/main.go | credentials 하드코딩 제거 → config 로드 |
| notify.go | telegramChatID const 제거 → getChatID() |
| config.go | TelegramChatID 필드, WatchConfig ctx 추가 |
| worktime.go | const 경로 → var (userHome 기반) |
| service.go | findNodeExe 개선, daemon maintenance.flag, WatchConfig 호출부 |
| ecount.go | JSON Unmarshal 에러 처리 |
| bridge.go | 지수 백오프, TLS 설정 기반 |
| .gitignore | OpenClawGateway-Setup.exe 추가 |
