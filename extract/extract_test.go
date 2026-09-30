package extract

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/quiz"
)

const (
	questionsPath = "../rules/questions.yaml"
	goldenPath    = "../rules/golden.yaml"
)

// fake — модель с заготовленным ответом; запоминает, что ей отправили.
type fake struct {
	reply  string
	err    error
	system string
	user   string
}

func (f *fake) Complete(_ context.Context, system, user string) (string, error) {
	f.system, f.user = system, user
	return f.reply, f.err
}

func newExtractor(t *testing.T, m Model) *Extractor {
	t.Helper()
	s, err := quiz.Load(questionsPath)
	if err != nil {
		t.Fatal(err)
	}
	return New(m, s)
}

func TestPromptListsEveryQuestion(t *testing.T) {
	e := newExtractor(t, &fake{})
	for _, q := range e.set.Questions {
		if q.AI == Skip {
			for _, f := range q.Facts() {
				if strings.Contains(e.Prompt(), "fact="+f) {
					t.Errorf("факт %s помечен ai: skip, но есть в инструкции модели", f)
				}
			}
			continue
		}
		for _, f := range q.Facts() {
			if !strings.Contains(e.Prompt(), "fact="+f) {
				t.Errorf("в инструкции модели нет факта %s", f)
			}
		}
		for _, o := range q.Options {
			excluded := slices.Contains(q.AIExclude, o.Value)
			if q.Group == "" && strings.Contains(e.Prompt(), "value="+o.Value+":") == excluded {
				t.Errorf("в инструкции модели нет варианта %s=%s", q.Fact, o.Value)
			}
		}
	}
}

func TestParse(t *testing.T) {
	e := newExtractor(t, &fake{})
	text := "Продавец нанял ИП-перевозчика довезти груз покупателю. Везут по России."
	cases := []struct {
		name   string
		raw    string
		facts  engine.Facts
		reason string
	}{
		{"принят", `{"facts":[{"fact":"transport","value":"hired_carrier","quote":"нанял ИП-перевозчика"}]}`,
			engine.Facts{"transport": "hired_carrier"}, ""},
		{"регистр, ё и знаки в цитате не важны", `{"facts":[{"fact":"carriage_customer","value":"seller","quote":"продавец  НАНЯЛ ип перевозчика"}]}`,
			engine.Facts{"carriage_customer": "seller"}, ""},
		{"JSON в обёртке", "Вот ответ:\n```json\n{\"facts\":[{\"fact\":\"international\",\"value\":\"no\",\"quote\":\"Везут по России\"}]}\n```",
			engine.Facts{"international": "no"}, ""},
		{"нет такого вопроса", `{"facts":[{"fact":"carriage_doc","value":"etrn","quote":"нанял ИП-перевозчика"}]}`,
			engine.Facts{}, "нет такого вопроса"},
		{"нет такого варианта", `{"facts":[{"fact":"transport","value":"truck","quote":"нанял ИП-перевозчика"}]}`,
			engine.Facts{}, "нет такого варианта ответа"},
		{"«не знаю» не извлекается", `{"facts":[{"fact":"transport","value":"unknown","quote":"нанял ИП-перевозчика"}]}`,
			engine.Facts{}, "нет такого варианта ответа"},
		{"цитата выдумана", `{"facts":[{"fact":"transport","value":"forwarder","quote":"по договору экспедиции"}]}`,
			engine.Facts{}, "цитаты нет в тексте"},
		{"цитата пустая", `{"facts":[{"fact":"transport","value":"hired_carrier","quote":" "}]}`,
			engine.Facts{}, "цитаты нет в тексте"},
		{"ai_exclude не принимается", `{"facts":[{"fact":"transport","value":"own_company","quote":"Везут по России"}]}`,
			engine.Facts{}, "нет такого варианта ответа"},
		{"ai: skip не принимается", `{"facts":[{"fact":"etrn_in_contract","value":"yes","quote":"Везут по России"}]}`,
			engine.Facts{}, "нет такого вопроса"},
		{"исключения ИИ не отмечает", `{"facts":[{"fact":"exc_precious","value":"yes","quote":"Везут по России"}]}`,
			engine.Facts{}, "нет такого вопроса"},
		{"разные ответы на один вопрос", `{"facts":[{"fact":"transport","value":"hired_carrier","quote":"нанял ИП-перевозчика"},{"fact":"transport","value":"own_seller","quote":"Продавец"}]}`,
			engine.Facts{}, "разные ответы на один вопрос"},
		{"повтор того же ответа", `{"facts":[{"fact":"transport","value":"hired_carrier","quote":"нанял"},{"fact":"transport","value":"hired_carrier","quote":"ИП-перевозчика"}]}`,
			engine.Facts{"transport": "hired_carrier"}, ""},
		{"не JSON", `Не могу ответить`, engine.Facts{}, "ответ модели — не JSON"},
		{"пусто", `{"facts":[]}`, engine.Facts{}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := e.Parse(c.raw, text)
			if len(r.Facts) != len(c.facts) {
				t.Fatalf("факты %v, нужно %v", r.Facts, c.facts)
			}
			for k, v := range c.facts {
				if r.Facts[k] != v {
					t.Errorf("%s=%q, нужно %q", k, r.Facts[k], v)
				}
			}
			if c.reason != "" && (len(r.Rejected) == 0 || r.Rejected[len(r.Rejected)-1].Reason != c.reason) {
				t.Errorf("отброшено %+v, нужна причина %q", r.Rejected, c.reason)
			}
			if len(r.Accepted) != len(r.Facts) {
				t.Errorf("принято %d предложений на %d фактов", len(r.Accepted), len(r.Facts))
			}
		})
	}
}

