package main

import (
	"testing"
	"time"
)

// TestParseIdleFromQueryUser_NoSessionIDMisparse is the regression test for the
// 2026-05-30 incident: when the idle-detector helper died, the `query user`
// fallback misread the numeric session ID "1" as the idle value (= 1 minute =
// 60s), making the user look active 24/7 and producing bogus 05:59 clock-ins /
// 21:59 clock-outs. STATE "활성" arrives as mojibake on Korean Windows.
func TestParseIdleFromQueryUser_NoSessionIDMisparse(t *testing.T) {
	// Reproduces the real console line (STATE = mojibake, IDLE = none).
	line := ">lab                   console             1  Ȱ��        none   2026-05-19 ���� 7:13"
	got := parseIdleFromQueryUser(line)
	if got == 1*time.Minute {
		t.Fatalf("regression: session ID misread as 60s idle (got %v)", got)
	}
	if got != 0 {
		t.Errorf("IDLE=none should parse as 0 (active), got %v", got)
	}
}

func TestParseIdleFromQueryUser_Variants(t *testing.T) {
	cases := []struct {
		name string
		line string
		want time.Duration
	}{
		{"none means active", "lab console 1 Active none 2026-05-19 19:13", 0},
		{"minutes", "lab console 1 Active 5 2026-05-19 19:13", 5 * time.Minute},
		{"hours:mins", "lab console 1 Active 1:30 2026-05-19 19:13", 90 * time.Minute},
		{"current-session marker", ">lab console 1 Active 12 2026-05-19 19:13", 12 * time.Minute},
		{"mojibake state, numeric idle", ">lab console 1 Ȱ�� 7 2026-05-19 19:13", 7 * time.Minute},
		{"no console token", "lab rdp-tcp 2 Active 3 2026-05-19 19:13", queryUserUnknownIdle},
		{"truncated line", "lab console 1", queryUserUnknownIdle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseIdleFromQueryUser(c.line); got != c.want {
				t.Errorf("parseIdleFromQueryUser(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

func TestParseIdleDuration(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"none", 0},
		{".", 0},
		{"5", 5 * time.Minute},
		{"1:30", 90 * time.Minute},
		{"2+03:04", 2*24*time.Hour + 3*time.Hour + 4*time.Minute},
	}
	for _, c := range cases {
		if got := parseIdleDuration(c.in); got != c.want {
			t.Errorf("parseIdleDuration(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
