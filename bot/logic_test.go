package bot

import (
	"context"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf16"

	"gopkg.in/yaml.v3"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/present"
	"github.com/RunetX/aktura-etrn/quiz"
	"github.com/RunetX/aktura-etrn/store"
)

// fake — транспорт в памяти: сообщения по ID, последний тост.
type fake struct {
	n     int
	msgs  map[string]msg
	order []string
	toast string
}

type msg struct {
	text string
	kb   *Keyboard
}

func newFake() *fake { return &fake{msgs: map[string]msg{}} }

func (f *fake) Send(_ context.Context, _ int64, text string, kb *Keyboard) string {
	f.n++
	id := strconv.Itoa(f.n)
	f.msgs[id] = msg{text, kb}
	f.order = append(f.order, id)
	return id
}

func (f *fake) Edit(_ context.Context, _ int64, id, text string, kb *Keyboard) {
	if _, ok := f.msgs[id]; !ok {
		panic("правка несуществующего сообщения " + id)
	}
	f.msgs[id] = msg{text, kb}
}

func (f *fake) AnswerCallback(_ context.Context, _ Callback, toast string) { f.toast = toast }

// active — последнее сообщение с кнопками.
func (f *fake) active() (string, msg) {
	for i := len(f.order) - 1; i >= 0; i-- {
		if m := f.msgs[f.order[i]]; !m.kb.Empty() {
			return f.order[i], m
		}
	}
	return "", msg{}
}

const user = 42

func setup(t *testing.T, st *store.Store) (*Logic, *fake) {
	t.Helper()
	b, err := engine.Load("../rules/rules.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s, err := quiz.Load("../rules/questions.yaml")
	if err != nil {
		t.Fatal(err)
	}
	tr := newFake()
	return New(present.New(b, s), tr, Config{Via: "test", Store: st, Site: "https://aktura.ru"}), tr
}

// press нажимает кнопку с данным payload на действующем сообщении.
func press(t *testing.T, l *Logic, tr *fake, payload string) {
	t.Helper()
	id, m := tr.active()
	for _, row := range m.kb.Rows {
		for _, b := range row {
			if b.Payload == payload {
				tr.toast = ""
				l.Callback(context.Background(), Callback{UserID: user, MessageID: id, Payload: payload})
				return
			}
		}
	}
	t.Fatalf("нет кнопки %q в сообщении:\n%s", payload, m.text)
}

// answer отвечает на текущий вопрос так, как ответил бы знающий факты f.
func answer(t *testing.T, l *Logic, tr *fake, q *quiz.Question, f engine.Facts) {
	t.Helper()
	if q.Group != "" {
		for _, k := range q.Facts() {
			if f[k] == "yes" {
				press(t, l, tr, Pack(vToggle, k))
			}
		}
		press(t, l, tr, Pack(vDone, q.ID()))
		return
	}
	if v, ok := f[q.Fact]; ok && v != engine.Unknown {
		press(t, l, tr, Pack(vAnswer, q.ID()+":"+v))
	} else {
		press(t, l, tr, Pack(vUnknown, q.ID()))
	}
}

func (l *Logic) current(t *testing.T) *session {
	t.Helper()
	s := l.session(user, false)
	if s == nil {
		t.Fatal("нет сессии")
	}
	return s
}

func run(t *testing.T, l *Logic, tr *fake, id string, f engine.Facts) {
	t.Helper()
	l.Start(context.Background(), user, "")
	for step := 0; ; step++ {
		s := l.current(t)
		q := l.pr.Set.Next(l.pr.Base, s.st.Answers)
		if q == nil {
			break
		}
		if step > len(l.pr.Set.Questions) {
			t.Fatalf("%s: опрос не завершается", id)
		}
		answer(t, l, tr, q, f)
	}
	s := l.current(t)
	if !s.result {
		t.Fatalf("%s: нет результата", id)
	}
	want, got := l.pr.Base.Evaluate(f), l.pr.Base.Evaluate(s.st.Answers)
	if !slices.Equal(want.AllRuleIDs(), got.AllRuleIDs()) || want.CarriageDoc != got.CarriageDoc ||
		want.ForwardingForm != got.ForwardingForm || !slices.Equal(want.ForwardingDocs, got.ForwardingDocs) ||
		want.Form != got.Form || want.Certainty != got.Certainty || !maps.Equal(want.Roles, got.Roles) {
		t.Errorf("%s: бот дал %v %s %s, движок напрямую — %v %s %s", id,
			got.RuleIDs, got.CarriageDoc, got.Certainty, want.RuleIDs, want.CarriageDoc, want.Certainty)
	}
	r := l.pr.Result(s.st.Answers)
	var all strings.Builder
	for _, id := range tr.order {
		all.WriteString(tr.msgs[id].text + "\n")
	}
	if r.Found && !strings.Contains(all.String(), r.CarriageDoc) {
		t.Errorf("%s: в сообщениях нет итога %q", id, r.CarriageDoc)
	}
	for _, d := range append(slices.Clone(r.FwdDocs), r.FwdForm) {
		if d != "" && !strings.Contains(all.String(), d) {
			t.Errorf("%s: в сообщениях нет экспедиторского документа %q", id, d)
		}
	}
	for _, rule := range r.Rules {
		if !strings.Contains(all.String(), rule.ID+". ") {
			t.Errorf("%s: в сообщениях нет основания %s", id, rule.ID)
		}
	}
	for _, m := range tr.msgs {
		if n := len(utf16.Encode([]rune(m.text))); n > maxMessage {
			t.Errorf("%s: сообщение длиной %d", id, n)
		}
	}
}

func TestSimulateCases(t *testing.T) {
	var tests struct {
		Cases []struct {
			ID    string            `yaml:"id"`
			Facts map[string]string `yaml:"facts"`
		} `yaml:"cases"`
	}
	var golden struct {
		Situations []struct {
			ID      string            `yaml:"id"`
			Facts   map[string]string `yaml:"facts"`
			Assumed map[string]string `yaml:"assumed"`
		} `yaml:"situations"`
	}
	for path, out := range map[string]any{"../rules/tests.yaml": &tests, "../rules/golden.yaml": &golden} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, out); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, c := range tests.Cases {
		l, tr := setup(t, nil)
		run(t, l, tr, c.ID, engine.Facts(c.Facts))
		n++
	}
	for _, g := range golden.Situations {
		f := engine.Facts{}
		maps.Copy(f, g.Facts)
		maps.Copy(f, g.Assumed)
		l, tr := setup(t, nil)
		run(t, l, tr, g.ID, f)
		n++
	}
	if n < 60 {
		t.Errorf("прогнано только %d сценариев", n)
	}
}

