package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
)

const (
	comCode  = "23580"
	userID   = "YIKIM"
	password = "rladyddlf0115#"
	zone     = "CC"
	tgChatID = "6723802240"
)

type CookieEntry struct {
	Name    string  `json:"name"`
	Value   string  `json:"value"`
	Domain  string  `json:"domain"`
	Path    string  `json:"path"`
	Expires float64 `json:"expires"`
}

type SessionCache struct {
	Cookies   []CookieEntry `json:"cookies"`
	SavedAt   time.Time     `json:"saved_at"`
	EcReqSid  string        `json:"ec_req_sid"`
}

type Config struct {
	TelegramBotToken string `json:"telegram_bot_token"`
	ApproverFilter   string `json:"approver_filter"`
}

type ApprovalItem struct {
	DateNo   string `json:"date_no"`
	Title    string `json:"title"`
	DocType  string `json:"doc_type"`
	Drafter  string `json:"drafter"`
	Approver string `json:"approver"`
	Status   string `json:"status"`
}

type State struct {
	SeenDateNos []string `json:"seen_date_nos"`
}

func configPath() string {
	home, _ := os.UserHomeDir()
	return home + "/.openclaw/ecount-config.json"
}
func sessionPath() string {
	home, _ := os.UserHomeDir()
	return home + "/.openclaw/ecount-session.json"
}
func statePath() string {
	home, _ := os.UserHomeDir()
	return home + "/.openclaw/ecount-state.json"
}

func loadConfig() Config {
	data, err := os.ReadFile(configPath())
	if err != nil {
		log.Fatalf("config not found: %v", err)
	}
	var full map[string]interface{}
	json.Unmarshal(data, &full)
	cfg := Config{}
	if v, ok := full["telegram_bot_token"].(string); ok {
		cfg.TelegramBotToken = v
	}
	if v, ok := full["approver_filter"].(string); ok {
		cfg.ApproverFilter = v
	}
	return cfg
}

func loadSession() *SessionCache {
	data, err := os.ReadFile(sessionPath())
	if err != nil {
		return nil
	}
	var s SessionCache
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	// 6시간 이내면 유효
	if time.Since(s.SavedAt) > 6*time.Hour {
		log.Println("   세션 만료 (6시간 초과) → 재로그인")
		return nil
	}
	return &s
}

func saveSession(s *SessionCache) {
	s.SavedAt = time.Now()
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(sessionPath(), data, 0600)
}

func loadState() State {
	data, _ := os.ReadFile(statePath())
	var s State
	json.Unmarshal(data, &s)
	return s
}
func saveState(s State) {
	data, _ := json.MarshalIndent(s, "", "  ")
	os.WriteFile(statePath(), data, 0644)
}

func sendTelegram(token, chatID, msg string) error {
	body, _ := json.Marshal(map[string]interface{}{
		"chat_id": chatID, "text": msg, "parse_mode": "HTML",
	})
	resp, err := http.Post(
		fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", token),
		"application/json", bytes.NewReader(body),
	)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("telegram %d: %s", resp.StatusCode, rb)
	}
	return nil
}

