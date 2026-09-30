// Package bot — опросник в мессенджерах (MAX, Telegram): то же ядро, что у сайта
// (quiz.Set.Next, engine.Evaluate через present), только кнопками. Выводы даёт движок.
//
// Свободный текст бот не принимает и никуда не пишет. Состояние опроса — в памяти,
// по ID пользователя мессенджера, не дольше TTL; на диск не попадает. В журнал ситуаций
// и отзывов ID пользователя не пишется.
package bot

import (
	"context"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RunetX/aktura-etrn/present"
	"github.com/RunetX/aktura-etrn/store"
)

// Глаголы payload.
const (
	vAnswer  = "a" // a:<qid>:<value>
	vUnknown = "u" // u:<qid>
	vToggle  = "t" // t:<fact> — галочка в вопросе-группе
	vDone    = "d" // d:<qid> — «Готово» в вопросе-группе
	vBack    = "b" // b:<шаг> — шаг, на котором показана кнопка
	vRestart = "r"
	vWrong   = "x" // x:<шаг> — «В моей ситуации не так»
)

// keepSource — «Заново» и истёкший опрос сохраняют метку, с которой человек пришёл.
const keepSource = "=keep" // не может прийти из ссылки: там только [A-Za-z0-9_-]

// Commands — меню команд бота.
var Commands = []Command{{Name: "start", Description: "Начать опрос заново"}, {Name: "help", Description: "Что умеет бот"}}

type Config struct {
	Via   string       // канал для журнала ситуаций: max | telegram
	Store *store.Store // журнал ситуаций и отзывы; nil — выключены
	Site  string       // адрес сайта для ссылок, например https://aktura.ru
	TTL   time.Duration
	// Sources — разрешённые метки ссылок на бота (?start=klerk): в журнал пишется метка из
	// списка, любая другая — «other». Свободная строка из ссылки в журнал не попадает.
	// «site» разрешена всегда: её ставят кнопки на сайте.
	Sources []string
}

// SiteSource — метка ссылок на ботов с сайта.
const SiteSource = "site"

type session struct {
	mu      sync.Mutex
	st      present.State
	checked map[string]bool // галочки в текущем вопросе-группе до «Готово»
	msgID   string          // сообщение с действующими кнопками
	text    string          // его текст — чтобы снять кнопки правкой (в MAX иначе нельзя)
	result  bool            // на экране результат
	sent    bool            // отзыв «не так» на этот результат уже принят
	source  string          // метка ссылки, по которой пришли (из Config.Sources, other или пусто)
	seen    time.Time
}

type Logic struct {
	pr  *present.Presenter
	tr  Transport
	cfg Config
	now func() time.Time

	mu       sync.Mutex
	sessions map[int64]*session
}

func New(pr *present.Presenter, tr Transport, cfg Config) *Logic {
	if cfg.TTL == 0 {
		cfg.TTL = 24 * time.Hour
	}
	return &Logic{pr: pr, tr: tr, cfg: cfg, now: time.Now, sessions: map[int64]*session{}}
}

// Run чистит устаревшие сессии, пока не отменён ctx.
func (l *Logic) Run(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			l.Purge()
		}
	}
}

// Purge удаляет сессии старше TTL.
func (l *Logic) Purge() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for id, s := range l.sessions {
		if l.now().Sub(s.seen) > l.cfg.TTL {
			delete(l.sessions, id)
		}
	}
}

// session — сессия пользователя (nil — нет или истекла); fresh — начать новую.
func (l *Logic) session(user int64, fresh bool) *session {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := l.sessions[user]
	if s != nil && l.now().Sub(s.seen) > l.cfg.TTL {
		delete(l.sessions, user)
		s = nil
	}
	if s == nil && fresh {
		s = &session{}
		l.sessions[user] = s
	}
	if s != nil {
		s.seen = l.now()
	}
	return s
}

// Text — входящее сообщение. Команды /start и /help; прочий текст не обрабатывается и не сохраняется.
func (l *Logic) Text(ctx context.Context, user int64, text string) {
	switch command(text) {
	case "/start":
		l.Start(ctx, user, commandArg(text))
	case "/help":
		l.tr.Send(ctx, user, l.welcome(), nil)
	default:
		kb := (&Keyboard{}).Row(Button{Text: "Начать заново", Payload: Pack(vRestart, "")})
		l.tr.Send(ctx, user, "Я понимаю только кнопки: выберите ответ под вопросом. Описание своими словами я не читаю и не сохраняю.", kb)
	}
}

// Start — приветствие и первый вопрос (для MAX — ещё и событие «бот запущен»).
func (l *Logic) Start(ctx context.Context, user int64, source string) {
	l.tr.Send(ctx, user, l.welcome(), nil)
	l.restart(ctx, user, l.source(source))
}

