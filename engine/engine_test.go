package engine_test

import (
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/RunetX/aktura-etrn/engine"
)

const (
	rulesPath  = "../rules/rules.yaml"
	testsPath  = "../rules/tests.yaml"
	goldenPath = "../rules/golden.yaml"
	vocabPath  = "../rules/VOCABULARY.md"
)

// expect — ожидаемый результат кейса (tests.yaml, golden.yaml).
type expect struct {
	Rules         *[]string         `yaml:"rules"`
	CarriageDoc   string            `yaml:"carriage_doc"`
	Roles         map[string]string `yaml:"roles"`
	Form          string            `yaml:"form"`
	Certainty     string            `yaml:"certainty"`
	Ask           []string          `yaml:"ask"`
	ForwarderMark *bool             `yaml:"forwarder_mark"`
	// Поправки к шаблону подписантов (слой signers).
	SignsPatches *[]string `yaml:"signs_patches"`
	// Экспедиторские документы (слои fwd_docs и fwd_form).
	FwdRules       *[]string `yaml:"fwd_rules"`
	ForwardingDocs *[]string `yaml:"forwarding_docs"`
	ForwardingForm string    `yaml:"forwarding_form"`
}

type testCase struct {
	ID     string            `yaml:"id"`
	Facts  map[string]string `yaml:"facts"`
	Expect expect            `yaml:"expect"`
}

type goldenLeg struct {
	Name    string            `yaml:"name"`
	Facts   map[string]string `yaml:"facts"`
	Assumed map[string]string `yaml:"assumed"`
	Expect  *expect           `yaml:"expect"`
}

type goldenSituation struct {
	ID          string            `yaml:"id"`
	BeforeStart bool              `yaml:"before_start"`
	Facts       map[string]string `yaml:"facts"`
	Assumed     map[string]string `yaml:"assumed"`
	Expect      *expect           `yaml:"expect"`
	Legs        []goldenLeg       `yaml:"legs"`
	Verdict     string            `yaml:"verdict"`
}

func load(t *testing.T) *engine.Base {
	t.Helper()
	b, err := engine.Load(rulesPath)
	if err != nil {
		t.Fatalf("загрузка базы правил: %v", err)
	}
	return b
}

func readYAML(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
}

func merge(a, b map[string]string) engine.Facts {
	f := engine.Facts{}
	for k, v := range a {
		f[k] = v
	}
	for k, v := range b {
		f[k] = v
	}
	return f
}

// check сравнивает результат движка с ожиданием по тем же правилам, что и rules/tests.yaml.
func check(r engine.Result, e expect) []string {
	var bad []string
	if r.Error != "" {
		bad = append(bad, "ошибка: "+r.Error)
	}
	if e.Rules != nil && !slices.Equal(r.RuleIDs, *e.Rules) {
		bad = append(bad, fmt.Sprintf("rules %v != %v", r.RuleIDs, *e.Rules))
	}
	if e.CarriageDoc != "" && r.CarriageDoc != e.CarriageDoc {
		bad = append(bad, fmt.Sprintf("carriage_doc %q != %q", r.CarriageDoc, e.CarriageDoc))
	}
	for k, v := range e.Roles {
		if r.Roles[k] != v {
			bad = append(bad, fmt.Sprintf("roles.%s %q != %q", k, r.Roles[k], v))
		}
	}
	if e.Form != "" && r.Form != e.Form {
		bad = append(bad, fmt.Sprintf("form %q != %q", r.Form, e.Form))
	}
	if e.Certainty != "" && r.Certainty != e.Certainty {
		bad = append(bad, fmt.Sprintf("certainty %q != %q", r.Certainty, e.Certainty))
	}
	for _, k := range e.Ask {
		if !slices.Contains(r.Ask, k) {
			bad = append(bad, fmt.Sprintf("ask %v не содержит %s", r.Ask, k))
		}
	}
	if e.ForwarderMark != nil && r.ForwarderMark != *e.ForwarderMark {
		bad = append(bad, fmt.Sprintf("forwarder_mark %v != %v", r.ForwarderMark, *e.ForwarderMark))
	}
	if e.SignsPatches != nil && !slices.Equal(r.SignsPatches, *e.SignsPatches) {
		bad = append(bad, fmt.Sprintf("signs_patches %v != %v", r.SignsPatches, *e.SignsPatches))
	}
	if e.FwdRules != nil && !slices.Equal(r.FwdRuleIDs(), *e.FwdRules) {
		bad = append(bad, fmt.Sprintf("fwd_rules %v != %v", r.FwdRuleIDs(), *e.FwdRules))
	}
	if e.ForwardingDocs != nil && !slices.Equal(r.ForwardingDocs, *e.ForwardingDocs) {
		bad = append(bad, fmt.Sprintf("forwarding_docs %v != %v", r.ForwardingDocs, *e.ForwardingDocs))
	}
	if e.ForwardingForm != "" && r.ForwardingForm != e.ForwardingForm {
		bad = append(bad, fmt.Sprintf("forwarding_form %q != %q", r.ForwardingForm, e.ForwardingForm))
	}
	return bad
}