func doLogin(ctx context.Context) (*SessionCache, error) {
	loginURL := fmt.Sprintf("https://login%s.ecount.com", strings.ToLower(zone))
	log.Printf("   로그인 URL: %s", loginURL)

	if err := chromedp.Run(ctx, chromedp.Navigate(loginURL)); err != nil {
		return nil, fmt.Errorf("navigate: %v", err)
	}
	chromedp.Run(ctx, chromedp.Sleep(3*time.Second))

	var curURL, title string
	chromedp.Run(ctx, chromedp.Location(&curURL), chromedp.Title(&title))
	log.Printf("   페이지: %s | %s", title, curURL)

	fillJS := fmt.Sprintf(`(function(){
		var all=Array.from(document.querySelectorAll('input'));
		var com,id,pw;
		all.forEach(function(el){
			var ph=(el.placeholder||'').toLowerCase(),nm=(el.name||'').toLowerCase();
			if(!com&&(ph.indexOf('\ud68c\uc0ac')>=0||nm.indexOf('com')>=0))com=el;
			else if(!id&&(ph.indexOf('\uc544\uc774\ub514')>=0||nm.indexOf('user')>=0))id=el;
			else if(!pw&&(el.type==='password'||nm.indexOf('pwd')>=0))pw=el;
		});
		if(!com&&all.length>0)com=all[0];
		if(!id&&all.length>1)id=all[1];
		if(!pw&&all.length>2)pw=all[2];
		function set(el,v){if(!el)return;el.value=v;el.dispatchEvent(new Event('input',{bubbles:true}));el.dispatchEvent(new Event('change',{bubbles:true}));}
		set(com,'%s');set(id,'%s');set(pw,'%s');
		return 'com:'+(com?com.value:'X')+' id:'+(id?id.value:'X')+' pw:'+(pw?'set':'X')+' total:'+all.length;
	})()`, comCode, userID, password)

	var fillResult string
	chromedp.Run(ctx, chromedp.Evaluate(fillJS, &fillResult))
	log.Printf("   폼 입력: %s", fillResult)
	chromedp.Run(ctx, chromedp.Sleep(1*time.Second))

	var clickResult string
	chromedp.Run(ctx,
		chromedp.Evaluate(`(function(){
			var btn=Array.from(document.querySelectorAll('button')).find(function(b){
				return b.textContent.indexOf('\ub85c\uadf8\uc778')>=0;
			});
			if(btn){btn.click();return 'clicked:'+btn.textContent.trim();}
			return 'no-btn count:'+document.querySelectorAll('button').length;
		})()`, &clickResult),
	)
	log.Printf("   버튼 클릭: %s", clickResult)
	chromedp.Run(ctx, chromedp.Sleep(8*time.Second))

	chromedp.Run(ctx, chromedp.Location(&curURL))
	log.Printf("   로그인 후 URL: %s", curURL)

	if !strings.Contains(curURL, "logincc.ecount.com") {
		// 현재 페이지 input 상태 출력
		var dbg string
		chromedp.Run(ctx, chromedp.Evaluate(`
		(function(){
			var ins=document.querySelectorAll('input');
			var r=[];
			ins.forEach(function(i,idx){if(idx<5)r.push(idx+':'+i.type+'='+i.value.substring(0,8));});
			return r.join(' | ')+' (total:'+ins.length+')';
		})()`, &dbg))
		log.Printf("   현재 inputs: %s", dbg)
		return nil, fmt.Errorf("login failed, url: %s", curURL)
	}

	// ec_req_sid 추출
	sid := ""
	if idx := strings.Index(curURL, "ec_req_sid="); idx >= 0 {
		sid = curURL[idx+11:]
		if end := strings.IndexAny(sid, "&#"); end >= 0 {
			sid = sid[:end]
		}
	}
	log.Printf("   ec_req_sid: %s", sid)

	// 쿠키 저장
	var cookies []*network.Cookie
	chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		var err error
		cookies, err = network.GetCookies().Do(ctx)
		return err
	}))

	var ce []CookieEntry
	for _, c := range cookies {
		ce = append(ce, CookieEntry{
			Name:    c.Name,
			Value:   c.Value,
			Domain:  c.Domain,
			Path:    c.Path,
			Expires: c.Expires,
		})
	}
	log.Printf("   쿠키 %d개 저장", len(ce))
	return &SessionCache{Cookies: ce, EcReqSid: sid}, nil
}

