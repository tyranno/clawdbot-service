package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/chromedp/chromedp"
)

// ===== 설정 =====

type EcountConfig struct {
	Enabled    bool   `json:"enabled"`
	ComCode    string `json:"com_code"`
	UserID     string `json:"user_id"`
	Password   string `json:"password"`
	Zone       string `json:"zone"`
	PollMinute int    `json:"poll_minute"`
}

func getEcountConfig() *EcountConfig {
	path := filepath.Join(userHome, ".openclaw", "ecount-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var cfg EcountConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		log.Printf("[Ecount] Failed to parse config: %v", err)
		return nil
	}
	if cfg.PollMinute <= 0 {
		cfg.PollMinute = 5
	}
	return &cfg
}

// ===== 결재 항목 =====

type EcountApprovalItem struct {
	DateNo  string
	Title   string
	DocType string
	Drafter string
}

// ===== 상태 저장 =====

type EcountState struct {
	LastSeenDateNos []string  `json:"last_seen_date_nos"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func loadEcountState() EcountState {
	path := filepath.Join(userHome, ".openclaw", "ecount-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return EcountState{}
	}
	var s EcountState
	json.Unmarshal(data, &s)
	return s
}

func saveEcountState(s EcountState) {
	s.UpdatedAt = time.Now()
	path := filepath.Join(userHome, ".openclaw", "ecount-state.json")
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(path, data, 0644)
}

// ===== 크롤러 =====

func fetchApprovalListChromedp(cfg *EcountConfig) ([]EcountApprovalItem, error) {
	loginURL := fmt.Sprintf("https://login%s.ecount.com", strings.ToLower(cfg.Zone))
	erpURL := loginURL + fmt.Sprintf(
		"/ec5/view/erp?w_flag=1#menuType=MENUTREE_000007&menuSeq=MENUTREE_000007&groupSeq=MENUTREE_000039&prgId=C000007&depth=1",
	)

	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-dev-shm-usage", true),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36"),
		chromedp.ModifyCmdFunc(func(cmd *exec.Cmd) {
			cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
		}),
	)

	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	defer allocCancel()

	ctx, cancel := chromedp.NewContext(allocCtx)
	defer cancel()

	timeoutCtx, timeoutCancel := context.WithTimeout(ctx, 90*time.Second)
	defer timeoutCancel()

	// 1. 로그인 페이지 이동
	log.Println("[Ecount] Navigating to login page...")
	err := chromedp.Run(timeoutCtx,
		chromedp.Navigate(loginURL),
		chromedp.WaitVisible(`input[placeholder="회사코드"], input[name="COM_CODE"], #txtComCode`, chromedp.ByQuery),
	)
	if err != nil {
		// placeholder가 한글이라 다른 셀렉터 시도
		err = chromedp.Run(timeoutCtx,
			chromedp.WaitVisible(`button`, chromedp.ByQuery),
		)
		if err != nil {
			return nil, fmt.Errorf("login page load failed: %v", err)
		}
	}

	// 2. 로그인 폼 입력 (JS로 직접 값 세팅 후 클릭)
	log.Println("[Ecount] Logging in...")
	var loginResult string
	err = chromedp.Run(timeoutCtx,
		chromedp.Evaluate(fmt.Sprintf(`
			(function() {
				// 입력 필드 찾기
				var inputs = document.querySelectorAll('input[type="text"], input[type="password"], input:not([type])');
				var comInput, idInput, pwInput;
				inputs.forEach(function(el) {
					var ph = el.placeholder || '';
					var id = el.id || '';
					var nm = el.name || '';
					if (ph.includes('회사') || id.includes('com') || nm.includes('COM')) comInput = el;
					else if (ph.includes('아이디') || ph.includes('ID') || id.includes('user') || nm.includes('USER')) idInput = el;
					else if (el.type === 'password' || ph.includes('비밀') || id.includes('pwd') || nm.includes('PWD')) pwInput = el;
				});
				if (!comInput || !idInput || !pwInput) {
					// 순서대로 시도
					var allInputs = document.querySelectorAll('input');
					if (allInputs.length >= 3) {
						comInput = allInputs[0];
						idInput = allInputs[1];
						pwInput = allInputs[2];
					}
				}
				if (comInput) { comInput.value = '%s'; comInput.dispatchEvent(new Event('input', {bubbles:true})); }
				if (idInput)  { idInput.value  = '%s'; idInput.dispatchEvent(new Event('input', {bubbles:true})); }
				if (pwInput)  { pwInput.value  = '%s'; pwInput.dispatchEvent(new Event('input', {bubbles:true})); }
				return (comInput ? '1' : '0') + (idInput ? '1' : '0') + (pwInput ? '1' : '0');
			})()
		`, cfg.ComCode, cfg.UserID, cfg.Password), &loginResult),
	)
	if err != nil {
		return nil, fmt.Errorf("fill login form: %v", err)
	}
	log.Printf("[Ecount] Form fill result: %s", loginResult)

	// 로그인 버튼 클릭
	err = chromedp.Run(timeoutCtx,
		chromedp.Evaluate(`
			(function() {
				var btn = document.querySelector('button[type="submit"]') ||
				          document.querySelector('button.btn-login') ||
				          document.querySelector('button');
				if (btn) { btn.click(); return 'clicked'; }
				return 'no button';
			})()
		`, &loginResult),
		chromedp.Sleep(3*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("login click: %v", err)
	}

	// 3. ERP 내결재관리 페이지로 이동
	log.Printf("[Ecount] Navigating to approval page: %s", erpURL)
	err = chromedp.Run(timeoutCtx,
		chromedp.Navigate(erpURL),
		// 그리드 테이블 로드 대기 (최대 30초)
		chromedp.WaitVisible(`table tr td`, chromedp.ByQuery),
		chromedp.Sleep(3*time.Second), // WebSocket 데이터 수신 대기
	)
	if err != nil {
		return nil, fmt.Errorf("navigate to approval page: %v", err)
	}

	// 4. DOM에서 결재 목록 추출 (수신참조 제외)
	log.Println("[Ecount] Extracting approval list from DOM...")
	var itemsJSON string
	err = chromedp.Run(timeoutCtx,
		chromedp.Evaluate(`
			(function() {
				var dateRe = /^\d{4}\/\d{2}\/\d{2}/;
				var rows = Array.from(document.querySelectorAll('tr')).filter(function(r) {
					var tds = r.querySelectorAll('td');
					return tds.length >= 5 && dateRe.test(tds[1] ? tds[1].textContent.trim() : '');
				});
				var items = rows.map(function(r) {
					var tds = Array.from(r.querySelectorAll('td')).map(function(t){ return t.textContent.trim(); });
					return {
						date_no: tds[1] || '',
						title:   tds[2] || '',
						doc_type: tds[3] || '',
						drafter: tds[4] || '',
						role:    tds[5] || ''
					};
				});
				// 수신참조 제외 - 실제 결재 필요한 건만
				var approvalItems = items.filter(function(it) {
					return it.role !== '수신참조';
				});
				return JSON.stringify(approvalItems);
			})()
		`, &itemsJSON),
	)
	if err != nil {
		return nil, fmt.Errorf("extract DOM: %v", err)
	}

	// 5. JSON 파싱
	var raw []struct {
		DateNo  string `json:"date_no"`
		Title   string `json:"title"`
		DocType string `json:"doc_type"`
		Drafter string `json:"drafter"`
		Role    string `json:"role"`
	}
	if err := json.Unmarshal([]byte(itemsJSON), &raw); err != nil {
		return nil, fmt.Errorf("parse items JSON: %v", err)
	}

	var items []EcountApprovalItem
	for _, r := range raw {
		items = append(items, EcountApprovalItem{
			DateNo:  r.DateNo,
			Title:   r.Title,
			DocType: r.DocType,
			Drafter: r.Drafter,
		})
	}

	log.Printf("[Ecount] Found %d items requiring approval (수신참조 excluded)", len(items))
	return items, nil
}

