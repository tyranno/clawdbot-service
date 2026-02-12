package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/Microsoft/go-winio"
)

// Idle time from helper (via Named Pipe)
var (
	currentIdleTime   time.Duration
	currentIdleMu     sync.RWMutex
	idleHelperRunning bool
)

// GetIdleTime returns the user idle duration from the helper
func GetIdleTime() time.Duration {
	currentIdleMu.RLock()
	defer currentIdleMu.RUnlock()

	if idleHelperRunning {
		return currentIdleTime
	}
	// fallback
	return getIdleTimeFromQueryUser()
}

// StartIdlePipeServer starts Named Pipe server to receive idle time from helper
func StartIdlePipeServer(ctx context.Context) {
	pipePath := `\\.\pipe\openclaw-idle`

	// Security descriptor that allows Everyone to connect
	// SDDL: D:(A;;GA;;;WD) = DACL: Allow Generic All to Everyone (World)
	pipeConfig := &winio.PipeConfig{
		SecurityDescriptor: "D:(A;;GA;;;WD)",
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Create pipe listener with permissive security
		listener, err := winio.ListenPipe(pipePath, pipeConfig)
		if err != nil {
			log.Printf("[IdlePipe] Listen error: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}

		log.Printf("[IdlePipe] Server started on %s", pipePath)

		// Close listener when context is cancelled
		go func() {
			<-ctx.Done()
			listener.Close()
		}()

		for {
			conn, err := listener.Accept()
			if err != nil {
				if ctx.Err() != nil {
					// Context cancelled, normal shutdown
					return
				}
				log.Printf("[IdlePipe] Accept error: %v", err)
				break
			}

			go handleIdleConnection(ctx, conn)
		}

		listener.Close()
	}
}

func handleIdleConnection(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	log.Printf("[IdlePipe] Helper connected")

	currentIdleMu.Lock()
	idleHelperRunning = true
	currentIdleMu.Unlock()

	defer func() {
		currentIdleMu.Lock()
		idleHelperRunning = false
		currentIdleMu.Unlock()
		log.Printf("[IdlePipe] Helper disconnected")
	}()

	reader := bufio.NewReader(conn)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}

		var idleSec int64
		fmt.Sscanf(strings.TrimSpace(line), "%d", &idleSec)

		currentIdleMu.Lock()
		currentIdleTime = time.Duration(idleSec) * time.Second
		currentIdleMu.Unlock()
	}
}

// getIdleTimeFromQueryUser is fallback when idle-detector helper is not running
func getIdleTimeFromQueryUser() time.Duration {
	out, err := exec.Command("cmd", "/c", "query user").CombinedOutput()
	if err != nil {
		if len(out) == 0 {
			return 0
		}
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if strings.Contains(line, "USERNAME") || strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, "console") {
			return parseIdleFromQueryUser(line)
		}
	}
	return 0
}

func parseIdleFromQueryUser(line string) time.Duration {
	fields := strings.Fields(line)
	for i, f := range fields {
		lf := strings.ToLower(f)
		if lf == "active" || lf == "활성" || strings.Contains(lf, "ȰƼ") {
			if i+1 < len(fields) {
				idle := fields[i+1]
				return parseIdleDuration(idle)
			}
		}
	}
	
	for _, f := range fields {
		if f == "none" || f == "." || f == "없음" {
			return 0
		}
		if d := parseIdleDuration(f); d > 0 {
			return d
		}
	}
	return 0
}

func parseIdleDuration(s string) time.Duration {
	s = strings.TrimSpace(s)
	if s == "none" || s == "." || s == "없음" {
		return 0
	}

	var days, hours, mins int

	if strings.Contains(s, "+") {
		parts := strings.SplitN(s, "+", 2)
		fmt.Sscanf(parts[0], "%d", &days)
		s = parts[1]
	}

	if strings.Contains(s, ":") {
		fmt.Sscanf(s, "%d:%d", &hours, &mins)
	} else {
		fmt.Sscanf(s, "%d", &mins)
	}

	return time.Duration(days)*24*time.Hour + time.Duration(hours)*time.Hour + time.Duration(mins)*time.Minute
}

