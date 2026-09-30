package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/web"
)

type fakeModel struct {
	reply string
	err   error
	calls int
}

func (f *fakeModel) Complete(context.Context, string, string) (string, error) {
	f.calls++
	return f.reply, f.err
}

func setupAI(t *testing.T, m extract.Model, perMinute int) http.Handler {
	t.Helper()
	b, s, _ := setup(t)
	h, err := web.New(b, s, web.WithExtractor(extract.New(m, s), "тестовую модель", perMinute, 1000, time.Second), web.WithOperator(testOp))
	if err != nil {
		t.Fatal(err)
	}
	return h
}

var testOp = web.Operator{Name: "Иван Тестов", Contact: "https://max.ru/test"}

// withConsent — форма описания (галочки согласия больше нет, политика, редакция 2).
func withConsent(text string) url.Values {
	return url.Values{"text": {text}}
}

const story = "Продавец нанял ИП-перевозчика довезти груз покупателю."

func TestNoAIWithoutModel(t *testing.T) {
	_, _, h := setup(t)
	if _, body := do(t, h, "GET", "/", nil); strings.Contains(body, "/describe") {
		t.Error("форма описания видна без модели")
	}
	if code, _ := do(t, h, "POST", "/describe", url.Values{"text": {story}}); code != http.StatusNotFound {
		t.Errorf("POST /describe без модели → %d", code)
	}
}

func TestDescribeConfirmThenQuiz(t *testing.T) {
	m := &fakeModel{reply: `{"facts":[
		{"fact":"transport","value":"hired_carrier","quote":"нанял ИП-перевозчика"},
		{"fact":"carriage_customer","value":"seller","quote":"Продавец нанял"},
		{"fact":"international","value":"no","quote":"по России"}]}`}
	h := setupAI(t, m, 10)

	_, body := do(t, h, "GET", "/", nil)
	if !strings.Contains(body, `action="/describe"`) || !strings.Contains(body, "тестовую модель") {
		t.Fatal("на первом шаге нет формы описания")
	}

	code, body := do(t, h, "POST", "/describe", withConsent(story))
	if code != 200 || !strings.Contains(body, "Что понято из описания") {
		t.Fatalf("POST /describe → %d", code)
	}
	for _, want := range []string{`name="f_transport" value="hired_carrier" checked`, `name="f_carriage_customer" value="seller" checked`, "«нанял ИП-перевозчика»"} {
		if !strings.Contains(body, want) {
			t.Errorf("на экране подтверждения нет %q", want)
		}
	}
	if strings.Contains(body, "f_international") {
		t.Error("показан факт с цитатой, которой нет в тексте")
	}
	if strings.Contains(body, `name="q"`) {
		t.Error("на экране подтверждения не должно быть вопроса")
	}

	// человек снял галочку с carriage_customer — этот вопрос ему зададут
	_, body = do(t, h, "POST", "/step", url.Values{"f_transport": {"hired_carrier"}})
	if !strings.Contains(body, `name="q" value="international"`) {
		t.Error("после подтверждения опросник должен спросить недостающее, начиная с international")
	}
	_, body = do(t, h, "POST", "/step", url.Values{"f_transport": {"hired_carrier"}, "q": {"international"}, "v": {"no"}, "h": {""}})
	if !strings.Contains(body, `name="q" value="carriage_customer"`) {
		t.Error("снятый с подтверждения факт не спрошен")
	}
}

func TestDescribeFailures(t *testing.T) {
	m := &fakeModel{err: errors.New("timeout")}
	h := setupAI(t, m, 1)
	_, body := do(t, h, "POST", "/describe", withConsent("<b>Наняли ТК</b>"))
	if !strings.Contains(body, "Не удалось разобрать описание") || !strings.Contains(body, "&lt;b&gt;Наняли ТК&lt;/b&gt;</textarea>") {
		t.Error("при ошибке модели нужно сообщение и возврат текста в форму (экранированного)")
	}
	if !strings.Contains(body, `name="q" value="international"`) {
		t.Error("при ошибке модели опросник должен остаться доступен")
	}
	_, body = do(t, h, "POST", "/describe", withConsent("Наняли ТК"))
	if !strings.Contains(body, "перегружен") || m.calls != 1 {
		t.Errorf("предел обращений не сработал: вызовов %d", m.calls)
	}
	_, body = do(t, h, "POST", "/describe", withConsent("   "))
	if strings.Contains(body, "Что понято") || m.calls != 1 {
		t.Error("пустое описание не отправляется модели")
	}
	if code, _ := do(t, h, "GET", "/describe", nil); code != http.StatusSeeOther {
		t.Errorf("GET /describe → %d", code)
	}
}

func TestDescribeNothingUnderstood(t *testing.T) {
	h := setupAI(t, &fakeModel{reply: `{"facts":[]}`}, 10)
	_, body := do(t, h, "POST", "/describe", withConsent("Помогите с накладной"))
	if !strings.Contains(body, "не удалось однозначно понять") {
		t.Error("нет сообщения, что ничего не понято")
	}
}

// Галочка исключения, найденная в описании, отмечена в групповом вопросе заранее.
func TestGroupPrechecked(t *testing.T) {
	_, _, h := setup(t)
	answers := engine.Facts{"international": "no", "transport": "hired_carrier", "carriage_customer": "seller",
		"tmc_accounting": "yes", "owner_escort": "no", "loader": "shipper", "consignee_party": "customer_buyer",
		"receiver_representative": "no", "exc_precious": "yes"}
	_, body := do(t, h, "POST", "/step", state(answers, nil))
	if !strings.Contains(body, `value="exc_precious" checked`) {
		t.Error("галочка из описания не отмечена в групповом вопросе")
	}
	if strings.Contains(body, `value="exc_mod" checked`) {
		t.Error("отмечено лишнее")
	}
}
