// Package web — опросник в браузере (бриф §7, этап 2). Без состояния на сервере:
// ответы передаются скрытыми полями формы, работает без JavaScript.
package web

import (
	"embed"
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/present"
	"github.com/RunetX/aktura-etrn/quiz"
	"github.com/RunetX/aktura-etrn/store"
)

//go:embed templates/*.html
var files embed.FS

type Server struct {
	base    *engine.Base
	set     *quiz.Set
	tpl     *template.Template
	pr      *present.Presenter
	ai      *aiConf // nil — режим без ИИ
	op      Operator
	docs    map[string]*legalDoc // nil — реквизиты оператора не заданы, данные не собираются
	metrika string               // номер счётчика Яндекс Метрики; пусто — выключена
	store   *store.Store         // журнал ситуаций и отзывы; nil — выключены
	bots    []kv                 // ссылки на ботов в мессенджерах: название → адрес
}

// Option — необязательная настройка сервера.
type Option func(*Server)

// WithOperator — реквизиты оператора персональных данных для политики и согласий.
func WithOperator(op Operator) Option { return func(s *Server) { s.op = op } }

// WithMetrika — счётчик Яндекс Метрики (загружается только с согласия посетителя).
func WithMetrika(id string) Option { return func(s *Server) { s.metrika = id } }

// WithStore — журнал ситуаций и отзывы «не так».
func WithStore(st *store.Store) Option { return func(s *Server) { s.store = st } }

// WithBots — ссылки на ботов в мессенджерах (пусто — ссылки нет). Принимаются только
// https://max.ru/… и https://t.me/…; к ссылке добавляется метка ?start=site — по ней журнал
// ситуаций бота отличает пришедших с сайта.
func WithBots(maxURL, tgURL string) Option {
	return func(s *Server) {
		if strings.HasPrefix(maxURL, "https://max.ru/") && !strings.Contains(maxURL, "?") {
			s.bots = append(s.bots, kv{Key: "MAX", Value: maxURL + "?start=site"})
		}
		if strings.HasPrefix(tgURL, "https://t.me/") && !strings.Contains(tgURL, "?") {
			s.bots = append(s.bots, kv{Key: "Telegram", Value: tgURL + "?start=site"})
		}
	}
}

// ErrNoOperator — обработка персональных данных включена без реквизитов оператора.
var ErrNoOperator = errors.New("ИИ-слой, Метрика и отзывы обрабатывают персональные данные: задайте реквизиты оператора (AKTURA_OPERATOR, AKTURA_CONTACT)")

func New(b *engine.Base, s *quiz.Set, opts ...Option) (http.Handler, error) {
	tpl, err := template.New("").Funcs(template.FuncMap{
		"cyr": func(s string) string { return strings.ReplaceAll(s, "T", "Т") },
	}).ParseFS(files, "templates/*.html")
	if err != nil {
		return nil, err
	}
	srv := &Server{base: b, set: s, tpl: tpl, pr: present.New(b, s)}
	for _, o := range opts {
		o(srv)
	}
	if strings.Trim(srv.metrika, "0123456789") != "" {
		return nil, errors.New("номер счётчика Метрики — только цифры")
	}
	if srv.op.Name != "" || srv.op.Contact != "" {
		if srv.op.Name == "" || srv.op.Contact == "" {
			return nil, ErrNoOperator
		}
		if srv.docs, err = loadDocs(srv.op); err != nil {
			return nil, err
		}
	} else if srv.ai != nil || srv.metrika != "" {
		return nil, ErrNoOperator
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.start)
	mux.HandleFunc("POST /step", srv.step)
	if srv.ai != nil {
		mux.HandleFunc("POST /describe", srv.describe)
	}
	if srv.docs != nil {
		mux.HandleFunc("GET /privacy", srv.docPage("privacy"))
		if srv.feedbackOn() {
			mux.HandleFunc("POST /feedback", srv.feedback)
		}
	}
	if srv.metrika != "" {
		mux.HandleFunc("GET /consent/metrika", srv.docPage("consent_metrika"))
		mux.HandleFunc("GET /cookies", srv.cookiesPage)
		mux.HandleFunc("POST /cookies", srv.cookiesSet)
		mux.HandleFunc("GET /metrika.js", srv.metrikaJS)
	}
	mux.HandleFunc("GET /robots.txt", srv.robots)
	mux.HandleFunc("GET /sitemap.xml", srv.sitemap)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte("ok"))
	})
	return srv.secure(methodGuard(mux)), nil
}

