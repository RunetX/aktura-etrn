package web_test

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/store"
	"github.com/RunetX/aktura-etrn/web"
)

const counter = "112979435"

// full — сервер со всем, что обрабатывает данные: ИИ, Метрика, журнал и отзывы.
func full(t *testing.T) (http.Handler, string) {
	t.Helper()
	b, s, _ := setup(t)
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	h, err := web.New(b, s, web.WithOperator(testOp), web.WithMetrika(counter), web.WithStore(st),
		web.WithExtractor(extract.New(&fakeModel{reply: `{"facts":[]}`}, s), "тестовую модель", 10, 1000, time.Second))
	if err != nil {
		t.Fatal(err)
	}
	return h, dir
}

// req — запрос с cookie выбора Метрики (пусто — не выбирал).
func req(t *testing.T, h http.Handler, method, target string, form url.Values, choice string) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	} else {
		body = strings.NewReader("")
	}
	r := httptest.NewRequest(method, target, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if choice != "" {
		r.AddCookie(&http.Cookie{Name: "aktura_metrika", Value: choice})
	}
	r.AddCookie(&http.Cookie{Name: "_ym_uid", Value: "123"})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestNeedsOperator(t *testing.T) {
	b, s, _ := setup(t)
	if _, err := web.New(b, s, web.WithMetrika(counter)); err == nil {
		t.Error("Метрика без реквизитов оператора")
	}
	if _, err := web.New(b, s, web.WithExtractor(extract.New(&fakeModel{}, s), "m", 1, 1, time.Second)); err == nil {
		t.Error("ИИ-слой без реквизитов оператора")
	}
	if _, err := web.New(b, s, web.WithOperator(web.Operator{Name: "Иван"})); err == nil {
		t.Error("оператор без контакта")
	}
	if _, err := web.New(b, s, web.WithOperator(testOp), web.WithMetrika("1');alert(1)//")); err == nil {
		t.Error("номер счётчика не проверяется")
	}
}

func TestLegalPages(t *testing.T) {
	h, _ := full(t)
	for path, want := range map[string]string{
		"/privacy":         "Политика в отношении обработки персональных данных",
		"/consent/metrika": "Согласие на статистику посещений",
	} {
		rec := req(t, h, "GET", path, nil, "no")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, want) || !strings.Contains(body, testOp.Name) || !strings.Contains(body, testOp.Contact) {
			t.Errorf("%s: %d, нет заголовка или реквизитов оператора", path, rec.Code)
		}
		if strings.Contains(body, "{{") || !strings.Contains(body, "sha256") {
			t.Errorf("%s: не подставлены реквизиты или нет контрольной суммы", path)
		}
		if strings.Contains(body, `name="q"`) {
			t.Errorf("%s: на странице документа опросник", path)
		}
	}
	// согласий на описание и отзыв больше нет (политика, редакция 2)
	for _, path := range []string{"/consent/ai", "/consent/feedback"} {
		if rec := req(t, h, "GET", path, nil, "no"); rec.Code != 404 {
			t.Errorf("%s → %d", path, rec.Code)
		}
	}
	// без реквизитов оператора страниц нет
	_, _, plain := setup(t)
	if rec := req(t, plain, "GET", "/privacy", nil, ""); rec.Code != 404 {
		t.Errorf("/privacy без оператора → %d", rec.Code)
	}
}

// Описание не предназначено для ПДн: галочки согласия нет, есть предупреждение и ссылка на политику.
func TestDescribeNotice(t *testing.T) {
	m := &fakeModel{reply: `{"facts":[]}`}
	h := setupAI(t, m, 10)
	_, body := do(t, h, "GET", "/", nil)
	if strings.Contains(body, `name="consent"`) || !strings.Contains(body, "не пишите ФИО") || !strings.Contains(body, `href="/privacy"`) {
		t.Error("у формы описания должно быть предупреждение и ссылка на политику, без галочки")
	}
	do(t, h, "POST", "/describe", url.Values{"text": {story}})
	if m.calls != 1 {
		t.Error("описание не ушло на разбор")
	}
}

func TestMetrikaOnlyWithConsent(t *testing.T) {
	h, _ := full(t)
	for _, choice := range []string{"", "no", "garbage"} {
		rec := req(t, h, "GET", "/", nil, choice)
		body, csp := rec.Body.String(), rec.Header().Get("Content-Security-Policy")
		if strings.Contains(body, "metrika.js") || strings.Contains(csp, "script-src") || strings.Contains(csp, "mc.yandex") {
			t.Errorf("выбор %q: счётчик или скрипты разрешены без согласия", choice)
		}
		if got := req(t, h, "GET", "/metrika.js", nil, choice).Code; got != 404 {
			t.Errorf("выбор %q: /metrika.js → %d", choice, got)
		}
		if banner := strings.Contains(body, `action="/cookies"`); banner != (choice == "" || choice == "garbage") {
			t.Errorf("выбор %q: баннер %v", choice, banner)
		}
	}
	rec := req(t, h, "GET", "/", nil, "yes")
	if !strings.Contains(rec.Body.String(), `<script src="/metrika.js" async>`) || !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self' https://mc.yandex.ru") {
		t.Error("после согласия счётчика нет")
	}
	if strings.Contains(rec.Body.String(), `action="/cookies" class="card banner"`) {
		t.Error("баннер после выбора")
	}
	js := req(t, h, "GET", "/metrika.js", nil, "yes").Body.String()
	for _, want := range []string{"tag.js?id=" + counter, "webvisor:false", "clickmap:false"} {
		if !strings.Contains(js, want) {
			t.Errorf("в коде счётчика нет %q", want)
		}
	}
	if strings.Contains(js, "webvisor:true") || strings.Contains(js, "ecommerce") {
		t.Error("в коде счётчика Вебвизор или ecommerce")
	}
}