// Notion config - loaded from files
const (
	notionVersion       = "2022-06-28"
	worktimeDir         = `C:\Users\lab\업무시간기록`
	worktimeFile        = `C:\Users\lab\업무시간기록\worktime.txt`
	absenceDetect       = 10 * time.Minute  // 자리비움 감지 시작
	absenceRecordMin    = 2 * time.Hour     // 이 이상이어야 자리비움으로 기록
	autoClockOut        = 22                // 22시 자동 퇴근
	notionAPIKeyFile    = `C:\Users\lab\.openclaw\notion-api-key.txt`
	notionParentIDFile  = `C:\Users\lab\.openclaw\notion-parent-id.txt`
)

var (
	notionAPIKey   string
	notionParentID string
)

func loadNotionConfig() error {
	// Load API key
	data, err := os.ReadFile(notionAPIKeyFile)
	if err != nil {
		return fmt.Errorf("read notion API key: %w", err)
	}
	notionAPIKey = strings.TrimSpace(string(data))

	// Load parent ID
	data, err = os.ReadFile(notionParentIDFile)
	if err != nil {
		return fmt.Errorf("read notion parent ID: %w", err)
	}
	notionParentID = strings.TrimSpace(string(data))

	if notionAPIKey == "" || notionParentID == "" {
		return fmt.Errorf("notion config is empty")
	}
	log.Printf("[WorkTime] Notion config loaded")
	return nil
}

type WorkState int

const (
	StateNone        WorkState = iota // 미출근
	StateWorking                      // 근무중
	StatePendingAway                  // 자리비움 감지 (아직 미기록)
	StateAway                         // 자리비움 (2시간 이상, 기록됨)
	StateLunch                        // 점심
	StateClockOut                     // 퇴근
)

func (s WorkState) String() string {
	switch s {
	case StateNone:
		return "미출근"
	case StateWorking:
		return "근무중"
	case StatePendingAway:
		return "자리비움감지"
	case StateAway:
		return "자리비움"
	case StateLunch:
		return "점심"
	case StateClockOut:
		return "퇴근"
	default:
		return "알수없음"
	}
}

type WorkEvent struct {
	Type string    `json:"type"` // 출근, 퇴근, 자리비움시작, 자리비움종료, 점심시작, 점심종료
	Time time.Time `json:"time"`
}

type WorkTimeMonitor struct {
	mu              sync.Mutex
	state           WorkState
	today           string // YYYY-MM-DD
	clockInTime     time.Time
	awayStartTime   time.Time
	lastActiveTime  time.Time // 마지막 활동 시간
	events          []WorkEvent
}

func NewWorkTimeMonitor() *WorkTimeMonitor {
	// Load notion config from files
	if err := loadNotionConfig(); err != nil {
		log.Printf("[WorkTime] Warning: %v (Notion sync disabled)", err)
	}

	m := &WorkTimeMonitor{
		state: StateNone,
	}
	m.loadTodayFromFile()
	return m
}

// loadTodayFromFile reads existing worktime.txt and restores today's state
func (m *WorkTimeMonitor) loadTodayFromFile() {
	today := time.Now().Format("2006-01-02")
	m.today = today

	content, err := os.ReadFile(worktimeFile)
	if err != nil {
		return
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, today) {
			continue
		}
		// Skip summary lines
		if strings.HasPrefix(line, "===") {
			continue
		}

		// Parse: "이벤트타입 - 2026-02-03 HH:mm:ss"
		parts := strings.SplitN(line, " - ", 2)
		if len(parts) != 2 {
			continue
		}
		eventType := strings.TrimSpace(parts[0])
		timeStr := strings.TrimSpace(parts[1])
		t, err := time.ParseInLocation("2006-01-02 15:04:05", timeStr, time.Local)
		if err != nil {
			continue
		}

		evt := WorkEvent{Type: eventType, Time: t}
		m.events = append(m.events, evt)

		switch eventType {
		case "출근":
			m.clockInTime = t
			m.lastActiveTime = t
			m.state = StateWorking
		case "퇴근":
			m.state = StateClockOut
		case "자리비움시작":
			m.state = StateAway
			m.awayStartTime = t
		case "자리비움종료":
			m.state = StateWorking
			m.lastActiveTime = t
		}
	}

	if m.state != StateNone {
		log.Printf("[WorkTime] Restored today's state from file: %s, %d events, clock-in: %s",
			m.state, len(m.events), m.clockInTime.Format("15:04:05"))
		// Note: Do NOT sync to notion here. The first tick() will handle state
		// transitions (e.g., ClockOut→Away conversion) and sync after that.
		// Syncing here causes a race condition where "Done" status can overwrite
		// the "Working" status set by convertClockOutToAway.
	}
}