// feedbackOn — отзывы «не так» принимаются: есть куда писать и кем подписано согласие.
func (s *Server) feedbackOn() bool { return s.store != nil && s.docs != nil }

const csp = "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; base-uri 'none'; frame-ancestors 'none'"

// cspMetrika — дополнения для счётчика по документации Метрики
// (https://yandex.ru/support/metrica/ru/code/install-counter-csp); Вебвизор и карта кликов выключены,
// поэтому фреймы и websocket не нужны.
const cspMetrika = "; script-src 'self' https://mc.yandex.ru https://yastatic.net; img-src https://mc.yandex.ru https://mc.yandex.com; connect-src https://mc.yandex.ru https://mc.yandex.com"

// secure — заголовки безопасности. Страница самодостаточна: стили встроены, формы уходят
// только на свой адрес. Скрипт (счётчик Метрики) разрешается только тому, кто на него согласился.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		policy := csp
		if s.metrikaAllowed(r) {
			policy += cspMetrika
		}
		h.Set("Content-Security-Policy", policy)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		next.ServeHTTP(w, r)
	})
}

// methodGuard: формы отправляются только POST. Открытый по ссылке адрес формы (закладка,
// проверка счётчика Метрики) уводит на начало опроса с тем же запросом.
func methodGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/step", "/describe", "/feedback":
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				to := "/"
				if r.URL.RawQuery != "" {
					to += "?" + r.URL.RawQuery
				}
				http.Redirect(w, r, to, http.StatusSeeOther)
				return
			}
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) start(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, engine.Facts{}, nil)
}

// state — ответы и история из скрытых полей формы; всё, чего нет в вопросах, отбрасывается.
func (s *Server) state(r *http.Request) (engine.Facts, []string) {
	answers := engine.Facts{}
	for k, vs := range r.PostForm {
		fact, ok := strings.CutPrefix(k, "f_")
		if ok && len(vs) == 1 && s.pr.Valid(fact, vs[0]) {
			answers[fact] = vs[0]
		}
	}
	var hist []string
	for _, id := range strings.Split(r.PostFormValue("h"), ",") {
		if q := s.set.Question(id); q != nil {
			hist = append(hist, id)
		}
	}
	return answers, hist
}

func (s *Server) step(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	answers, hist := s.state(r)
	st := present.State{Answers: answers, History: hist}

	if r.PostFormValue("back") != "" {
		st = s.pr.Back(st)
		s.render(w, r, st.Answers, st.History)
		return
	}

	answered := false
	if q := s.set.Question(r.PostFormValue("q")); q != nil {
		switch {
		case r.PostFormValue("unknown") != "":
			st = s.pr.Unknown(st, q)
		case q.Group != "":
			st = s.pr.Group(st, q, r.PostForm["g"])
		default:
			var ok bool
			if st, ok = s.pr.Answer(st, q, r.PostFormValue("v")); !ok {
				s.render(w, r, st.Answers, st.History)
				return
			}
		}
		answered = true
	}
	answers, hist = st.Answers, st.History
	// Журнал ситуаций: только когда ответ на вопрос привёл к результату (не при «Назад»
	// и не при повторном показе), без текста и без сведений о посетителе.
	if answered && s.store != nil && s.set.Next(s.base, answers) == nil {
		s.logSituation(answers, r.PostFormValue("via") == "ai")
	}
	s.render(w, r, answers, hist)
}

