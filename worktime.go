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
	"path/filepath"
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

	// 활동 재개 이벤트 채널: idle이 2분 이상 → 0 근처로 떨어질 때 마우스 첫 움직임 시간 전송
	activityResumedCh = make(chan time.Time, 20)
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
	var prevIdleSec int64 = 999999 // 초기값: 매우 긴 idle

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

		// 활동 재개 감지: idle이 2분 이상에서 갑자기 5초 미만으로 떨어짐
		// = 마우스/키보드가 처음 감지된 순간
		if prevIdleSec >= 120 && idleSec < 5 {
			activityTime := time.Now().Add(-time.Duration(idleSec) * time.Second)
			log.Printf("[IdlePipe] 활동 재개 감지: %s (이전idle=%ds → 현재=%ds)",
				activityTime.Format("15:04:05"), prevIdleSec, idleSec)
			select {
			case activityResumedCh <- activityTime:
			default: // 채널 가득 찼으면 스킵
			}
		}

		prevIdleSec = idleSec
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
	absenceDetect       = 10 * time.Minute  // 자리비움 감지 시작
	absenceRecordMin    = 2 * time.Hour     // 이 이상이어야 자리비움으로 기록
	autoClockOut        = 22                // 22시 자동 퇴근 (근무중 상태 fallback)
	awayAutoClockOut    = 20                // 20시 자동 퇴근 (자리비움 상태 fallback)
	longAbsenceClockOut = 60 * time.Minute // 자리비움 1시간 이상 (16시 이후) → 즉시 퇴근 처리
)

var (
	worktimeDir        = filepath.Join(userHome, "업무시간기록")
	worktimeFile       = filepath.Join(userHome, "업무시간기록", "worktime2.txt")
	notionAPIKeyFile   = filepath.Join(userHome, ".openclaw", "notion-api-key.txt")
	notionParentIDFile = filepath.Join(userHome, ".openclaw", "notion-parent-id.txt")
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

// 출근 판정 파라미터
const (
	clockInStreakRequired = 3 // 연속 활동 N tick (= N분) 이상이어야 출근 판정
	sleepCooldownMins     = 5 // 슬립/재시작 감지 후 출근 억제 시간 (분)
)

type WorkTimeMonitor struct {
	mu                   sync.Mutex
	state                WorkState
	today                string // YYYY-MM-DD
	clockInTime          time.Time
	clockOutTime         time.Time
	awayStartTime        time.Time
	lastActiveTime       time.Time // 마지막 실제 입력 시간 (now - idle)
	lastTickTime         time.Time // 슬립 감지용
	sleepUntil           time.Time // 이 시간 이전에는 출근 판정 억제
	convertClockOutUntil time.Time // 이 시간 이전에는 퇴근→자리비움 변환 억제 (재시작 직후 보호)
	clockOutFromFile     bool      // 파일에서 복원된 퇴근 기록 (외부 도구가 기록한 퇴근은 절대 변환하지 않음)
	activeStreak         int       // 연속 활동 카운터
	firstActiveTime      time.Time // streak 시작 시점의 실제 입력 시간 (출근 시간으로 사용)
	events               []WorkEvent
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
func parseWorktimeLine(line string) (string, time.Time, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "===") {
		return "", time.Time{}, false
	}

	parts := strings.SplitN(line, " - ", 2)
	if len(parts) != 2 {
		return "", time.Time{}, false
	}
	kind := strings.TrimSpace(parts[0])
	timeStr := strings.TrimSpace(parts[1])
	t, err := time.ParseInLocation("2006-01-02 15:04:05", timeStr, time.Local)
	if err != nil {
		return "", time.Time{}, false
	}
	return kind, t, true
}

func (m *WorkTimeMonitor) recomputeStateFromEvents() {
	m.state = StateNone
	m.clockInTime = time.Time{}
	m.clockOutTime = time.Time{}
	m.awayStartTime = time.Time{}
	m.lastActiveTime = time.Time{}
	m.clockOutFromFile = false

	for _, evt := range m.events {
		switch evt.Type {
		case "출근":
			if m.clockInTime.IsZero() {
				m.clockInTime = evt.Time
			}
			m.lastActiveTime = evt.Time
			m.state = StateWorking
		case "퇴근":
			if m.clockOutTime.IsZero() {
				m.clockOutTime = evt.Time
			}
			m.state = StateClockOut
			m.clockOutFromFile = true
		case "자리비움시작":
			m.state = StateAway
			m.awayStartTime = evt.Time
		case "자리비움종료":
			m.state = StateWorking
			m.lastActiveTime = evt.Time
		case "점심시작":
			m.state = StateLunch
		case "점심종료":
			m.state = StateWorking
		}
	}
}

func (m *WorkTimeMonitor) loadTodayFromFile() {
	today := time.Now().Format("2006-01-02")
	m.today = today

	content, err := os.ReadFile(worktimeFile)
	if err != nil {
		return
	}

	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		kind, t, ok := parseWorktimeLine(line)
		if !ok || !strings.Contains(t.Format("2006-01-02"), today) {
			continue
		}
		m.events = append(m.events, WorkEvent{Type: kind, Time: t})
	}

	m.recomputeStateFromEvents()

	if m.state != StateNone {
		log.Printf("[WorkTime] Restored today's state from file: %s, %d events, clock-in: %s",
			m.state, len(m.events), m.clockInTime.Format("15:04:05"))
	}
}