// StartWorkTimeMonitor runs the main monitoring loop
func StartWorkTimeMonitor(ctx context.Context) {
	m := NewWorkTimeMonitor()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	log.Println("[WorkTime] Monitor started")

	// Initial check
	m.tick()

	for {
		select {
		case <-ctx.Done():
			log.Println("[WorkTime] Monitor stopping")
			// 퇴근 처리 → 마지막 활동 시간 사용
			m.mu.Lock()
			if m.state == StateWorking || m.state == StateAway || m.state == StatePendingAway {
				clockOutTime := m.lastActiveTime
				if clockOutTime.IsZero() || clockOutTime.Before(m.clockInTime) {
					clockOutTime = time.Now()
				}
				m.doClockOut(clockOutTime, "서비스종료")
			}
			m.mu.Unlock()
			return
		case <-ticker.C:
			m.tick()
		}
	}
}

func (m *WorkTimeMonitor) tick() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	today := now.Format("2006-01-02")
	idle := GetIdleTime()

	// 날짜 변경 체크
	if m.today != today {
		// 이전 날 마무리
		if m.state == StateWorking || m.state == StateAway {
			m.doClockOut(now, "날짜변경")
		}
		m.today = today
		m.state = StateNone
		m.events = nil
		m.cleanOldLogs()
	}

	isLunchTime := isLunch(now)
	isActive := idle < 2*time.Minute // 2분 이내 활동 = 활동중

	// 활동 중이면 마지막 활동 시간 갱신
	if isActive && m.state != StateNone && m.state != StateClockOut {
		m.lastActiveTime = now
	}

	switch m.state {
	case StateNone:
		// 첫 활동 감지 → 출근
		if isActive && now.Hour() >= 6 {
			m.doClockIn(now)
		}

	case StateWorking:
		// 22시 자동 퇴근 → 마지막 활동 시간을 퇴근 시간으로 사용
		if now.Hour() >= autoClockOut {
			clockOutTime := m.lastActiveTime
			if clockOutTime.IsZero() || clockOutTime.Before(m.clockInTime) {
				clockOutTime = now
			}
			m.doClockOut(clockOutTime, "자동퇴근")
			return
		}
		// 점심시간 진입
		if isLunchTime {
			m.doLunchStart(now)
			return
		}
		// 자리비움 감지 (아직 기록 안 함)
		if idle >= absenceDetect {
			awayStart := now.Add(-idle)
			m.doPendingAwayStart(awayStart)
		}

	case StatePendingAway:
		// 22시 자동 퇴근 → 자리비움 시작 시간(=마지막 활동)을 퇴근으로
		if now.Hour() >= autoClockOut {
			m.finalizePendingAway(now) // 2시간 이상이면 기록
			clockOutTime := m.awayStartTime
			if clockOutTime.IsZero() {
				clockOutTime = m.lastActiveTime
			}
			if clockOutTime.IsZero() {
				clockOutTime = now
			}
			m.doClockOut(clockOutTime, "자동퇴근")
			return
		}
		// 점심시간 진입 - pending away 취소하고 점심으로
		if isLunchTime {
			m.state = StateWorking // pending 취소
			m.awayStartTime = time.Time{}
			m.doLunchStart(now)
			return
		}
		// 활동 복귀
		if isActive {
			m.finalizePendingAway(now) // 2시간 이상이면 기록, 아니면 무시
		}

	case StateAway:
		// 22시 자동 퇴근 → 자리비움 시작을 퇴근으로 (doClockOut이 처리)
		if now.Hour() >= autoClockOut {
			m.doClockOut(now, "자동퇴근") // doClockOut이 awayStartTime 사용
			return
		}
		// 활동 복귀
		if isActive {
			m.doAwayEnd(now)
		}

	case StateLunch:
		// 점심 끝
		if !isLunchTime {
			m.doLunchEnd(now)
		}

	case StateClockOut:
		// 퇴근 후 다시 활동 → 퇴근 취소, 자리비움으로 변환
		if isActive && now.Hour() < autoClockOut {
			m.convertClockOutToAway(now)
		}
	}
}