func TestCases(t *testing.T) {
	b := load(t)
	var doc struct {
		Cases []testCase `yaml:"cases"`
	}
	readYAML(t, testsPath, &doc)
	if len(doc.Cases) == 0 {
		t.Fatal("нет кейсов в tests.yaml")
	}
	for _, c := range doc.Cases {
		t.Run(c.ID, func(t *testing.T) {
			if bad := check(b.Evaluate(merge(c.Facts, nil)), c.Expect); len(bad) > 0 {
				t.Error(strings.Join(bad, "; "))
			}
		})
	}
}

func TestGolden(t *testing.T) {
	b := load(t)
	var doc struct {
		Situations []goldenSituation `yaml:"situations"`
	}
	readYAML(t, goldenPath, &doc)
	main := 0
	for _, g := range doc.Situations {
		if !g.BeforeStart {
			main++
		}
		if g.Verdict != "answer" && g.Verdict != "ambiguous" {
			t.Errorf("%s: verdict %q", g.ID, g.Verdict)
		}
		if g.Expect != nil {
			t.Run(g.ID, func(t *testing.T) {
				if bad := check(b.Evaluate(merge(g.Facts, g.Assumed)), *g.Expect); len(bad) > 0 {
					t.Error(strings.Join(bad, "; "))
				}
			})
		}
		for i, leg := range g.Legs {
			if leg.Expect == nil {
				continue
			}
			t.Run(fmt.Sprintf("%s/%d", g.ID, i+1), func(t *testing.T) {
				if bad := check(b.Evaluate(merge(leg.Facts, leg.Assumed)), *leg.Expect); len(bad) > 0 {
					t.Error(strings.Join(bad, "; "))
				}
			})
		}
	}
	if main < 30 {
		t.Errorf("в золотом наборе %d ситуаций после 01.09.2026, нужно ≥30 (бриф §10)", main)
	}
}

// Инварианты базы правил (бриф §9, CLAUDE.md).
func TestRulesInvariants(t *testing.T) {
	b := load(t)
	var doc struct {
		Cases []testCase `yaml:"cases"`
	}
	readYAML(t, testsPath, &doc)
	tested := map[string]bool{}
	for _, c := range doc.Cases {
		for _, ids := range []*[]string{c.Expect.Rules, c.Expect.FwdRules} {
			if ids != nil {
				for _, id := range *ids {
					tested[id] = true
				}
			}
		}
	}
	vocab, err := os.ReadFile(vocabPath)
	if err != nil {
		t.Fatal(err)
	}
	inVocab := func(s string) bool { return strings.Contains(string(vocab), "`"+s+"`") }

	seen := map[string]bool{}
	for _, r := range b.Rules {
		if seen[r.ID] {
			t.Errorf("повтор id %s", r.ID)
		}
		seen[r.ID] = true
		if !tested[r.ID] {
			t.Errorf("%s: нет тест-кейса", r.ID)
		}
		if len(r.Sources) == 0 {
			t.Errorf("%s: нет источников", r.ID)
		}
		for _, s := range r.Sources {
			if n := len(strings.Fields(s.Quote)); n > 25 {
				t.Errorf("%s: цитата %d слов (> 25)", r.ID, n)
			}
			if s.Quote == "" || s.URL == "" || s.Checked == "" {
				t.Errorf("%s: у источника %q нет цитаты, ссылки или даты проверки", r.ID, s.Act)
			}
		}
		if !slices.Contains([]string{"roles", "signers", "form", "fwd_docs", "fwd_form"}, r.Layer) {
			t.Errorf("%s: неизвестный слой %q", r.ID, r.Layer)
		}
		if !slices.Contains([]string{"clear", "disputed", "unknown"}, r.Certainty) {
			t.Errorf("%s: certainty %q", r.ID, r.Certainty)
		}
		for _, k := range r.FactKeys() {
			if !inVocab(k) {
				t.Errorf("%s: ключ %s не описан в VOCABULARY.md", r.ID, k)
			}
		}
		if d := r.Then.CarriageDoc; d != "" && !inVocab(d) {
			t.Errorf("%s: carriage_doc %s не описан в VOCABULARY.md", r.ID, d)
		}
		for _, v := range []string{r.Then.ForwardingDocs, r.Then.ForwardingForm} {
			if v != "" && !inVocab(v) {
				t.Errorf("%s: значение %s не описано в VOCABULARY.md", r.ID, v)
			}
		}
		if s := r.Then.Signs; s != "" && b.Titles(s) == nil {
			t.Errorf("%s: шаблон подписантов %s не найден", r.ID, s)
		}
		if p := r.Then.SignsPatch; p != "" && !b.HasPatch(p) {
			t.Errorf("%s: поправка подписантов %s не найдена", r.ID, p)
		}
		if r.Layer == "signers" && r.Then.SignsPatch == "" {
			t.Errorf("%s: правило слоя signers без signs_patch", r.ID)
		}
	}
}