// StartWorkTimeMonitor runs the main monitoring loop
func StartWorkTimeMonitor(ctx context.Context) {
	m := NewWorkTimeMonitor()
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	log.Println("[WorkTime] Monitor started")

	// 서비스 시작 직후 출근 오판 방지: 재시작/부팅 후 N분간 출근 억제
	// 퇴근→자리비움 변환도 억제 (재시작 시 기존 퇴근 기록 보호)
	m.mu.Lock()
	m.lastTickTime = time.Now()
	m.sleepUntil = time.Now().Add(sleepCooldownMins * time.Minute)
	m.convertClockOutUntil = time.Now().Add(sleepCooldownMins * time.Minute)
	log.Printf("[WorkTime] 슬립 억제 설정: %s까지", m.sleepUntil.Format("15:04:05"))
	log.Printf("[WorkTime] 퇴근→자리비움 변환 억제: %s까지", m.convertClockOutUntil.Format("15:04:05"))
	m.mu.Unlock()

	// Initial check
	m.tick()

	for {
		select {
		case <-ctx.Done():
			log.Println("[WorkTime] Monitor stopping")
			m.mu.Lock()
			// 서비스종료 시 worktime2.txt에 퇴근 기록하지 않음
			// → 재시작 시 가짜 퇴근이 남아서 근무 중인데 퇴근 처리되는 문제 방지
			// 날짜변경 시에만 퇴근 처리 (오늘 기록 마감 필요)
			if m.state == StateWorking || m.state == StateAway || m.state == StatePendingAway {
				log.Printf("[WorkTime] 서비스종료 - 퇴근 미기록 (state=%s, lastActive=%s)",
					m.state, m.lastActiveTime.Format("15:04:05"))
			}
			m.mu.Unlock()
			return

		case actTime := <-activityResumedCh:
			// idle-detector에서 감지한 실제 마우스/키보드 첫 움직임 시간
			m.onActivityResumed(actTime)

		case <-ticker.C:
			m.tick()
		}
	}
}