func isLunch(t time.Time) bool {
	mins := t.Hour()*60 + t.Minute()
	return mins >= 11*60+20 && mins < 12*60+20
}

func (m *WorkTimeMonitor) doClockIn(t time.Time) {
	m.state = StateWorking
	m.clockInTime = t
	m.lastActiveTime = t
	evt := WorkEvent{Type: "출근", Time: t}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 출근: %s", t.Format("15:04:05"))
	m.recordEvent(evt)

	// 텔레그램 알림
	dayNames := []string{"일", "월", "화", "수", "목", "금", "토"}
	dow := dayNames[t.Weekday()]
	msg := fmt.Sprintf("🟢 <b>출근</b>\n📅 %s (%s) %s", t.Format("2006-01-02"), dow, t.Format("15:04"))
	go sendTelegramNotification(msg)
}

func (m *WorkTimeMonitor) doClockOut(t time.Time, reason string) {
	actualClockOut := t

	// 자리비움 중이었으면: 자리비움 시작 시간을 실제 퇴근 시간으로 간주
	if m.state == StateAway && !m.awayStartTime.IsZero() {
		actualClockOut = m.awayStartTime
		log.Printf("[WorkTime] 자리비움 중 퇴근 → 자리비움 시작시간(%s)을 퇴근시간으로 사용",
			actualClockOut.Format("15:04:05"))
		// 자리비움 시작 이벤트 제거 (퇴근으로 대체)
		for i := len(m.events) - 1; i >= 0; i-- {
			if m.events[i].Type == "자리비움시작" && m.events[i].Time.Equal(m.awayStartTime) {
				m.events = append(m.events[:i], m.events[i+1:]...)
				break
			}
		}
	}

	m.state = StateClockOut
	evt := WorkEvent{Type: "퇴근", Time: actualClockOut}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 퇴근 (%s): %s", reason, actualClockOut.Format("15:04:05"))
	m.recordEvent(evt)
	m.recordDaySummary(actualClockOut)

	// 텔레그램 알림
	dur := m.calcWorkDuration()
	hours := int(dur.Hours())
	mins := int(dur.Minutes()) % 60
	msg := fmt.Sprintf("🔴 <b>퇴근</b> (%s)\n📅 %s %s\n⏱ 근무시간: %d시간 %d분\n\n오늘도 수고하셨습니다! 🦖",
		reason, actualClockOut.Format("2006-01-02"), actualClockOut.Format("15:04"), hours, mins)
	go sendTelegramNotification(msg)
}

func (m *WorkTimeMonitor) doAwayStart(t time.Time) {
	// 점심시간 겹침 분할 처리
	if isLunch(t) {
		// 점심시간 중 자리비움 시작이면 점심 후로 조정
		lunchEnd := time.Date(t.Year(), t.Month(), t.Day(), 12, 20, 0, 0, t.Location())
		if time.Now().Before(lunchEnd) {
			m.state = StateLunch
			lunchStart := time.Date(t.Year(), t.Month(), t.Day(), 11, 20, 0, 0, t.Location())
			evt := WorkEvent{Type: "점심시작", Time: lunchStart}
			m.events = append(m.events, evt)
			m.recordEvent(evt)
			return
		}
		t = lunchEnd
	}

	m.state = StateAway
	m.awayStartTime = t
	evt := WorkEvent{Type: "자리비움시작", Time: t}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 자리비움시작: %s", t.Format("15:04:05"))
	m.recordEvent(evt)
}

func (m *WorkTimeMonitor) doAwayEnd(t time.Time) {
	m.state = StateWorking
	evt := WorkEvent{Type: "자리비움종료", Time: t}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 자리비움종료: %s", t.Format("15:04:05"))
	m.recordEvent(evt)
}

func (m *WorkTimeMonitor) doPendingAwayStart(t time.Time) {
	m.state = StatePendingAway
	m.awayStartTime = t
	log.Printf("[WorkTime] 자리비움 감지 (미기록): %s", t.Format("15:04:05"))
}