// ===== 모니터 =====

func StartEcountMonitor(ctx context.Context) {
	cfg := getEcountConfig()
	if cfg == nil || !cfg.Enabled {
		log.Println("[Ecount] Monitor disabled (no config or enabled=false)")
		return
	}

	log.Printf("[Ecount] Monitor started (zone=%s, user=%s, poll=%dm)", cfg.Zone, cfg.UserID, cfg.PollMinute)

	ticker := time.NewTicker(time.Duration(cfg.PollMinute) * time.Minute)
	defer ticker.Stop()

	// 시작 시 즉시 1회 실행
	checkEcount(cfg)

	for {
		select {
		case <-ctx.Done():
			log.Println("[Ecount] Monitor stopped")
			return
		case <-ticker.C:
			checkEcount(cfg)
		}
	}
}

func checkEcount(cfg *EcountConfig) {
	log.Println("[Ecount] Checking approval list...")
	items, err := fetchApprovalListChromedp(cfg)
	if err != nil {
		log.Printf("[Ecount] Error: %v", err)
		return
	}

	state := loadEcountState()
	seenSet := make(map[string]bool)
	for _, d := range state.LastSeenDateNos {
		seenSet[d] = true
	}

	var newItems []EcountApprovalItem
	for _, item := range items {
		if !seenSet[item.DateNo] {
			newItems = append(newItems, item)
		}
	}

	if len(newItems) > 0 {
		log.Printf("[Ecount] %d new approval item(s)!", len(newItems))
		sendEcountAlert(newItems)
	} else {
		log.Println("[Ecount] No new approvals.")
	}

	// 상태 갱신
	var dateNos []string
	for _, item := range items {
		dateNos = append(dateNos, item.DateNo)
	}
	saveEcountState(EcountState{LastSeenDateNos: dateNos})
}

func sendEcountAlert(items []EcountApprovalItem) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 <b>결재 요청 %d건</b>\n\n", len(items)))
	for i, item := range items {
		if i >= 5 {
			sb.WriteString(fmt.Sprintf("... 외 %d건\n", len(items)-5))
			break
		}
		sb.WriteString(fmt.Sprintf("• <b>%s</b>\n  📅 %s | 👤 %s\n\n",
			item.Title, item.DateNo, item.Drafter))
	}
	msg := sb.String()

	if err := sendTelegramNotification(msg); err != nil {
		log.Printf("[Ecount] Telegram alert failed: %v", err)
	}
	if err := sendVoiceChatNotification("📋 결재 요청", msg); err != nil {
		log.Printf("[Ecount] VoiceChat alert failed: %v", err)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
