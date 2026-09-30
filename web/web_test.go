package web_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/quiz"
	"github.com/RunetX/aktura-etrn/web"
)

func setup(t *testing.T) (*engine.Base, *quiz.Set, http.Handler) {
	t.Helper()
	b, err := engine.Load("../rules/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := quiz.Load("../rules/questions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(b, s)
	if err != nil {
		t.Fatal(err)
	}
	return b, s, h
}

func do(t *testing.T, h http.Handler, method, target string, form url.Values) (int, string) {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestStart(t *testing.T) {
	_, s, h := setup(t)
	code, body := do(t, h, "GET", "/", nil)
	if code != 200 {
		t.Fatalf("GET / → %d", code)
	}
	for _, want := range []string{s.Question("international").Text, "Не знаю", `name="viewport"`, "не юридическая консультация"} {
		if !strings.Contains(body, want) {
			t.Errorf("стартовая страница не содержит %q", want)
		}
	}
}

// state собирает форму: известные ответы + ответ на вопрос q.
func state(answers engine.Facts, hist []string) url.Values {
	v := url.Values{}
	for k, x := range answers {
		v.Set("f_"+k, x)
	}
	if len(hist) > 0 {
		v.Set("h", strings.Join(hist, ","))
	}
	return v
}

// walk проходит опрос через /step, отвечая по truth (нет ответа — «Не знаю», галочки — не отмечены),
// и возвращает страницу результата.
func walk(t *testing.T, b *engine.Base, s *quiz.Set, h http.Handler, truth engine.Facts) string {
	t.Helper()
	answers := engine.Facts{}
	var hist []string
	var body string
	for step := 0; step < 30; step++ {
		q := s.Next(b, answers)
		if q == nil {
			break
		}
		form := state(answers, hist)
		form.Set("q", q.ID())
		if q.Group != "" {
			// ни одна галочка не отмечена
		} else if v, ok := truth[q.Fact]; ok {
			form.Set("v", v)
		} else {
			form.Set("unknown", "1")
		}
		var code int
		code, body = do(t, h, "POST", "/step", form)
		if code != 200 {
			t.Fatalf("шаг %d (%s): код %d", step, q.ID(), code)
		}
		for _, f := range q.Facts() {
			if q.Group != "" {
				answers[f] = "no"
			} else if v, ok := truth[f]; ok {
				answers[f] = v
			} else {
				answers[f] = engine.Unknown
			}
		}
		hist = append(hist, q.ID())
		if next := s.Next(b, answers); next != nil && !strings.Contains(body, next.Text) {
			t.Fatalf("после %s ожидался вопрос %q", q.ID(), next.Text)
		}
	}
	return body
}

// Пользователь проходит опрос для «покупатель нанимает перевозчика у поставщика» (T-004).
func TestFlowSelfPickup(t *testing.T) {
	b, s, h := setup(t)
	body := walk(t, b, s, h, engine.Facts{"international": "no", "transport": "hired_carrier", "carriage_customer": "buyer",
		"tmc_accounting": "yes", "owner_escort": "no", "loader": "seller", "consignee_party": "customer_itself", "etrn_in_contract": "no"})
	for _, want := range []string{
		"Грузоотправитель", "Покупатель", "Продавец",
		s.Labels.Form["paper_allowed"], s.Labels.FormNotes["paper_allowed"], s.Labels.Certainty["clear"],
		"R-004", "R-107", "Устав автомобильного транспорта", "Т1", "Проверено",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("страница результата не содержит %q", want)
		}
	}
	if strings.Contains(body, s.Labels.Signers["consignee_proxy"]) {
		t.Error("представитель грузополучателя показан, хотя о нём не спрашивали")
	}
}

// Груз за покупателя принимает его склад (T-401): Т3 — представитель по доверенности от имени покупателя.
func TestFlowReceiverProxy(t *testing.T) {
	b, s, h := setup(t)
	body := walk(t, b, s, h, engine.Facts{"international": "no", "transport": "hired_carrier", "carriage_customer": "seller",
		"tmc_accounting": "yes", "owner_escort": "no", "loader": "shipper", "consignee_party": "customer_buyer",
		"receiver_representative": "yes"})
	for _, want := range []string{
		s.Labels.Signers["consignee_proxy"] + " — Покупатель", "передоверия", "R-003", "R-301", "63-ФЗ, ст. 17.2",
		s.Labels.FormNotes["electronic"],
	} {
		if !strings.Contains(body, want) {
			t.Errorf("страница результата не содержит %q", want)
		}
	}
}

// Подсказки к пунктам группы исключений видны на странице вопроса.
func TestExceptionHints(t *testing.T) {
	b, s, h := setup(t)
	truth := engine.Facts{"international": "no", "transport": "hired_carrier", "carriage_customer": "seller",
		"tmc_accounting": "yes", "owner_escort": "no", "loader": "shipper", "consignee_party": "customer_buyer",
		"receiver_representative": "no"}
	answers := engine.Facts{}
	var body string
	for step := 0; ; step++ {
		q := s.Next(b, answers)
		if q == nil || step > 30 {
			t.Fatal("группа исключений не встретилась")
		}
		if q.Group == "exceptions" {
			break
		}
		form := state(answers, nil)
		form.Set("q", q.ID())
		if v, ok := truth[q.Fact]; ok {
			form.Set("v", v)
			answers[q.Fact] = v
		} else {
			form.Set("unknown", "1")
			answers[q.Fact] = engine.Unknown
		}
		_, body = do(t, h, "POST", "/step", form)
	}
	n := 0
	for _, o := range s.Question("exceptions").Options {
		if o.Hint == "" {
			continue
		}
		n++
		if !strings.Contains(body, o.Hint) {
			t.Errorf("на странице нет подсказки к %s", o.Fact)
		}
	}
	if n == 0 {
		t.Error("у пунктов группы исключений нет подсказок")
	}
}

func TestUnknownEverything(t *testing.T) {
	b, s, h := setup(t)
	answers := engine.Facts{}
	var body string
	for i := 0; i < 30; i++ {
		q := s.Next(b, answers)
		if q == nil {
			break
		}
		form := state(answers, nil)
		form.Set("q", q.ID())
		form.Set("unknown", "1")
		_, body = do(t, h, "POST", "/step", form)
		for _, f := range q.Facts() {
			answers[f] = engine.Unknown
		}
	}
	if !strings.Contains(body, "не хватает") {
		t.Error("при «не знаю» на всё нет сообщения, каких ответов не хватает")
	}
}

func TestBack(t *testing.T) {
	_, s, h := setup(t)
	form := state(engine.Facts{"international": "no"}, []string{"international"})
	form.Set("back", "1")
	_, body := do(t, h, "POST", "/step", form)
	if !strings.Contains(body, s.Question("international").Text) {
		t.Error("«Назад» не вернул к первому вопросу")
	}
}

// Мусор в форме игнорируется: чужие факты и значения не из словаря.
func TestRejectsGarbage(t *testing.T) {
	_, s, h := setup(t)
	form := url.Values{}
	form.Set("f_international", "maybe")
	form.Set("f_evil", "yes")
	form.Set("q", "transport")
	form.Set("v", "teleport")
	code, body := do(t, h, "POST", "/step", form)
	if code != 200 {
		t.Fatalf("код %d", code)
	}
	if !strings.Contains(body, s.Question("international").Text) {
		t.Error("некорректные ответы не отброшены")
	}
	if strings.Contains(body, "evil") || strings.Contains(body, "teleport") {
		t.Error("мусор попал в страницу")
	}
}

func TestMethods(t *testing.T) {
	_, _, h := setup(t)
	// Проверка счётчика Метрики открывает адрес по ссылке — уводим на начало с тем же запросом.
	req := httptest.NewRequest("GET", "/step?_ym_status-check=1&_ym_lang=ru", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?_ym_status-check=1&_ym_lang=ru" {
		t.Errorf("GET /step → %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if code, _ := do(t, h, "PUT", "/step", nil); code != http.StatusMethodNotAllowed {
		t.Errorf("PUT /step → %d", code)
	}
	if code, _ := do(t, h, "GET", "/nope", nil); code != http.StatusNotFound {
		t.Errorf("GET /nope → %d", code)
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	_, _, h := setup(t)
	req := httptest.NewRequest("GET", "/robots.txt", nil)
	req.Host = "aktura.ru"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Sitemap: https://aktura.ru/sitemap.xml") ||
		!strings.Contains(rec.Body.String(), "Disallow: /step") {
		t.Errorf("robots.txt: %d %q", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", "/sitemap.xml", nil)
	req.Host = "aktura.ru"
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "<loc>https://aktura.ru/</loc>") {
		t.Errorf("sitemap.xml: %d %q", rec.Code, rec.Body.String())
	}
}

func TestHealthz(t *testing.T) {
	_, _, h := setup(t)
	code, body := do(t, h, "GET", "/healthz", nil)
	if code != 200 || !strings.Contains(body, "ok") {
		t.Errorf("/healthz → %d %q", code, body)
	}
}

func TestSecurityHeaders(t *testing.T) {
	_, _, h := setup(t)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	for k, want := range map[string]string{
		"Content-Security-Policy": "default-src 'none'",
		"X-Content-Type-Options":  "nosniff",
		"Referrer-Policy":         "no-referrer",
		"X-Frame-Options":         "DENY",
	} {
		if got := rec.Header().Get(k); !strings.Contains(got, want) {
			t.Errorf("%s = %q, ожидалось %q", k, got, want)
		}
	}
}

func TestBotLinks(t *testing.T) {
	b, s, plain := setup(t)
	if _, body := do(t, plain, "GET", "/", nil); strings.Contains(body, "Опросник в мессенджере") {
		t.Error("ссылки на ботов без настройки")
	}
	h, err := web.New(b, s, web.WithBots("https://max.ru/aktura_bot", "http://evil.example/"))
	if err != nil {
		t.Fatal(err)
	}
	_, body := do(t, h, "GET", "/", nil)
	if !strings.Contains(body, `href="https://max.ru/aktura_bot?start=site"`) || strings.Contains(body, "evil.example") {
		t.Errorf("ссылки на ботов: %s", body[strings.Index(body, "Опросник в мессенджере")-20:][:200])
	}
}