func (m *WorkTimeMonitor) finalizePendingAway(returnTime time.Time) {
	awayDuration := returnTime.Sub(m.awayStartTime)

	if awayDuration >= absenceRecordMin {
		// 2시간 이상: 자리비움으로 기록
		log.Printf("[WorkTime] 자리비움 확정 (%.1f시간): %s ~ %s",
			awayDuration.Hours(), m.awayStartTime.Format("15:04:05"), returnTime.Format("15:04:05"))

		// 자리비움 시작 기록
		startEvt := WorkEvent{Type: "자리비움시작", Time: m.awayStartTime}
		m.events = append(m.events, startEvt)
		m.recordEventToFile(startEvt)

		// 자리비움 종료 기록
		endEvt := WorkEvent{Type: "자리비움종료", Time: returnTime}
		m.events = append(m.events, endEvt)
		m.recordEventToFile(endEvt)

		go m.syncToNotion()
	} else {
		// 2시간 미만: 무시
		log.Printf("[WorkTime] 자리비움 무시 (%.0f분 < 2시간): %s ~ %s",
			awayDuration.Minutes(), m.awayStartTime.Format("15:04:05"), returnTime.Format("15:04:05"))
	}

	m.state = StateWorking
	m.awayStartTime = time.Time{}
}

func (m *WorkTimeMonitor) doLunchStart(t time.Time) {
	m.state = StateLunch
	lunchStart := time.Date(t.Year(), t.Month(), t.Day(), 11, 20, 0, 0, t.Location())
	evt := WorkEvent{Type: "점심시작", Time: lunchStart}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 점심시작: %s", lunchStart.Format("15:04:05"))
	// 파일에는 기록하지 않고 노션만 동기화
	go m.syncToNotion()
}

func (m *WorkTimeMonitor) doLunchEnd(t time.Time) {
	m.state = StateWorking
	lunchEnd := time.Date(t.Year(), t.Month(), t.Day(), 12, 20, 0, 0, t.Location())
	evt := WorkEvent{Type: "점심종료", Time: lunchEnd}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 점심종료: %s", lunchEnd.Format("15:04:05"))
	go m.syncToNotion()
}

func (m *WorkTimeMonitor) convertClockOutToAway(t time.Time) {
	// 마지막 퇴근 이벤트를 찾아서 자리비움시작으로 변환
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].Type == "퇴근" {
			awayStart := m.events[i].Time
			m.events[i].Type = "자리비움시작"
			log.Printf("[WorkTime] 퇴근→자리비움 변환: %s", awayStart.Format("15:04:05"))
			// 요약도 제거
			if i+1 < len(m.events) {
				m.events = m.events[:i+1]
			}
			break
		}
	}

	// 자리비움 종료 추가
	m.state = StateWorking
	evt := WorkEvent{Type: "자리비움종료", Time: t}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 복귀: %s", t.Format("15:04:05"))

	// 파일/노션 전체 재기록
	m.rewriteAll()
}

// recordEvent appends a single event to file and notion
func (m *WorkTimeMonitor) recordEvent(evt WorkEvent) {
	m.recordEventToFile(evt)
	go m.syncToNotion()
}

func (m *WorkTimeMonitor) recordEventToFile(evt WorkEvent) {
	os.MkdirAll(worktimeDir, 0755)

	// 출퇴근은 기존 형식, 자리비움 등은 새 형식
	var line string
	switch evt.Type {
	case "출근":
		line = fmt.Sprintf("출근 - %s\n", evt.Time.Format("2006-01-02 15:04:05"))
	case "퇴근":
		line = fmt.Sprintf("퇴근 - %s\n", evt.Time.Format("2006-01-02 15:04:05"))
	default:
		line = fmt.Sprintf("%s - %s\n", evt.Type, evt.Time.Format("2006-01-02 15:04:05"))
	}

	f, err := os.OpenFile(worktimeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.Printf("[WorkTime] File write error: %v", err)
		return
	}
	defer f.Close()
	f.WriteString(line)
}