func fetchApprovals(ctx context.Context, sess *SessionCache) ([]ApprovalItem, error) {
	loginURL := fmt.Sprintf("https://login%s.ecount.com", strings.ToLower(zone))
	hashPath := "menuType=MENUTREE_000007&menuSeq=MENUTREE_000007&groupSeq=MENUTREE_000039&prgId=C000007&depth=1"

	// 쿠키 설정
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
		for _, c := range sess.Cookies {
			exp := cdp.TimeSinceEpoch(time.Unix(int64(c.Expires), 0))
			err := network.SetCookie(c.Name, c.Value).
				WithDomain(c.Domain).
				WithPath(c.Path).
				WithExpires(&exp).
				Do(ctx)
			if err != nil {
				log.Printf("   cookie set err (%s): %v", c.Name, err)
			}
		}
		return nil
	}))
	if err != nil {
		return nil, fmt.Errorf("set cookies: %v", err)
	}

	erpURL := loginURL + fmt.Sprintf("/ec5/view/erp?w_flag=1&ec_req_sid=%s", sess.EcReqSid)
	log.Printf("   ERP URL: %s", erpURL)

	if err := chromedp.Run(ctx, chromedp.Navigate(erpURL)); err != nil {
		return nil, fmt.Errorf("navigate ERP: %v", err)
	}
	chromedp.Run(ctx, chromedp.Sleep(3*time.Second))

	var curURL string
	chromedp.Run(ctx, chromedp.Location(&curURL))
	log.Printf("   ERP 접속 후: %s", curURL)

	if !strings.Contains(curURL, "logincc.ecount.com/ec5") {
		return nil, fmt.Errorf("session expired, redirected to: %s", curURL)
	}

	// hash 이동
	var r string
	chromedp.Run(ctx,
		chromedp.Evaluate(fmt.Sprintf(`location.hash='%s'`, hashPath), &r),
		chromedp.Sleep(15*time.Second),
	)

	// DOM 추출
	extractJS := `(function(){
		var dateRe=/^\d{4}\/\d{2}\/\d{2}/;
		var rows=Array.from(document.querySelectorAll('tr')).filter(function(r){
			var tds=r.querySelectorAll('td');
			return tds.length>=6&&dateRe.test((tds[1]||{textContent:''}).textContent.trim());
		});
		return JSON.stringify(rows.map(function(r){
			var tds=Array.from(r.querySelectorAll('td')).map(function(t){return t.textContent.trim();});
			return {date_no:tds[1]||'',title:tds[2]||'',doc_type:tds[3]||'',drafter:tds[4]||'',approver:tds[5]||'',status:tds[6]||''};
		}));
	})()`

	var itemsJSON string
	chromedp.Run(ctx, chromedp.Evaluate(extractJS, &itemsJSON))
	clean := itemsJSON
	if strings.HasPrefix(clean, `"`) {
		json.Unmarshal([]byte(clean), &clean)
	}

	var all []ApprovalItem
	if err := json.Unmarshal([]byte(clean), &all); err != nil {
		return nil, fmt.Errorf("unmarshal: %v (raw: %.100s)", err, clean)
	}
	return all, nil
}



func newChromedpCtx() (context.Context, context.CancelFunc) {
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", true),
		chromedp.Flag("no-sandbox", true),
		chromedp.Flag("disable-gpu", true),
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.UserAgent("Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"),
	)
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	ctx, ctxCancel := chromedp.NewContext(allocCtx)
	ctx, timeoutCancel := context.WithTimeout(ctx, 120*time.Second)
	return ctx, func() { timeoutCancel(); ctxCancel(); allocCancel() }
}