func TestRedact(t *testing.T) {
	cases := map[string]string{
		"пишите на Ivan.Petrov@mail.ru":       "пишите на [e-mail]",
		"звонить +7 (912) 345-67-89 после 10": "звонить [телефон] после 10",
		"тел. 8 912 345 67 89":                "тел. [телефон]",
		"ИНН 7701234567, ОГРН 1027700132195":  "ИНН [номер], ОГРН [номер]",
		"ИНН ИП 770123456789":                 "ИНН ИП [номер]",
		"р/с 40702810900000000001":            "р/с [номер]",
		"машина 20 т, 3 паллеты, 1 500 кг":    "машина 20 т, 3 паллеты, 1 500 кг",
		"договор № 15 от 01.09.2026":          "договор № 15 от 01.09.2026",
	}
	for in, want := range cases {
		if got := Redact(in); got != want {
			t.Errorf("Redact(%q) = %q, нужно %q", in, got, want)
		}
	}
}

func TestExtractSendsRedactedText(t *testing.T) {
	f := &fake{reply: `{"facts":[{"fact":"transport","value":"hired_carrier","quote":"наняли ТК"}]}`}
	e := newExtractor(t, f)
	r, err := e.Extract(context.Background(), "  Наняли ТК, ИНН 7701234567, почта a@b.ru  ")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.user, "7701234567") || strings.Contains(f.user, "a@b.ru") {
		t.Errorf("модели ушли персональные данные: %q", f.user)
	}
	if f.system != e.Prompt() {
		t.Error("модели ушла не та инструкция")
	}
	if r.Facts["transport"] != "hired_carrier" || r.Text != f.user {
		t.Errorf("результат %+v", r)
	}

	long := strings.Repeat("я", MaxText+100)
	e.Extract(context.Background(), long)
	if n := len([]rune(f.user)); n != MaxText {
		t.Errorf("модели ушло %d символов, предел %d", n, MaxText)
	}

	f.err = errors.New("timeout")
	if _, err := e.Extract(context.Background(), "текст"); err == nil {
		t.Error("ошибка модели потерялась")
	}
}

// TestGoldenOracle — проверка самой проверки: «модель», возвращающая разметку ситуации
// с цитатой из её же текста, проходит без ошибок; разметка — только из словаря опросника.
func TestGoldenOracle(t *testing.T) {
	sits, err := LoadGolden(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	e := newExtractor(t, &fake{})
	n := 0
	for _, s := range sits {
		if strings.TrimSpace(s.Story) == "" {
			t.Errorf("%s: нет story — нечего подавать ИИ-слою", s.ID)
			continue
		}
		var items []string
		for k, v := range s.Facts {
			if !slices.Contains(e.valid[k], v) {
				continue // ai: skip или ai_exclude — такой ответ ИИ не даёт
			}
			items = append(items, `{"fact":"`+k+`","value":"`+v+`","quote":"`+strings.ReplaceAll(s.Story, `"`, `\"`)+`"}`)
		}
		r := e.Parse(`{"facts":[`+strings.Join(items, ",")+`]}`, s.Story)
		for _, rj := range r.Rejected {
			t.Errorf("%s: разметка %s=%s отброшена: %s", s.ID, rj.Fact, rj.Value, rj.Reason)
		}
		for _, m := range Score(s, r.Facts) {
			if m.Verdict != Correct {
				t.Errorf("%s: %+v", s.ID, m)
			}
		}
		n++
	}
	if n < 30 {
		t.Errorf("ситуаций с текстом %d, нужно ≥30", n)
	}
}

func TestScore(t *testing.T) {
	s := Situation{
		Facts:   map[string]string{"transport": "hired_carrier"},
		Assumed: map[string]string{"international": "no"},
	}
	got := Score(s, map[string]string{"transport": "forwarder", "international": "no", "loader": "shipper"})
	want := map[string]Verdict{"transport": Wrong, "international": Correct, "loader": Extra}
	for _, m := range got {
		if want[m.Fact] != m.Verdict {
			t.Errorf("%s: %s, нужно %s", m.Fact, m.Verdict, want[m.Fact])
		}
	}
}
