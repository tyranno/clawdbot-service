package main

import (
	"fmt"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/Microsoft/go-winio"
)

var (
	user32               = syscall.NewLazyDLL("user32.dll")
	kernel32             = syscall.NewLazyDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

type LASTINPUTINFO struct {
	CbSize uint32
	DwTime uint32
}

func GetIdleTime() time.Duration {
	var lii LASTINPUTINFO
	lii.CbSize = uint32(unsafe.Sizeof(lii))

	ret, _, _ := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&lii)))
	if ret == 0 {
		return 0
	}

	tickCount, _, _ := procGetTickCount.Call()
	idleMs := uint32(tickCount) - lii.DwTime

	return time.Duration(idleMs) * time.Millisecond
}

const pipeName = `\\.\pipe\openclaw-idle`

func main() {
	consoleMode := len(os.Args) > 1 && os.Args[1] == "-console"

	if consoleMode {
		fmt.Println("=== Idle Detector (Named Pipe) ===")
		fmt.Println("Pipe:", pipeName)
		fmt.Println()
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	var conn net.Conn
	var lastConnectAttempt time.Time

	for range ticker.C {
		idle := GetIdleTime()
		idleSec := int64(idle.Seconds())

		// 서비스에 연결 시도 (5초마다)
		if conn == nil && time.Since(lastConnectAttempt) > 5*time.Second {
			lastConnectAttempt = time.Now()
			c, err := winio.DialPipe(pipeName, nil)
			if err == nil {
				conn = c
				if consoleMode {
					fmt.Println("✅ 서비스 연결됨")
				}
			} else if consoleMode {
				fmt.Printf("⏳ 서비스 대기 중... (%v)\n", err)
			}
		}

		// 연결되어 있으면 idle 시간 전송
		if conn != nil {
			_, err := fmt.Fprintf(conn, "%d\n", idleSec)
			if err != nil {
				if consoleMode {
					fmt.Println("❌ 연결 끊김, 재연결 대기...")
				}
				conn.Close()
				conn = nil
			}
		}

		if consoleMode {
			fmt.Printf("[%s] idle: %d초\n", time.Now().Format("15:04:05"), idleSec)
		}
	}
}