// Перебор сочетаний основных фактов: в слое roles не больше одного правила,
// и нет сочетаний, для которых правило не найдено.
func TestNoOverlap(t *testing.T) {
	b := load(t)
	dims := []struct {
		key  string
		vals []string
	}{
		{"transport", []string{"own_seller", "own_buyer", "own_company", "hired_carrier", "forwarder", "no_contract", "rented_with_crew"}},
		{"carriage_customer", []string{"seller", "buyer", "company", "forwarder", "other"}},
		{"forwarder_own_vehicles", []string{"yes", "no"}},
		{"forwarder_takes_possession", []string{"yes", "no"}},
		{"international", []string{"yes", "no", "ru_via_foreign"}},
		{"tmc_accounting", []string{"yes", "no"}},
		{"owner_escort", []string{"yes", "no"}},
		{"seller_paid_delivery", []string{"yes", "no"}},
		{"ownership_before_delivery", []string{"yes", "no"}},
		{"intermediary_acts_in", []string{"own_name", "principal_name"}},
	}
	idx := make([]int, len(dims))
	total, nomatch := 0, 0
	for {
		f := engine.Facts{}
		for i, d := range dims {
			f[d.key] = d.vals[idx[i]]
		}
		r := b.Evaluate(f)
		total++
		if r.Error != "" {
			t.Fatalf("%v: %s", f, r.Error)
		}
		if r.RoleRule == nil {
			nomatch++
		}
		i := 0
		for ; i < len(dims); i++ {
			idx[i]++
			if idx[i] < len(dims[i].vals) {
				break
			}
			idx[i] = 0
		}
		if i == len(dims) {
			break
		}
	}
	if nomatch > 0 {
		t.Errorf("%d из %d сочетаний без правила слоя roles", nomatch, total)
	}
}

// «Не знаю» — то же, что отсутствие факта: движок не угадывает.
func TestUnknownIsMissing(t *testing.T) {
	b := load(t)
	r := b.Evaluate(engine.Facts{"transport": "unknown", "international": "no"})
	if r.RoleRule != nil {
		t.Fatalf("правило %s сработало при transport = unknown", r.RoleRule.ID)
	}
	if !slices.Contains(r.Ask, "transport") {
		t.Errorf("ask %v не содержит transport", r.Ask)
	}
}