func (m *WorkTimeMonitor) recordDaySummary(t time.Time) {
	if m.clockInTime.IsZero() {
		return
	}
	dur := m.calcWorkDuration()
	hours := int(dur.Hours())
	mins := int(dur.Minutes()) % 60
	summary := fmt.Sprintf("=== %s 근무시간: %d시간 %d분===\n", m.today, hours, mins)

	f, err := os.OpenFile(worktimeFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	f.WriteString(summary)
}

func (m *WorkTimeMonitor) calcWorkDuration() time.Duration {
	if m.clockInTime.IsZero() {
		return 0
	}
	var lastTime time.Time
	for _, e := range m.events {
		if e.Type == "퇴근" {
			lastTime = e.Time
		}
	}
	if lastTime.IsZero() {
		lastTime = time.Now()
	}
	total := lastTime.Sub(m.clockInTime)

	// 자리비움 시간 제외
	var awayDur time.Duration
	var awayStart time.Time
	for _, e := range m.events {
		switch e.Type {
		case "자리비움시작", "점심시작":
			awayStart = e.Time
		case "자리비움종료", "점심종료":
			if !awayStart.IsZero() {
				awayDur += e.Time.Sub(awayStart)
				awayStart = time.Time{}
			}
		}
	}

	// 점심 1시간 자동 제외 (점심 이벤트가 없어도)
	hasLunchEvent := false
	for _, e := range m.events {
		if e.Type == "점심시작" {
			hasLunchEvent = true
			break
		}
	}
	if !hasLunchEvent && lastTime.Hour() >= 12 && m.clockInTime.Hour() < 12 {
		awayDur += 1 * time.Hour
	}

	return total - awayDur
}

// rewriteAll rewrites the entire file for today (used after clock-out→away conversion)
func (m *WorkTimeMonitor) rewriteAll() {
	// Read existing file, remove today's lines, rewrite
	content, _ := os.ReadFile(worktimeFile)
	lines := strings.Split(string(content), "\n")

	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.Contains(line, m.today) {
			continue // remove today's entries
		}
		kept = append(kept, line)
	}

	// Add today's events (점심 제외)
	for _, evt := range m.events {
		if evt.Type == "점심시작" || evt.Type == "점심종료" {
			continue
		}
		switch evt.Type {
		case "출근":
			kept = append(kept, fmt.Sprintf("출근 - %s", evt.Time.Format("2006-01-02 15:04:05")))
		case "퇴근":
			kept = append(kept, fmt.Sprintf("퇴근 - %s", evt.Time.Format("2006-01-02 15:04:05")))
		default:
			kept = append(kept, fmt.Sprintf("%s - %s", evt.Type, evt.Time.Format("2006-01-02 15:04:05")))
		}
	}

	os.WriteFile(worktimeFile, []byte(strings.Join(kept, "\n")+"\n"), 0644)
	go m.syncToNotion()
}

func (m *WorkTimeMonitor) cleanOldLogs() {
	content, err := os.ReadFile(worktimeFile)
	if err != nil {
		return
	}
	cutoff := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	lines := strings.Split(string(content), "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		// 날짜를 찾아서 7일 이전이면 제거
		skip := false
		for i := 0; i <= len(line)-10; i++ {
			if len(line) >= i+10 && line[i] >= '2' && line[i] <= '2' {
				dateStr := line[i : i+10]
				if len(dateStr) == 10 && dateStr[4] == '-' && dateStr[7] == '-' {
					if dateStr < cutoff {
						skip = true
					}
					break
				}
			}
		}
		if !skip {
			kept = append(kept, line)
		}
	}
	os.WriteFile(worktimeFile, []byte(strings.Join(kept, "\n")+"\n"), 0644)
}

// === Notion API (Database) ===

const notionDBIDFile = `C:\Users\lab\.openclaw\worktime-notion-db.txt`

var notionDBID string // 근무시간 데이터베이스 ID

func (m *WorkTimeMonitor) syncToNotion() {
	if err := m.loadNotionDBID(); err != nil {
		log.Printf("[WorkTime] Notion DB ID error: %v", err)
		return
	}
	if err := m.upsertNotionRow(); err != nil {
		log.Printf("[WorkTime] Notion upsert error: %v", err)
	}
}

