# mail-checker

ecount IMAP 메일 체커

## 설정

`C:\Users\lab\.openclaw\mail-config.json` (git 제외, 로컬 전용)

```json
{
  "imap": {
    "host": "wmbox2.ecount.com",
    "port": 993,
    "secure": true,
    "auth": {
      "user": "yikim@doowoninc.com",
      "pass": "YOUR_PASSWORD"
    }
  }
}
```

## 사용법

```bash
# 최근 10개 메일
node mail-checker.js

# 최근 N개
node mail-checker.js --limit 20

# 읽지 않은 메일만
node mail-checker.js --unread

# JSON 출력
node mail-checker.js --json
```