// onActivityResumed: idle-detector에서 마우스/키보드 첫 움직임을 감지했을 때 즉시 호출
// 출근 시간 및 자리비움 종료 시간을 정확하게 기록하기 위한 이벤트 핸들러
func (m *WorkTimeMonitor) onActivityResumed(t time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	today := now.Format("2006-01-02")

	// 활동 시간이 오늘 날짜가 아니면 무시 (날짜 변경은 tick에서 처리)
	if t.Format("2006-01-02") != today {
		log.Printf("[WorkTime] 활동 재개 시간 날짜 불일치, 무시: %s", t.Format("2006-01-02 15:04:05"))
		return
	}

	// 마지막 실제 입력 시간 업데이트
	m.lastActiveTime = t

	log.Printf("[WorkTime] 활동 재개: %s (state=%s)", t.Format("15:04:05"), m.state)

	switch m.state {
	case StateNone:
		// 출근 판정은 tick에서 activeStreak로 처리 (즉시 출근 안 함)
		// 여기서는 lastActiveTime 업데이트만 함

	case StateAway:
		// 자리비움 종료 → 복귀
		m.doAwayEnd(now)

	case StatePendingAway:
		// 자리비움 감지 중이었으면 복귀 처리
		m.finalizePendingAway(now)

	case StateClockOut:
		// 퇴근 후 다시 활동 → 자리비움으로 변환
		// 재시작 직후에는 억제 (기존 퇴근 기록 보호)
		// 파일에서 복원된 퇴근(외부 도구 기록)은 절대 변환하지 않음
		if now.Hour() < autoClockOut && now.After(m.convertClockOutUntil) && !m.clockOutFromFile {
			m.convertClockOutToAway(now)
		} else if now.Before(m.convertClockOutUntil) {
			log.Printf("[WorkTime] 퇴근→자리비움 변환 억제 중 (재시작 보호, %s까지)", m.convertClockOutUntil.Format("15:04:05"))
		}
	// StateWorking, StateLunch는 lastActiveTime 업데이트만으로 충분
	}
}

func (m *WorkTimeMonitor) tick() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	today := now.Format("2006-01-02")
	idle := GetIdleTime()
	actualLastInput := now.Add(-idle)

	if !m.lastTickTime.IsZero() {
		tickGap := now.Sub(m.lastTickTime)
		if tickGap > 3*time.Minute {
			log.Printf("[WorkTime] 슬립 복귀 감지 (gap=%s) → %s까지 출근 억제",
				tickGap.Round(time.Second), now.Add(sleepCooldownMins*time.Minute).Format("15:04:05"))
			m.sleepUntil = now.Add(sleepCooldownMins * time.Minute)
			m.activeStreak = 0
			m.firstActiveTime = time.Time{}
		}
	}
	m.lastTickTime = now

	if m.today != today {
		m.today = today
		m.state = StateNone
		m.events = nil
		m.clockInTime = time.Time{}
		m.clockOutTime = time.Time{}
		m.awayStartTime = time.Time{}
		m.lastActiveTime = time.Time{}
		m.clockOutFromFile = false
		m.activeStreak = 0
		m.firstActiveTime = time.Time{}
		m.cleanOldLogs()
	}

	isActive := idle < 2*time.Minute
	if isActive {
		m.lastActiveTime = actualLastInput
	}

	// 이미 퇴근 기록이 있으면 더 이상 자동 판정하지 않음.
	if m.clockOutTime.IsZero() {
		suppressed := now.Before(m.sleepUntil)
		if m.clockInTime.IsZero() {
			if isActive && now.Hour() >= 6 && !suppressed {
				if m.activeStreak == 0 {
					m.firstActiveTime = actualLastInput
				}
				m.activeStreak++
				log.Printf("[WorkTime] 출근 후보 streak=%d/%d (첫활동=%s)", m.activeStreak, clockInStreakRequired, m.firstActiveTime.Format("15:04:05"))
				if m.activeStreak >= clockInStreakRequired {
					m.doClockIn(m.firstActiveTime)
					m.activeStreak = 0
					m.firstActiveTime = time.Time{}
				}
			} else {
				m.activeStreak = 0
				m.firstActiveTime = time.Time{}
			}
			return
		}

		switch m.state {
		case StateWorking:
			if now.Hour() >= autoClockOut {
				checkoutTime := actualLastInput
				if checkoutTime.IsZero() || checkoutTime.Before(m.clockInTime) {
					checkoutTime = now
				}
				m.doClockOut(checkoutTime, "자동퇴근")
				return
			}
			if isLunch(now) {
				m.doLunchStart(now)
				return
			}
			if idle >= absenceDetect {
				m.doPendingAwayStart(actualLastInput)
			}

		case StatePendingAway:
			if isActive {
				m.finalizePendingAway(now)
				return
			}
			if now.Hour() >= awayAutoClockOut && !m.awayStartTime.IsZero() {
				m.doClockOut(m.awayStartTime, "자동퇴근")
				return
			}
			if now.Hour() >= 16 && !m.awayStartTime.IsZero() && now.Sub(m.awayStartTime) >= longAbsenceClockOut {
				m.doClockOut(m.awayStartTime, "자리비움퇴근")
				return
			}
			if isLunch(now) {
				m.doLunchStart(now)
				return
			}

		case StateAway:
			if isActive {
				m.doAwayEnd(now)
			}
			if now.Hour() >= awayAutoClockOut && !m.awayStartTime.IsZero() {
				m.doClockOut(m.awayStartTime, "자동퇴근")
				return
			}

		case StateLunch:
			if !isLunch(now) {
				m.doLunchEnd(now)
			}
		case StateClockOut:
			return
		}
	}
}

