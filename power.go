package main

import (
	"fmt"
	"log"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modkernel32              = windows.NewLazyDLL("kernel32.dll")
	procSetConsoleCtrlHandler = modkernel32.NewProc("SetConsoleCtrlHandler") // not used for service
	moduser32                = windows.NewLazyDLL("user32.dll")
	procRegisterPowerNotify  = windows.NewLazyDLL("user32.dll").NewProc("RegisterPowerSettingNotification")
)

// Use a simpler approach: monitor system uptime / detect resume by checking time gaps
func monitorPowerEvents(done <-chan struct{}) {
	log.Println("Power event monitor started.")

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	lastCheck := time.Now()

	for {
		select {
		case <-done:
			log.Println("Power event monitor stopped.")
			return
		case now := <-ticker.C:
			elapsed := now.Sub(lastCheck)

			// If more than 2 minutes passed between 30s ticks,
			// the system was likely sleeping/hibernating
			if elapsed > 2*time.Minute {
				hostname, _ := os.Hostname()
				sleepDuration := elapsed.Round(time.Second)

				log.Printf("[Power] Resume detected! gap=%s, lastCheck=%s, now=%s",
					sleepDuration, lastCheck.Format("15:04:05"), now.Format("15:04:05"))

				msg := fmt.Sprintf("💤 <b>PC Resumed from Sleep</b>\n\n"+
					"🖥 Host: %s\n"+
					"⏰ Time: %s\n"+
					"😴 Slept for: %s\n"+
					"✅ Gateway is running!", hostname, now.Format("2006-01-02 15:04:05"), sleepDuration)

				err := sendTelegramNotification(msg)
				if err != nil {
					log.Printf("[Power] Failed to send resume notification: %v", err)
				}
			} else {
				log.Printf("[Power] tick: elapsed=%s (normal)", elapsed.Round(time.Second))
			}

			lastCheck = now
		}
	}
}

// Alternative: Register for Windows power broadcast via undocumented service notification
// This uses PowerBroadcastStatus in the service handler
const (
	SERVICE_CONTROL_POWEREVENT = 13
	PBT_APMRESUMEAUTOMATIC    = 18
	PBT_APMRESUMESUSPEND      = 7
	PBT_APMSUSPEND            = 4
)

// Unused but reserved for future use with extended service handler
var _ = unsafe.Pointer(nil)