// Все payload, какие может собрать бот, укладываются в предел Telegram.
func TestPayloadLimit(t *testing.T) {
	l, _ := setup(t, nil)
	check := func(p string) {
		if len(p) > PayloadLimit {
			t.Errorf("payload %q длиной %d", p, len(p))
		}
		if v, _, ok := Parse(p); !ok || v == "" {
			t.Errorf("payload %q не разбирается", p)
		}
	}
	for _, q := range l.pr.Set.Questions {
		check(Pack(vUnknown, q.ID()))
		check(Pack(vDone, q.ID()))
		for _, o := range q.Options {
			check(Pack(vAnswer, q.ID()+":"+o.Value))
			if o.Fact != "" {
				check(Pack(vToggle, o.Fact))
			}
		}
	}
	check(Pack(vBack, "999"))
	check(Pack(vWrong, "999"))
}

func TestBackAndStale(t *testing.T) {
	l, tr := setup(t, nil)
	ctx := context.Background()
	l.Start(ctx, user, "")
	firstID, first := tr.active()
	q := l.pr.Set.Next(l.pr.Base, engine.Facts{})
	o := q.Options[0]
	press(t, l, tr, Pack(vAnswer, q.ID()+":"+o.Value))
	if s := l.current(t); len(s.st.History) != 1 {
		t.Fatalf("история %v", s.st.History)
	}
	// Вопрос правится на месте: сообщение то же, кнопка «Назад» появилась.
	id, m := tr.active()
	if id != firstID || m.text == first.text || !strings.Contains(m.text, "Вопрос 2") {
		t.Fatalf("второй вопрос не на месте первого: %s %q", id, m.text)
	}
	press(t, l, tr, Pack(vBack, "1"))
	if s := l.current(t); len(s.st.History) != 0 || len(s.st.Answers) != 0 {
		t.Fatalf("после «Назад» %v %v", s.st.History, s.st.Answers)
	}
	// Повторное нажатие устаревшей «Назад» ничего не меняет.
	l.Callback(ctx, Callback{UserID: user, MessageID: id, Payload: Pack(vBack, "1")})
	if tr.toast == "" {
		t.Error("нет тоста на устаревшую кнопку")
	}
	// Кнопка из чужого (старого) сообщения.
	l.Callback(ctx, Callback{UserID: user, MessageID: "999", Payload: Pack(vAnswer, q.ID()+":"+o.Value)})
	if tr.toast == "" || len(l.current(t).st.History) != 0 {
		t.Error("нажатие в старом сообщении сработало")
	}
	// Ответ на другой вопрос, чем текущий.
	l.Callback(ctx, Callback{UserID: user, MessageID: id, Payload: Pack(vAnswer, "transport:auto")})
	if tr.toast == "" || len(l.current(t).st.History) != 0 {
		t.Error("ответ не на текущий вопрос сработал")
	}
	// Мусор.
	l.Callback(ctx, Callback{UserID: user, MessageID: id, Payload: "2:a:x"})
	if tr.toast == "" {
		t.Error("нет тоста на чужую версию")
	}
}