func isLunch(t time.Time) bool {
	mins := t.Hour()*60 + t.Minute()
	return mins >= 11*60+20 && mins < 12*60+20
}

func (m *WorkTimeMonitor) doClockIn(t time.Time) {
	if !m.clockInTime.IsZero() || m.clockOutTime.IsZero() == false {
		log.Printf("[WorkTime] 출근 중복 방지: already clocked in/out today")
		return
	}

	m.state = StateWorking
	m.clockInTime = t
	m.lastActiveTime = t
	evt := WorkEvent{Type: "출근", Time: t}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 출근: %s", t.Format("15:04:05"))
	m.recordEvent(evt)

	dayNames := []string{"일", "월", "화", "수", "목", "금", "토"}
	dow := dayNames[t.Weekday()]
	msg := fmt.Sprintf("🟢 <b>출근</b>\n📅 %s (%s) %s", t.Format("2006-01-02"), dow, t.Format("15:04"))
	go sendTelegramNotification(msg)
	go sendVoiceChatNotification("🟢 출근", fmt.Sprintf("%s (%s) %s", t.Format("2006-01-02"), dow, t.Format("15:04")))
}

func (m *WorkTimeMonitor) doClockOut(t time.Time, reason string) {
	if !m.clockOutTime.IsZero() {
		log.Printf("[WorkTime] 퇴근 중복 방지: already clocked out (%s), 요청: %s", m.clockOutTime.Format("15:04:05"), reason)
		return
	}

	actualClockOut := t
	if actualClockOut.IsZero() {
		actualClockOut = time.Now()
	}

	m.state = StateClockOut
	m.clockOutTime = actualClockOut
	m.clockOutFromFile = false
	evt := WorkEvent{Type: "퇴근", Time: actualClockOut}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 퇴근 (%s): %s", reason, actualClockOut.Format("15:04:05"))
	m.recordEvent(evt)
	m.recordDaySummary(actualClockOut)

	if reason != "서비스종료" && reason != "날짜변경" {
		dur := m.calcWorkDuration()
		hours := int(dur.Hours())
		mins := int(dur.Minutes()) % 60
		msg := fmt.Sprintf("🔴 <b>퇴근</b> (%s)\n📅 %s %s\n⏱ 근무시간: %d시간 %d분\n\n오늘도 수고하셨습니다! 🦖",
			reason, actualClockOut.Format("2006-01-02"), actualClockOut.Format("15:04"), hours, mins)
		go sendTelegramNotification(msg)
		go sendVoiceChatNotification("🔴 퇴근", fmt.Sprintf("%s %s · 근무시간: %d시간 %d분",
			actualClockOut.Format("2006-01-02"), actualClockOut.Format("15:04"), hours, mins))
	} else {
		log.Printf("[WorkTime] 퇴근 알림 생략 (reason=%s)", reason)
	}
}