func main() {
	cfg := loadConfig()

	log.Println("=== Ecount 결재 모니터 ===")

	ctx, cancel := newChromedpCtx()
	defer cancel()

	// 세션 로드 or 로그인
	sess := loadSession()
	if sess == nil {
		log.Println(">> 로그인 시도...")
		var err error
		sess, err = doLogin(ctx)
		if err != nil {
			log.Fatalf("로그인 실패: %v", err)
		}
		saveSession(sess)
		log.Println("   세션 저장 완료")
	} else {
		log.Printf(">> 저장된 세션 사용 (저장시각: %s)", sess.SavedAt.Format("01/02 15:04"))
	}

	// 결재 목록 조회
	log.Println(">> 결재 목록 조회...")
	allItems, err := fetchApprovals(ctx, sess)
	if err != nil {
		// 세션 만료 가능성 → 재로그인
		if strings.Contains(err.Error(), "session expired") {
			log.Println("   세션 만료 → 재로그인...")
			os.Remove(sessionPath())
			ctx2, cancel2 := newChromedpCtx()
			defer cancel2()
			sess, err = doLogin(ctx2)
			if err != nil {
				log.Fatalf("재로그인 실패: %v", err)
			}
			saveSession(sess)
			allItems, err = fetchApprovals(ctx2, sess)
		}
		if err != nil {
			log.Fatalf("조회 실패: %v", err)
		}
	}
	log.Printf("   전체 %d건 수신", len(allItems))

	// 필터: 수신참조 아닌 것 + 진행중
	SOOSIN := "\uc218\uc2e0\ucc38\uc870"   // 수신참조
	JINHAENG := "\uc9c4\ud589\uc911"        // 진행중

	var pending []ApprovalItem
	for _, it := range allItems {
		isCC := it.Approver == SOOSIN
		isActive := strings.Contains(it.Status, JINHAENG)
		if !isCC && isActive {
			if cfg.ApproverFilter == "" || strings.Contains(it.Approver, cfg.ApproverFilter) {
				pending = append(pending, it)
			}
		}
	}

	fmt.Printf("\n==========================================\n")
	fmt.Printf("결재 대기 (수신참조 제외, 진행중): %d건\n", len(pending))
	fmt.Printf("==========================================\n\n")

	if len(pending) == 0 {
		fmt.Println("[OK] 결재 대기 없음")
	} else {
		fmt.Println("[결재 대기]")
		for i, it := range pending {
			fmt.Printf("  %d. [%s] %s\n      기안자: %s | 결재자컬럼: %s\n",
				i+1, it.DateNo, it.Title, it.Drafter, it.Approver)
		}
	}

	// 컬럼 구조 확인
	fmt.Println("\n[전체 목록 상위 5건]")
	lim := len(allItems); if lim > 5 { lim = 5 }
	for i := 0; i < lim; i++ {
		it := allItems[i]
		fmt.Printf("  %2d. 결재자=%-12s 상태=%-8s | %s\n", i+1, it.Approver, it.Status, it.Title)
	}

	// 신규 건 감지 → 텔레그램
	state := loadState()
	seenSet := make(map[string]bool)
	for _, d := range state.SeenDateNos { seenSet[d] = true }

	var newItems []ApprovalItem
	for _, it := range pending {
		if !seenSet[it.DateNo] { newItems = append(newItems, it) }
	}

	var dateNos []string
	for _, it := range pending { dateNos = append(dateNos, it.DateNo) }
	saveState(State{SeenDateNos: dateNos})

	if len(newItems) == 0 {
		fmt.Println("\n[신규 없음]")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📋 <b>결재 요청 %d건</b>\n\n", len(newItems)))
	for i, it := range newItems {
		if i >= 5 { sb.WriteString(fmt.Sprintf("... 외 %d건\n", len(newItems)-5)); break }
		sb.WriteString(fmt.Sprintf("• <b>%s</b>\n  📅 %s | 👤 %s\n\n", it.Title, it.DateNo, it.Drafter))
	}
	msg := sb.String()
	fmt.Printf("\n[신규 %d건 텔레그램 발송]\n%s\n", len(newItems), msg)

	if cfg.TelegramBotToken != "" {
		if err := sendTelegram(cfg.TelegramBotToken, tgChatID, msg); err != nil {
			log.Printf("텔레그램 실패: %v", err)
		} else {
			log.Println("텔레그램 발송 완료!")
		}
	}
}