// source — метка для журнала: из разрешённого списка, иначе other; пусто — без метки.
func (l *Logic) source(raw string) string {
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch {
	case raw == "":
		return ""
	case raw == SiteSource || contains(l.cfg.Sources, raw):
		return raw
	}
	return "other"
}

func (l *Logic) welcome() string {
	var b strings.Builder
	b.WriteString("Кто есть кто в ЭТрН\n\n")
	b.WriteString("Отвечу, кто в вашей перевозке грузоотправитель, грузополучатель, перевозчик и кто какой титул ЭТрН подписывает. ")
	b.WriteString("Отвечайте кнопками; если не знаете ответа — «Не знаю».\n\n")
	b.WriteString("Справочный инструмент, не юридическая консультация. Роли и выводы даёт детерминированный движок по базе правил со ссылками на нормы.")
	if l.cfg.Site != "" {
		b.WriteString("\n\nСайт: " + l.cfg.Site + "\nПолитика обработки данных: " + l.cfg.Site + "/privacy")
	}
	return b.String()
}

// restart начинает опрос заново; source == keepSource — метка прежней сессии.
func (l *Logic) restart(ctx context.Context, user int64, source string) {
	s := l.session(user, false)
	if s != nil {
		s.mu.Lock()
		l.dropKeyboard(ctx, user, s)
		if source == keepSource {
			source = s.source
		}
		s.mu.Unlock()
	}
	if source == keepSource {
		source = ""
	}
	l.mu.Lock()
	s = &session{seen: l.now(), source: source}
	l.sessions[user] = s
	l.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.st = present.State{Answers: map[string]string{}}
	l.show(ctx, user, s, "")
}