// Пока исключения приказа № 262 не выяснены, движок сообщает, о чём ещё спросить.
func TestPending(t *testing.T) {
	b := load(t)
	f := engine.Facts{"transport": "hired_carrier", "carriage_customer": "seller", "international": "no", "tmc_accounting": "yes", "owner_escort": "no"}
	r := b.Evaluate(f)
	for _, k := range []string{"loader", "consignee_party", "receiver_representative", "exc_mod", "exc_individual_personal"} {
		if !slices.Contains(r.Pending, k) {
			t.Errorf("pending %v не содержит %s", r.Pending, k)
		}
	}
	if slices.Contains(r.Pending, "same_shipper_consignee") {
		t.Error("производный факт same_shipper_consignee не спрашивается")
	}
	if slices.Contains(r.Pending, "etrn_in_contract") {
		t.Error("etrn_in_contract спрашивается, только когда бумага допустима")
	}
	f["exc_individual_personal"] = "yes"
	r = b.Evaluate(f)
	if r.Form != "paper_allowed" || !slices.Contains(r.Pending, "etrn_in_contract") {
		t.Errorf("form %s, pending %v", r.Form, r.Pending)
	}
	// Если документ не ЭТрН — исключения и подписанты не нужны.
	r = b.Evaluate(engine.Facts{"transport": "own_buyer", "international": "no"})
	if len(r.Pending) != 0 {
		t.Errorf("pending %v при carriage_doc = none", r.Pending)
	}
}

// Экспедиторские документы спрашиваются только при экспедиторе и только когда сценарий выбран.
func TestPendingForwarding(t *testing.T) {
	b := load(t)
	f := engine.Facts{"transport": "forwarder", "forwarder_own_vehicles": "no", "forwarder_takes_possession": "yes",
		"international": "no", "tmc_accounting": "yes", "owner_escort": "no"}
	r := b.Evaluate(f)
	for _, k := range []string{"fwd_storage", "exc_fwd_consumer", "exc_fwd_foreign_client"} {
		if !slices.Contains(r.Pending, k) {
			t.Errorf("pending %v не содержит %s", r.Pending, k)
		}
	}
	if slices.Contains(r.Pending, "fwd_docs_in_contract") {
		t.Error("fwd_docs_in_contract спрашивается, только когда бумага допустима")
	}
	f["forwarder_takes_possession"] = "no"
	if r := b.Evaluate(f); slices.Contains(r.Pending, "fwd_storage") {
		t.Error("складская расписка не спрашивается, если экспедитор груз не принимает")
	}
	r = b.Evaluate(engine.Facts{"transport": "hired_carrier", "carriage_customer": "seller", "international": "no",
		"tmc_accounting": "yes", "owner_escort": "no"})
	for _, k := range r.Pending {
		if strings.HasPrefix(k, "exc_fwd") || strings.HasPrefix(k, "fwd_") {
			t.Errorf("без экспедитора спрашивается %s", k)
		}
	}
}

// Представитель грузополучателя: поправка заменяет только строку Т3, роли и прочие строки — как в сценарии.
func TestSignRowsProxy(t *testing.T) {
	b := load(t)
	f := engine.Facts{"transport": "hired_carrier", "carriage_customer": "seller", "international": "no",
		"tmc_accounting": "yes", "owner_escort": "no"}
	plain := b.SignRows(b.Evaluate(f))
	f["receiver_representative"] = "yes"
	r := b.Evaluate(f)
	proxy := b.SignRows(r)
	if len(plain) == 0 || len(plain) != len(proxy) {
		t.Fatalf("строк подписантов: без представителя %d, с ним %d", len(plain), len(proxy))
	}
	for i := range plain {
		switch {
		case plain[i].Title == "T3":
			if proxy[i].Signs != "consignee_proxy" || proxy[i].OnBehalf != "consignee" || proxy[i].Detail == "" {
				t.Errorf("Т3 с представителем: %+v", proxy[i])
			}
		case plain[i] != proxy[i]:
			t.Errorf("поправка изменила строку %s: %+v → %+v", plain[i].Title, plain[i], proxy[i])
		}
	}
	if r.Roles["consignee"] != "buyer" {
		t.Errorf("грузополучатель %q, ожидался buyer: представитель ролей не меняет", r.Roles["consignee"])
	}
	// Шаблон сценария в базе не испорчен поправкой.
	if again := b.SignRows(b.Evaluate(engine.Facts{"transport": "hired_carrier", "carriage_customer": "seller",
		"international": "no", "tmc_accounting": "yes", "owner_escort": "no"})); !slices.Equal(again, plain) {
		t.Error("поправка изменила шаблон standard")
	}
}

var idRe = regexp.MustCompile(`^R-\d{3}$`)

func TestRuleIDs(t *testing.T) {
	for _, r := range load(t).Rules {
		if !idRe.MatchString(r.ID) {
			t.Errorf("id %q не по шаблону R-NNN", r.ID)
		}
	}
}