func notionRequest(method, path string, body interface{}) (map[string]interface{}, error) {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, "https://api.notion.com"+path, reader)
	req.Header.Set("Authorization", "Bearer "+notionAPIKey)
	req.Header.Set("Notion-Version", notionVersion)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var result map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&result)
	if resp.StatusCode >= 400 {
		return result, fmt.Errorf("notion %d: %v", resp.StatusCode, result)
	}
	return result, nil
}

func (m *WorkTimeMonitor) loadNotionDBID() error {
	if notionDBID != "" {
		return nil
	}

	// Read DB ID from file (created by Node.js setup script)
	data, err := os.ReadFile(notionDBIDFile)
	if err != nil {
		return fmt.Errorf("read DB ID file: %w (run notion-create-worktime-db.js first)", err)
	}
	notionDBID = strings.TrimSpace(string(data))
	if notionDBID == "" {
		return fmt.Errorf("empty DB ID in file")
	}
	log.Printf("[WorkTime] Loaded Notion DB ID: %s", notionDBID)
	return nil
}

func (m *WorkTimeMonitor) findTodayRow() (string, error) {
	body := map[string]interface{}{
		"filter": map[string]interface{}{
			"property": "Date",
			"title": map[string]interface{}{
				"equals": m.today,
			},
		},
	}
	result, err := notionRequest("POST", fmt.Sprintf("/v1/databases/%s/query", notionDBID), body)
	if err != nil {
		return "", err
	}
	results, ok := result["results"].([]interface{})
	if !ok || len(results) == 0 {
		return "", fmt.Errorf("not found")
	}
	page := results[0].(map[string]interface{})
	if id, ok := page["id"].(string); ok {
		return id, nil
	}
	return "", fmt.Errorf("no id")
}

func (m *WorkTimeMonitor) upsertNotionRow() error {
	// Build properties
	clockIn := ""
	clockOut := ""
	for _, evt := range m.events {
		switch evt.Type {
		case "출근":
			clockIn = evt.Time.Format("15:04:05")
		case "퇴근":
			clockOut = evt.Time.Format("15:04:05")
		}
	}

	dur := m.calcWorkDuration()
	hours := int(dur.Hours())
	mins := int(dur.Minutes()) % 60
	workDur := fmt.Sprintf("%d시간 %d분", hours, mins)

	// Calculate day of week
	t, _ := time.Parse("2006-01-02", m.today)
	dayNames := []string{"일", "월", "화", "수", "목", "금", "토"}
	dayOfWeek := dayNames[t.Weekday()]

	stateLabel := "Working"
	switch m.state {
	case StateClockOut:
		stateLabel = "Done"
	case StateAway:
		stateLabel = "Away"
	case StateLunch:
		stateLabel = "Lunch"
	}

	props := map[string]interface{}{
		"Date": map[string]interface{}{
			"title": []map[string]interface{}{
				{"text": map[string]interface{}{"content": m.today}},
			},
		},
		"WorkDate": map[string]interface{}{
			"date": map[string]interface{}{"start": m.today},
		},
		"DayOfWeek": map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{"text": map[string]interface{}{"content": dayOfWeek}},
			},
		},
		"ClockIn": map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{"text": map[string]interface{}{"content": clockIn}},
			},
		},
		"ClockOut": map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{"text": map[string]interface{}{"content": clockOut}},
			},
		},
		"WorkHours": map[string]interface{}{
			"rich_text": []map[string]interface{}{
				{"text": map[string]interface{}{"content": workDur}},
			},
		},
		"Status": map[string]interface{}{
			"select": map[string]interface{}{"name": stateLabel},
		},
	}

	// Try to find existing row
	rowID, err := m.findTodayRow()
	if err == nil && rowID != "" {
		// Update existing
		_, err = notionRequest("PATCH", fmt.Sprintf("/v1/pages/%s", rowID), map[string]interface{}{
			"properties": props,
		})
		if err != nil {
			return fmt.Errorf("update row: %w", err)
		}
		log.Printf("[WorkTime] Notion row updated: %s", m.today)
		return nil
	}

	// Create new row
	_, err = notionRequest("POST", "/v1/pages", map[string]interface{}{
		"parent":     map[string]interface{}{"database_id": notionDBID},
		"properties": props,
	})
	if err != nil {
		return fmt.Errorf("create row: %w", err)
	}
	log.Printf("[WorkTime] Notion row created: %s", m.today)
	return nil
}
