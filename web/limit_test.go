package web

import (
	"testing"
	"time"
)

func TestDailyLimit(t *testing.T) {
	now := time.Date(2026, 9, 24, 20, 59, 0, 0, time.UTC) // 23:59 по Москве
	d := &daily{n: 2, now: func() time.Time { return now }}
	if !d.take() || !d.take() || d.take() {
		t.Fatal("потолок 2 в сутки не держится")
	}
	now = now.Add(2 * time.Minute) // 00:01 по Москве — новые сутки
	if !d.take() {
		t.Error("в новые сутки счётчик не сброшен")
	}
}
