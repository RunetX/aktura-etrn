package quiz_test

import (
	"maps"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/quiz"
)

const (
	rulesPath     = "../rules/rules.yaml"
	questionsPath = "../rules/questions.yaml"
	testsPath     = "../rules/tests.yaml"
	goldenPath    = "../rules/golden.yaml"
	vocabPath     = "../rules/VOCABULARY.md"
)

func load(t *testing.T) (*engine.Base, *quiz.Set) {
	t.Helper()
	b, err := engine.Load(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	s, err := quiz.Load(questionsPath)
	if err != nil {
		t.Fatal(err)
	}
	return b, s
}

// vocabFacts разбирает таблицу «Факты» в VOCABULARY.md: ключ → допустимые значения.
func vocabFacts(t *testing.T) map[string][]string {
	t.Helper()
	data, err := os.ReadFile(vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	start := strings.Index(text, "## Факты")
	end := strings.Index(text, "## Результаты")
	row := regexp.MustCompile("(?m)^\\| `([a-z_]+)` \\|.*\\| (.*) \\|$")
	tick := regexp.MustCompile("`([a-z_]+)`")
	out := map[string][]string{}
	for _, m := range row.FindAllStringSubmatch(text[start:end], -1) {
		for _, v := range tick.FindAllStringSubmatch(m[2], -1) {
			out[m[1]] = append(out[m[1]], v[1])
		}
	}
	if len(out) < 20 {
		t.Fatalf("из словаря прочитано только %d фактов", len(out))
	}
	return out
}

func TestCoverage(t *testing.T) {
	_, s := load(t)
	vocab := vocabFacts(t)
	asked := map[string]int{}
	for _, q := range s.Questions {
		if q.Text == "" || q.Hint == "" {
			t.Errorf("%v: нет текста или подсказки «как понять»", q.Facts())
		}
		if len(q.Options) == 0 {
			t.Errorf("%v: нет вариантов", q.Facts())
		}
		for _, f := range q.Facts() {
			asked[f]++
			vals, ok := vocab[f]
			if !ok {
				t.Errorf("факт %s нет в VOCABULARY.md", f)
				continue
			}
			if q.Group != "" {
				if !slices.Contains(vals, "yes") || !slices.Contains(vals, "no") {
					t.Errorf("групповой факт %s не yes/no", f)
				}
				continue
			}
			for _, o := range q.Options {
				if !slices.Contains(vals, o.Value) {
					t.Errorf("%s: значение %q не из словаря %v", f, o.Value, vals)
				}
			}
		}
	}
	for f := range vocab {
		if asked[f] != 1 {
			t.Errorf("факт %s задаётся %d раз (нужно ровно 1)", f, asked[f])
		}
	}
}

func TestFirstQuestion(t *testing.T) {
	b, s := load(t)
	q := s.Next(b, engine.Facts{})
	if q == nil || q.Fact != "international" {
		t.Fatalf("первый вопрос %+v", q)
	}
}

// oracle отвечает на вопрос так, как ответил бы пользователь, знающий факты f.
func oracle(q *quiz.Question, f engine.Facts, answers engine.Facts) {
	if q.Group != "" {
		for _, k := range q.Facts() {
			if f[k] == "yes" {
				answers[k] = "yes"
			} else {
				answers[k] = "no"
			}
		}
		return
	}
	if v, ok := f[q.Fact]; ok {
		answers[q.Fact] = v
	} else {
		answers[q.Fact] = engine.Unknown
	}
}

func simulate(t *testing.T, b *engine.Base, s *quiz.Set, id string, f engine.Facts) {
	t.Helper()
	answers := engine.Facts{}
	seen := map[string]bool{}
	for step := 0; ; step++ {
		q := s.Next(b, answers)
		if q == nil {
			break
		}
		key := strings.Join(q.Facts(), ",")
		if seen[key] {
			t.Fatalf("%s: вопрос %s задан повторно", id, key)
		}
		seen[key] = true
		if step > len(s.Questions) {
			t.Fatalf("%s: опрос не завершается", id)
		}
		oracle(q, f, answers)
	}
	want, got := b.Evaluate(f), b.Evaluate(answers)
	if !slices.Equal(want.RuleIDs, got.RuleIDs) || want.CarriageDoc != got.CarriageDoc ||
		want.Form != got.Form || want.Certainty != got.Certainty || !maps.Equal(want.Roles, got.Roles) {
		t.Errorf("%s: опрос дал %v %s %s %s %v, движок напрямую — %v %s %s %s %v", id,
			got.RuleIDs, got.CarriageDoc, got.Form, got.Certainty, got.Roles,
			want.RuleIDs, want.CarriageDoc, want.Form, want.Certainty, want.Roles)
	}
	if !slices.Equal(want.ForwardingDocs, got.ForwardingDocs) || want.ForwardingForm != got.ForwardingForm {
		t.Errorf("%s: экспедиторские документы по опросу %v %s, напрямую — %v %s", id,
			got.ForwardingDocs, got.ForwardingForm, want.ForwardingDocs, want.ForwardingForm)
	}
	// Уточнения остаются и без сценария ЭТрН — это исключения приказа № 115 (экспедиторские документы).
	if len(got.Pending) > 0 {
		t.Errorf("%s: опрос завершён, но остались уточнения %v", id, got.Pending)
	}
}

func TestSimulateCases(t *testing.T) {
	b, s := load(t)
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
	for path, out := range map[string]any{testsPath: &tests, goldenPath: &golden} {
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
		simulate(t, b, s, c.ID, engine.Facts(c.Facts))
		n++
	}
	for _, g := range golden.Situations {
		f := engine.Facts{}
		maps.Copy(f, g.Facts)
		maps.Copy(f, g.Assumed)
		simulate(t, b, s, g.ID, f)
		n++
	}
	if n < 60 {
		t.Errorf("прогнано только %d сценариев", n)
	}
}

// «Не знаю» на всё: опрос завершается, движок честно говорит, что правило не найдено.
func TestAllUnknown(t *testing.T) {
	b, s := load(t)
	answers := engine.Facts{}
	for i := 0; i <= len(s.Questions); i++ {
		q := s.Next(b, answers)
		if q == nil {
			break
		}
		for _, k := range q.Facts() {
			answers[k] = engine.Unknown
		}
	}
	if s.Next(b, answers) != nil {
		t.Fatal("опрос не завершился")
	}
	if r := b.Evaluate(answers); r.RoleRule != nil || r.Certainty != "unknown" {
		t.Errorf("при «не знаю» на всё: %v %s", r.RuleIDs, r.Certainty)
	}
}

func TestLabels(t *testing.T) {
	b, s := load(t)
	for _, r := range b.Rules {
		if d := r.Then.CarriageDoc; d != "" && s.Labels.CarriageDoc[d] == "" {
			t.Errorf("%s: нет подписи для carriage_doc %s", r.ID, d)
		}
		for k, v := range r.Then.Roles {
			if k == "loader" && v == "shipper" {
				continue // ссылка «то же лицо, что грузоотправитель», движок её раскрывает
			}
			if s.Labels.RoleKeys[k] == "" || s.Labels.RoleValues[v] == "" {
				t.Errorf("%s: нет подписи для роли %s = %s", r.ID, k, v)
			}
		}
		if s.Labels.Certainty[r.Certainty] == "" {
			t.Errorf("%s: нет подписи для certainty %s", r.ID, r.Certainty)
		}
	}
	for _, v := range []string{"warehouse", "third_party", "seller", "client"} {
		if s.Labels.RoleValues[v] == "" {
			t.Errorf("нет подписи для значения роли %s", v)
		}
	}
	for _, f := range []string{"electronic", "paper_allowed", "electronic_by_contract"} {
		if s.Labels.Form[f] == "" {
			t.Errorf("нет подписи для form %s", f)
		}
		if s.Labels.FormNotes[f] == "" {
			t.Errorf("нет пояснения к form %s (бланк бумажной ТрН, распечатка ЭТрН)", f)
		}
	}
}

// Пункты исключений, в которых читатели путаются, объясняются подсказкой
// (research/2026-09_paper/REPORT.md, «Предложения по правилам и опроснику»).
func TestOptionHints(t *testing.T) {
	_, s := load(t)
	g := s.Question("exceptions")
	if g == nil {
		t.Fatal("нет группы exceptions")
	}
	want := []string{"exc_foreign_unaccredited", "exc_emergency_regime", "exc_periodicals", "exc_gossopka_failure"}
	for _, o := range g.Options {
		if slices.Contains(want, o.Fact) && o.Hint == "" {
			t.Errorf("%s: нет подсказки", o.Fact)
		}
	}
	for _, q := range s.Questions {
		for _, o := range q.Options {
			if o.Hint != "" && strings.ContainsAny(o.Hint, "<>&'\"+") {
				t.Errorf("%s: в подсказке символы, которые экранирует HTML", o.Fact+o.Value)
			}
		}
	}
}