func TestExpiredSession(t *testing.T) {
	l, tr := setup(t, nil)
	now := time.Now()
	l.now = func() time.Time { return now }
	l.Start(context.Background(), user, "")
	id, _ := tr.active()
	now = now.Add(25 * time.Hour)
	l.Purge()
	if l.session(user, false) != nil {
		t.Fatal("сессия не удалена")
	}
	q := l.pr.Set.Questions[0]
	l.Callback(context.Background(), Callback{UserID: user, MessageID: id, Payload: Pack(vAnswer, q.ID()+":"+q.Options[0].Value)})
	s := l.current(t)
	if len(s.st.History) != 0 || s.msgID == id {
		t.Error("после истечения опрос не начат заново")
	}
}

func TestTextNotStored(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, tr := setup(t, st)
	l.Text(context.Background(), user, "ООО Ромашка, Иванов +7 999 123-45-67")
	if len(tr.order) != 1 || strings.Contains(tr.msgs[tr.order[0]].text, "Ромашка") {
		t.Errorf("ответ на текст: %v", tr.msgs)
	}
	if l.session(user, false) != nil {
		t.Error("текст завёл сессию")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("текст попал в файлы: %v", entries)
	}
}

func TestResultLogAndFeedback(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, tr := setup(t, st)
	run(t, l, tr, "all-unknown", engine.Facts{})
	s := l.current(t)
	press(t, l, tr, Pack(vWrong, strconv.Itoa(len(s.st.History))))
	if !s.sent || tr.toast == "" {
		t.Fatal("отзыв не принят")
	}
	for _, name := range []string{"situations.jsonl", "feedback.jsonl"} {
		data, err := os.ReadFile(dir + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), strconv.Itoa(user)) && !strings.Contains(string(data), `"test"`) {
			t.Errorf("%s: похоже на ID пользователя: %s", name, data)
		}
		if name == "situations.jsonl" && !strings.Contains(string(data), `"via":"test"`) {
			t.Errorf("журнал без канала: %s", data)
		}
	}
	// После отзыва кнопки «не так» нет, «Назад» ведёт к вопросу новым сообщением.
	_, m := tr.active()
	for _, row := range m.kb.Rows {
		for _, b := range row {
			if b.Payload == Pack(vWrong, strconv.Itoa(len(s.st.History))) {
				t.Error("кнопка отзыва осталась")
			}
		}
	}
	before := len(tr.order)
	press(t, l, tr, Pack(vBack, strconv.Itoa(len(s.st.History))))
	if len(tr.order) != before+1 || l.current(t).result {
		t.Error("«Назад» с результата не показал вопрос")
	}
}