func (s *Server) logSituation(answers engine.Facts, ai bool) {
	res := s.base.Evaluate(answers)
	via := "quiz"
	if ai {
		via = "ai"
	}
	err := s.store.AddSituation(store.Situation{Day: s.store.Today(), Via: via, Facts: answers, Rules: res.AllRuleIDs(),
		Certainty: res.Certainty, Found: res.RoleRule != nil, RulesVer: s.base.Meta.Version})
	if err != nil {
		log.Printf("журнал ситуаций: %v", err)
	}
}

// ---------- представление ----------

type kv = present.KV

type qView struct {
	ID, Text, Hint string
	Group          bool
	Options        []quiz.Option
	Checked        map[string]bool // галочки, уже отмеченные разбором описания
}

type fbView struct {
	Sent    bool
	Error   string
	Comment string
}

type page struct {
	AI      *aiView // форма описания (только на первом шаге) или подтверждение разбора
	Q       *qView
	R       *present.Result
	FB      *fbView      // форма «В моей ситуации это не так» под результатом
	Doc     *legalDoc    // страница политики или согласия
	Cookies *cookiesView // страница «Настройки cookie»
	Hidden  []kv
	Hist    string
	Via     string
	Step    int
	Answers []kv
	Meta    engine.Meta
	Quiz    string
	// Общее для всех страниц: баннер согласия на Метрику, счётчик, ссылки в подвале.
	Banner  bool
	Ret     string // куда вернуться после выбора в баннере
	Metrika bool
	Privacy bool
	Cookie  bool
	Bots    []kv
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, answers engine.Facts, hist []string) {
	var p page
	if s.ai != nil && len(answers) == 0 && len(hist) == 0 {
		p.AI = &aiView{Form: true, Model: s.ai.name, Max: extract.MaxText}
	}
	if r.Method == http.MethodPost && r.PostFormValue("via") == "ai" {
		p.Via = "ai"
	}
	s.renderPage(w, r, p, answers, hist)
}

// renderPage дополняет p общими полями. На экране подтверждения разбора описания, на страницах
// документов и настроек cookie вопроса и результата нет.
func (s *Server) renderPage(w http.ResponseWriter, r *http.Request, p page, answers engine.Facts, hist []string) {
	p.Hist, p.Step, p.Meta, p.Quiz = strings.Join(hist, ","), len(hist)+1, s.base.Meta, s.set.Version
	p.Privacy, p.Cookie, p.Bots = s.docs != nil, s.metrika != "", s.bots
	p.Metrika = s.metrikaAllowed(r)
	p.Banner = s.metrika != "" && metrikaChoice(r) == "" && p.Cookies == nil
	if p.Ret == "" {
		p.Ret = "quiz"
		if p.Doc != nil {
			p.Ret = r.URL.Path
		}
	}
	for _, q := range s.set.Questions {
		for _, f := range q.Facts() {
			if v, ok := answers[f]; ok {
				p.Hidden = append(p.Hidden, kv{Key: "f_" + f, Value: v})
			}
		}
	}
	p.Answers = s.pr.Summary(answers)
	switch q := s.set.Next(s.base, answers); {
	case p.AI != nil && p.AI.Confirm, p.Doc != nil, p.Cookies != nil:
	case q != nil:
		p.Q = &qView{ID: q.ID(), Text: q.Text, Hint: q.Hint, Group: q.Group != "", Options: q.Options, Checked: map[string]bool{}}
		for _, f := range q.Facts() {
			p.Q.Checked[f] = answers[f] == "yes"
		}
	default:
		p.R = s.pr.Result(answers)
		if s.feedbackOn() && p.FB == nil {
			p.FB = &fbView{}
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tpl.ExecuteTemplate(w, "page.html", p); err != nil {
		log.Printf("шаблон: %v", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}
