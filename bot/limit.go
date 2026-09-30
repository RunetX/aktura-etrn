package bot

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter — не чаще одного сообщения в интервал на пользователя (мессенджеры режут частые
// отправки в один чат: результат уходит двумя-тремя сообщениями подряд).
type Limiter struct {
	mu    sync.Mutex
	every time.Duration
	m     map[int64]*rate.Limiter
}

func NewLimiter(every time.Duration) *Limiter {
	return &Limiter{every: every, m: map[int64]*rate.Limiter{}}
}

func (l *Limiter) Wait(ctx context.Context, user int64) error {
	l.mu.Lock()
	if len(l.m) > 10000 { // простой потолок памяти: лимитеры пересоздаются
		clear(l.m)
	}
	lim := l.m[user]
	if lim == nil {
		lim = rate.NewLimiter(rate.Every(l.every), 2)
		l.m[user] = lim
	}
	l.mu.Unlock()
	return lim.Wait(ctx)
}