func TestGroupToggle(t *testing.T) {
	l, tr := setup(t, nil)
	var g *quiz.Question
	for i := range l.pr.Set.Questions {
		if l.pr.Set.Questions[i].Group == "exceptions" {
			g = &l.pr.Set.Questions[i]
		}
	}
	// Доводим опрос до группы исключений ответами кейса T-101 без самого исключения.
	facts := engine.Facts{"transport": "hired_carrier", "carriage_customer": "seller", "international": "no",
		"tmc_accounting": "yes", "owner_escort": "no"}
	l.Start(context.Background(), user, "")
	for {
		s := l.current(t)
		q := l.pr.Set.Next(l.pr.Base, s.st.Answers)
		if q == nil {
			t.Fatal("группа исключений не встретилась")
		}
		if q.ID() == g.ID() {
			break
		}
		answer(t, l, tr, q, facts)
	}
	f := g.Options[0].Fact
	press(t, l, tr, Pack(vToggle, f))
	press(t, l, tr, Pack(vToggle, f))
	press(t, l, tr, Pack(vToggle, f))
	_, m := tr.active()
	if !strings.Contains(m.text, "☑ 1.") {
		t.Errorf("галочка не видна:\n%s", m.text)
	}
	for _, o := range g.Options {
		if o.Hint != "" && !strings.Contains(m.text, o.Hint) {
			t.Errorf("нет подсказки к %s", o.Fact)
		}
	}
	press(t, l, tr, Pack(vDone, g.ID()))
	s := l.current(t)
	if s.st.Answers[f] != "yes" || s.st.Answers[g.Options[1].Fact] != "no" {
		t.Errorf("ответы группы %v", s.st.Answers)
	}
}

// Вопрос не режется на части: каждый, со всеми подсказками и отмеченными галочками, — одно сообщение.
func TestQuestionsFit(t *testing.T) {
	l, _ := setup(t, nil)
	for i := range l.pr.Set.Questions {
		q := &l.pr.Set.Questions[i]
		s := &session{checked: map[string]bool{}}
		for _, o := range q.Options {
			s.checked[o.Fact] = true
		}
		text, _ := l.question(s, q)
		if n := len(utf16.Encode([]rune(text))); n > maxMessage {
			t.Errorf("%s: сообщение длиной %d", q.ID(), n)
		}
	}
}

func TestSplit(t *testing.T) {
	text := strings.Repeat("абзац текста\n", 500) + strings.Repeat("я", 5000)
	parts := split(text, 1000)
	for _, p := range parts {
		if n := len(utf16.Encode([]rune(p))); n > 1000 || n == 0 {
			t.Errorf("часть длиной %d", n)
		}
	}
	if got := strings.ReplaceAll(strings.Join(parts, ""), "\n", ""); got != strings.ReplaceAll(text, "\n", "") {
		t.Error("при разбиении потерян текст")
	}
}

// Метка ссылки: из списка — как есть, прочая — other, «Заново» её сохраняет; в журнал —
// только метка, не строка из ссылки.
func TestSource(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, tr := setup(t, st)
	l.cfg.Sources = []string{"klerk"}
	ctx := context.Background()
	for raw, want := range map[string]string{"": "", "klerk": "klerk", "KLERK": "klerk", "site": "site", "ivanov_petr": "other"} {
		if got := l.source(raw); got != want {
			t.Errorf("метка %q → %q, ожидалось %q", raw, got, want)
		}
	}
	l.Text(ctx, user, "/start klerk")
	if s := l.current(t); s.source != "klerk" {
		t.Fatalf("метка после /start klerk: %q", s.source)
	}
	l.Callback(ctx, Callback{UserID: user, MessageID: "старое", Payload: Pack(vRestart, "")})
	if s := l.current(t); s.source != "klerk" {
		t.Fatalf("«Заново» потеряло метку: %q", s.source)
	}
	l.Start(ctx, user, "ivanov_petr")
	for {
		s := l.current(t)
		q := l.pr.Set.Next(l.pr.Base, s.st.Answers)
		if q == nil {
			break
		}
		answer(t, l, tr, q, engine.Facts{})
	}
	data, err := os.ReadFile(dir + "/situations.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"source":"other"`) || strings.Contains(string(data), "ivanov") {
		t.Errorf("журнал: %s", data)
	}
}