// Callback — нажатие кнопки.
func (l *Logic) Callback(ctx context.Context, cb Callback) {
	verb, arg, ok := Parse(cb.Payload)
	if !ok {
		l.tr.AnswerCallback(ctx, cb, "Кнопка устарела — начните заново: /start")
		return
	}
	if verb == vRestart {
		l.tr.AnswerCallback(ctx, cb, "")
		l.restart(ctx, cb.UserID, keepSource)
		return
	}
	s := l.session(cb.UserID, false)
	if s == nil {
		l.tr.AnswerCallback(ctx, cb, "Опрос устарел — начнём заново")
		l.restart(ctx, cb.UserID, keepSource)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cb.MessageID != "" && cb.MessageID != s.msgID {
		l.tr.AnswerCallback(ctx, cb, "Эта кнопка уже не действует")
		return
	}
	q := l.pr.Set.Next(l.pr.Base, s.st.Answers)
	step := strconv.Itoa(len(s.st.History))
	switch verb {
	case vAnswer, vUnknown, vDone, vToggle:
		if q == nil || s.result {
			l.tr.AnswerCallback(ctx, cb, "Этот вопрос уже пройден")
			return
		}
	}
	switch verb {
	case vAnswer:
		qid, v, _ := strings.Cut(arg, ":")
		st, ok := s.st, false
		if qid == q.ID() {
			st, ok = l.pr.Answer(s.st, q, v)
		}
		if !ok {
			l.tr.AnswerCallback(ctx, cb, "Этот вопрос уже пройден")
			return
		}
		l.tr.AnswerCallback(ctx, cb, "")
		s.st = st
		l.advance(ctx, cb.UserID, s)
	case vUnknown:
		if arg != q.ID() {
			l.tr.AnswerCallback(ctx, cb, "Этот вопрос уже пройден")
			return
		}
		l.tr.AnswerCallback(ctx, cb, "")
		s.st = l.pr.Unknown(s.st, q)
		l.advance(ctx, cb.UserID, s)
	case vToggle:
		if q.Group == "" || !contains(q.Facts(), arg) {
			l.tr.AnswerCallback(ctx, cb, "Этот вопрос уже пройден")
			return
		}
		l.tr.AnswerCallback(ctx, cb, "")
		if s.checked == nil {
			s.checked = map[string]bool{}
		}
		s.checked[arg] = !s.checked[arg]
		l.show(ctx, cb.UserID, s, s.msgID)
	case vDone:
		if q.Group == "" || arg != q.ID() {
			l.tr.AnswerCallback(ctx, cb, "Этот вопрос уже пройден")
			return
		}
		l.tr.AnswerCallback(ctx, cb, "")
		var checked []string
		for f, on := range s.checked {
			if on {
				checked = append(checked, f)
			}
		}
		s.st = l.pr.Group(s.st, q, checked)
		l.advance(ctx, cb.UserID, s)
	case vBack:
		if arg != step || len(s.st.History) == 0 {
			l.tr.AnswerCallback(ctx, cb, "Эта кнопка уже не действует")
			return
		}
		l.tr.AnswerCallback(ctx, cb, "")
		wasResult := s.result
		s.st = l.pr.Back(s.st)
		s.checked = nil
		if wasResult {
			// результат остаётся в истории чата, вопрос — новым сообщением
			l.dropKeyboard(ctx, cb.UserID, s)
			l.show(ctx, cb.UserID, s, "")
		} else {
			l.show(ctx, cb.UserID, s, s.msgID)
		}
	case vWrong:
		if !s.result || arg != step {
			l.tr.AnswerCallback(ctx, cb, "Эта кнопка уже не действует")
			return
		}
		if s.sent {
			l.tr.AnswerCallback(ctx, cb, "Отзыв уже принят, спасибо")
			return
		}
		if l.cfg.Store == nil {
			l.tr.AnswerCallback(ctx, cb, "Отзывы сейчас не принимаются")
			return
		}
		res := l.pr.Base.Evaluate(s.st.Answers)
		err := l.cfg.Store.AddFeedback(store.Feedback{Day: l.cfg.Store.Today(), Facts: s.st.Answers, Rules: res.AllRuleIDs(),
			Comment: "(бот " + l.cfg.Via + ", без комментария)", Source: s.source, RulesVer: l.pr.Base.Meta.Version})
		if err != nil {
			log.Printf("бот %s: отзыв: %v", l.cfg.Via, err)
			l.tr.AnswerCallback(ctx, cb, "Не удалось сохранить отзыв, попробуйте позже")
			return
		}
		s.sent = true
		l.tr.AnswerCallback(ctx, cb, "Спасибо! По отзыву проверим правила.")
		text := s.text + "\n\nОтзыв «не так» принят. Подробнее описать ситуацию можно на сайте — там есть поле для комментария."
		l.tr.Edit(ctx, cb.UserID, s.msgID, text, l.resultKeyboard(s))
		s.text = text
	default:
		l.tr.AnswerCallback(ctx, cb, "Кнопка устарела — начните заново: /start")
	}
}

// advance — после ответа: следующий вопрос правкой того же сообщения или результат.
func (l *Logic) advance(ctx context.Context, user int64, s *session) {
	s.checked = nil
	if l.pr.Set.Next(l.pr.Base, s.st.Answers) != nil {
		l.show(ctx, user, s, s.msgID)
		return
	}
	l.logSituation(s)
	l.show(ctx, user, s, s.msgID)
}

// show выводит текущий экран: вопрос (правкой edit или новым сообщением) или результат.
func (l *Logic) show(ctx context.Context, user int64, s *session, edit string) {
	q := l.pr.Set.Next(l.pr.Base, s.st.Answers)
	if q != nil {
		s.result, s.sent = false, false
		text, kb := l.question(s, q)
		if edit != "" {
			l.tr.Edit(ctx, user, edit, text, kb)
		} else {
			edit = l.tr.Send(ctx, user, text, kb)
		}
		s.msgID, s.text = edit, text
		return
	}
	// Результат: сообщение с вопросом превращается в сводку ответов, итог — новыми сообщениями.
	summary := "Ваши ответы:\n" + l.summary(s)
	if edit != "" {
		l.tr.Edit(ctx, user, edit, summary, nil)
	} else {
		l.tr.Send(ctx, user, summary, nil)
	}
	s.result, s.sent = true, false
	parts := split(l.resultText(s), maxMessage)
	var id string
	for i, p := range parts {
		var kb *Keyboard
		if i == len(parts)-1 {
			kb = l.resultKeyboard(s)
		}
		id = l.tr.Send(ctx, user, p, kb)
		s.text = p
	}
	s.msgID = id
}

// dropKeyboard снимает кнопки с сообщения, у которого они действуют.
func (l *Logic) dropKeyboard(ctx context.Context, user int64, s *session) {
	if s.msgID != "" && s.text != "" {
		l.tr.Edit(ctx, user, s.msgID, s.text, nil)
	}
	s.msgID = ""
}

func (l *Logic) logSituation(s *session) {
	if l.cfg.Store == nil {
		return
	}
	res := l.pr.Base.Evaluate(s.st.Answers)
	err := l.cfg.Store.AddSituation(store.Situation{Day: l.cfg.Store.Today(), Via: l.cfg.Via, Source: s.source, Facts: s.st.Answers,
		Rules: res.AllRuleIDs(), Certainty: res.Certainty, Found: res.RoleRule != nil, RulesVer: l.pr.Base.Meta.Version})
	if err != nil {
		log.Printf("бот %s: журнал ситуаций: %v", l.cfg.Via, err)
	}
}

// command — команда в начале сообщения (учитывает /cmd@botname).
func command(text string) string {
	f := strings.Fields(text)
	if len(f) == 0 || !strings.HasPrefix(f[0], "/") {
		return ""
	}
	cmd, _, _ := strings.Cut(f[0], "@")
	return cmd
}

// commandArg — аргумент команды: /start klerk → klerk (Telegram передаёт метку ссылки так).
func commandArg(text string) string {
	f := strings.Fields(text)
	if len(f) < 2 {
		return ""
	}
	return f[1]
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