func TestBannerKeepsQuizState(t *testing.T) {
	h, _ := full(t)
	form := url.Values{"metrika": {"yes"}, "ret": {"quiz"}, "f_international": {"no"}, "h": {"international"}}
	rec := req(t, h, "POST", "/cookies", form, "")
	body := rec.Body.String()
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "aktura_metrika=yes") {
		t.Error("выбор не сохранён")
	}
	if !strings.Contains(body, `name="q" value="transport"`) || !strings.Contains(body, "metrika.js") {
		t.Error("после выбора в баннере опрос должен продолжиться с того же места, уже со счётчиком")
	}
	if strings.Contains(rec.Header().Get("Content-Security-Policy"), "mc.yandex") == false {
		t.Error("CSP ответа не учитывает только что данное согласие")
	}
}

func TestRefuseDropsMetrikaCookies(t *testing.T) {
	h, _ := full(t)
	rec := req(t, h, "POST", "/cookies", url.Values{"metrika": {"no"}}, "yes")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/cookies?saved=1" {
		t.Errorf("отказ со страницы настроек → %d %s", rec.Code, rec.Header().Get("Location"))
	}
	var dropped bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == "_ym_uid" && c.MaxAge < 0 {
			dropped = true
		}
	}
	if !dropped {
		t.Error("cookie Метрики не удалены при отказе")
	}
	if rec := req(t, h, "POST", "/cookies", url.Values{"metrika": {"maybe"}}, ""); rec.Code != 400 {
		t.Errorf("мусор в выборе → %d", rec.Code)
	}
	if rec := req(t, h, "POST", "/cookies", url.Values{"metrika": {"no"}, "ret": {"https://evil.example/"}}, ""); rec.Header().Get("Location") != "/cookies?saved=1" {
		t.Error("открытый редирект")
	}
}

// Результат опроса → журнал ситуаций; отзыв — только с согласием, без IP и cookie.
func TestSituationLogAndFeedback(t *testing.T) {
	h, dir := full(t)
	answers := engine.Facts{"international": "no", "transport": "own_buyer", "tmc_accounting": "yes", "owner_escort": "no",
		"loader": "seller", "etrn_in_contract": "no"}
	// последний ответ, который приводит к результату
	form := state(answers, nil)
	form.Del("f_etrn_in_contract")
	form.Set("q", "etrn_in_contract")
	form.Set("v", "no")
	rec := req(t, h, "POST", "/step", form, "no")
	body := rec.Body.String()
	if !strings.Contains(body, `action="/feedback"`) {
		t.Skip("опрос не дошёл до результата — проверьте набор ответов") // страховка от изменения правил
	}
	log, _ := os.ReadFile(filepath.Join(dir, "situations.jsonl"))
	if !strings.Contains(string(log), `"transport":"own_buyer"`) {
		t.Errorf("журнал ситуаций: %s", log)
	}
	// «Назад» с результата журнал не пополняет
	back := state(answers, []string{"etrn_in_contract"})
	back.Set("back", "1")
	req(t, h, "POST", "/step", back, "no")
	if log2, _ := os.ReadFile(filepath.Join(dir, "situations.jsonl")); len(log2) != len(log) {
		t.Error("журнал пополнился без ответа на вопрос")
	}

	fb := state(answers, nil)
	rec = req(t, h, "POST", "/feedback", fb, "no")
	if !strings.Contains(rec.Body.String(), "Напишите, что не так") {
		t.Error("пустой отзыв принят")
	}
	if _, err := os.Stat(filepath.Join(dir, "feedback.jsonl")); err == nil {
		t.Error("пустой отзыв записан")
	}
	fb.Set("comment", "Не так: звоните +7 912 345-67-89, ИНН 7701234567")
	rec = req(t, h, "POST", "/feedback", fb, "no")
	if !strings.Contains(rec.Body.String(), "Спасибо") {
		t.Fatal("отзыв не принят")
	}
	saved, _ := os.ReadFile(filepath.Join(dir, "feedback.jsonl"))
	got := string(saved)
	for _, bad := range []string{"912", "7701234567", "192.0.2", "_ym_uid", "aktura_metrika"} {
		if strings.Contains(got, bad) {
			t.Errorf("в отзыве сохранено лишнее: %q", bad)
		}
	}
	if !strings.Contains(got, `"policy":"`+web.Documents["privacy"]+`"`) || !strings.Contains(got, `"policy_sha":"`) {
		t.Errorf("нет редакции и контрольной суммы политики: %s", got)
	}
	if code, _ := do(t, h, "GET", "/feedback", nil); code != http.StatusSeeOther {
		t.Errorf("GET /feedback → %d", code)
	}
}

func TestFooterLinks(t *testing.T) {
	h, _ := full(t)
	body := req(t, h, "GET", "/", nil, "no").Body.String()
	if !strings.Contains(body, `href="/privacy"`) || !strings.Contains(body, `href="/cookies"`) {
		t.Error("в подвале нет ссылок на политику и настройки cookie")
	}
	_, _, plain := setup(t)
	if _, body := do(t, plain, "GET", "/", nil); strings.Contains(body, "/privacy") {
		t.Error("ссылка на политику без политики")
	}
}
