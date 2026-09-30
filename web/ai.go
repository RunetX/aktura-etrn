package web

import (
	"context"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/extract"
)

// ИИ-слой (бриф §5, этап 3): описание своими словами → предложенные ответы на вопросы.
// Предложения не применяются молча: человек видит их с цитатой из своего текста и снимает
// лишние галочки; подтверждённое уходит в /step как обычные ответы, остальное спросит опросник.
// Текст описания не сохраняется и не пишется в журнал.

type aiConf struct {
	ext     *extract.Extractor
	name    string // название модели для предупреждения в форме
	timeout time.Duration
	limit   *bucket // в минуту
	daily   *daily  // в сутки — потолок расходов на модель
}

// WithExtractor включает форму описания. perMinute и perDay — общие (не по адресам) пределы
// обращений к модели: модель платная (запрос — порядка рубля и больше), адреса посетителей
// не храним. perDay — потолок суточных расходов: без него поток запросов стоил бы десятки
// тысяч рублей в сутки.
func WithExtractor(ext *extract.Extractor, name string, perMinute, perDay int, timeout time.Duration) Option {
	return func(s *Server) {
		s.ai = &aiConf{ext: ext, name: name, timeout: timeout, limit: newBucket(perMinute, time.Minute), daily: &daily{n: perDay}}
	}
}

type aiItem struct {
	Name, Value, Question, Label, Quote string
}

type aiView struct {
	Form    bool // форма описания на первом шаге
	Model   string
	Max     int
	Text    string // введённый текст — чтобы вернуть его в форму при ошибке
	Error   string
	Confirm bool // экран подтверждения разбора
	Items   []aiItem
}

func (s *Server) describe(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	text := strings.TrimSpace(r.PostFormValue("text"))
	if text == "" {
		s.render(w, r, engine.Facts{}, nil)
		return
	}
	fail := func(msg string) {
		s.renderPage(w, r, page{AI: &aiView{Form: true, Model: s.ai.name, Max: extract.MaxText, Text: text, Error: msg}}, engine.Facts{}, nil)
	}
	if !s.ai.daily.take() {
		fail("Разбор описаний на сегодня исчерпан. Ответьте на вопросы ниже — это работает всегда.")
		return
	}
	if !s.ai.limit.take() {
		fail("Разбор описаний сейчас перегружен. Попробуйте через минуту или ответьте на вопросы ниже.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), s.ai.timeout)
	defer cancel()
	res, err := s.ai.ext.Extract(ctx, text)
	if err != nil {
		log.Printf("describe: ошибка модели: %v", err)
		fail("Не удалось разобрать описание. Ответьте на вопросы ниже — это работает всегда.")
		return
	}
	var why []string
	for _, rj := range res.Rejected {
		why = append(why, rj.Fact+": "+rj.Reason)
	}
	log.Printf("describe: принято %d, отброшено %d %v", len(res.Accepted), len(res.Rejected), why)

	v := &aiView{Confirm: true, Model: s.ai.name}
	for _, q := range s.set.Questions {
		for _, it := range res.Accepted {
			item := aiItem{Name: "f_" + it.Fact, Value: it.Value, Question: q.Text, Quote: it.Quote}
			for _, o := range q.Options {
				switch {
				case q.Group != "" && o.Fact == it.Fact:
					item.Label = o.Label
				case q.Group == "" && q.Fact == it.Fact && o.Value == it.Value:
					item.Label = o.Label
				}
			}
			if item.Label != "" {
				v.Items = append(v.Items, item)
			}
		}
	}
	s.renderPage(w, r, page{AI: v, Via: "ai"}, engine.Facts{}, nil)
}

// bucket — простой общий ограничитель: не больше n обращений за период.
type bucket struct {
	mu     sync.Mutex
	n      int
	period time.Duration
	start  time.Time
	used   int
}

func newBucket(n int, period time.Duration) *bucket { return &bucket{n: n, period: period} }

func (b *bucket) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now := time.Now(); now.Sub(b.start) >= b.period {
		b.start, b.used = now, 0
	}
	if b.used >= b.n {
		return false
	}
	b.used++
	return true
}

// daily — не больше n обращений за календарные сутки (по Москве, UTC+3).
type daily struct {
	mu   sync.Mutex
	n    int
	day  string
	used int
	now  func() time.Time
}

var msk = time.FixedZone("MSK", 3*60*60)

func (d *daily) take() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := time.Now
	if d.now != nil {
		now = d.now
	}
	if day := now().In(msk).Format(time.DateOnly); day != d.day {
		d.day, d.used = day, 0
	}
	if d.used >= d.n {
		return false
	}
	d.used++
	return true
}