func (m *WorkTimeMonitor) doAwayStart(t time.Time) {
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
	if m.state == StatePendingAway || m.state == StateAway || m.state == StateClockOut {
		return
	}
	m.state = StatePendingAway
	m.awayStartTime = t
	log.Printf("[WorkTime] 자리비움 감지 (미기록): %s", t.Format("15:04:05"))
}

func (m *WorkTimeMonitor) finalizePendingAway(returnTime time.Time) {
	if m.awayStartTime.IsZero() {
		m.state = StateWorking
		return
	}

	awayDuration := returnTime.Sub(m.awayStartTime)
	if awayDuration < 0 {
		awayDuration = 0
	}

	if awayDuration >= absenceRecordMin {
		startEvt := WorkEvent{Type: "자리비움시작", Time: m.awayStartTime}
		endEvt := WorkEvent{Type: "자리비움종료", Time: returnTime}
		m.events = append(m.events, startEvt, endEvt)
		m.recordEventToFile(startEvt)
		m.recordEventToFile(endEvt)
		go m.syncToNotion()
		log.Printf("[WorkTime] 자리비움 확정 (%.1f시간): %s ~ %s", awayDuration.Hours(), m.awayStartTime.Format("15:04:05"), returnTime.Format("15:04:05"))
	} else {
		log.Printf("[WorkTime] 자리비움 무시 (%.0f분 < 2시간): %s ~ %s", awayDuration.Minutes(), m.awayStartTime.Format("15:04:05"), returnTime.Format("15:04:05"))
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
	m.recordEvent(evt)
}

func (m *WorkTimeMonitor) doLunchEnd(t time.Time) {
	m.state = StateWorking
	lunchEnd := time.Date(t.Year(), t.Month(), t.Day(), 12, 20, 0, 0, t.Location())
	evt := WorkEvent{Type: "점심종료", Time: lunchEnd}
	m.events = append(m.events, evt)
	log.Printf("[WorkTime] 점심종료: %s", lunchEnd.Format("15:04:05"))
	m.recordEvent(evt)
}

func (m *WorkTimeMonitor) convertClockOutToAway(t time.Time) {
	return
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
	case "점심시작", "점심종료":
		return
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
	newSummary := fmt.Sprintf("=== %s 근무시간: %d시간 %d분===", m.today, hours, mins)

	// 기존 요약 제거 후 새 요약 추가 (중복 방지)
	content, _ := os.ReadFile(worktimeFile)
	lines := strings.Split(string(content), "\n")
	var kept []string
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if strings.HasPrefix(line, "===") && strings.Contains(line, m.today) {
			continue // 오늘 날짜 기존 요약 제거
		}
		kept = append(kept, line)
	}
	kept = append(kept, newSummary)
	os.WriteFile(worktimeFile, []byte(strings.Join(kept, "\n")+"\n"), 0644)
}

func (m *WorkTimeMonitor) calcWorkDuration() time.Duration {
	if m.clockInTime.IsZero() {
		return 0
	}
	lastTime := m.clockOutTime
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

	// Add today's events (점심 제외, 중복 출근 방지)
	clockInWritten := false
	for _, evt := range m.events {
		if evt.Type == "점심시작" || evt.Type == "점심종료" {
			continue
		}
		// 출근 중복 방지: 첫 번째만 기록
		if evt.Type == "출근" {
			if clockInWritten {
				log.Printf("[WorkTime] rewriteAll: 중복 출근 제거 (%s)", evt.Time.Format("15:04:05"))
				continue
			}
			clockInWritten = true
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
