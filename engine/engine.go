// Package engine — детерминированный движок базы правил «Кто есть кто в ЭТрН».
//
// Семантика описана в rules/VOCABULARY.md: слой roles (ровно одно правило),
// уточнение ролей фактами loader и consignee_party, слой signers (поправки к шаблону
// подписантов, роли не меняет), слой form (исключения
// приказа Минтранса № 262), слои fwd_docs и fwd_form (экспедиторские документы
// и исключения приказа № 115), итоговая уверенность — минимальная из сработавших.
// Движок ничего не угадывает: факт со значением "unknown" равен отсутствующему.
package engine

import (
	"fmt"
	"os"
	"slices"
	"sort"

	"gopkg.in/yaml.v3"
)

// Facts — ответы пользователя: ключ факта → значение из VOCABULARY.md.
type Facts map[string]string

// Unknown — ответ «не знаю».
const Unknown = "unknown"

type Source struct {
	Act     string `yaml:"act"`
	Quote   string `yaml:"quote"`
	URL     string `yaml:"url"`
	Checked string `yaml:"checked"`
}

type Then struct {
	CarriageDoc   string            `yaml:"carriage_doc"`
	Form          string            `yaml:"form"`
	Roles         map[string]string `yaml:"roles"`
	Signs         string            `yaml:"signs"`
	ForwarderMark bool              `yaml:"forwarder_mark"`
	// Поправка к шаблону подписантов (titles.patches) — слой signers.
	SignsPatch string `yaml:"signs_patch"`
	// Экспедиторские документы: один документ на правило слоя fwd_docs; форма — слой fwd_form.
	ForwardingDocs string `yaml:"forwarding_docs"`
	ForwardingForm string `yaml:"forwarding_form"`
}

type Rule struct {
	ID        string         `yaml:"id"`
	Layer     string         `yaml:"layer"`
	Title     string         `yaml:"title"`
	When      map[string]any `yaml:"when"`
	Then      Then           `yaml:"then"`
	Certainty string         `yaml:"certainty"`
	Sources   []Source       `yaml:"sources"`
	Notes     string         `yaml:"notes"`
}

// TitleEntry — строка шаблона подписантов (titles.templates и titles.patches в rules.yaml).
type TitleEntry struct {
	Title          string `yaml:"title"`
	File           string `yaml:"file"`
	Signs          string `yaml:"signs"`
	OnBehalf       string `yaml:"on_behalf"` // от имени какой роли подписывает (представитель)
	Detail         string `yaml:"detail"`    // короткое пояснение под подписантом
	DriverOptional string `yaml:"driver_optional"`
	Note           string `yaml:"note"`
}

type Note struct {
	Text    string   `yaml:"text"`
	Sources []Source `yaml:"sources"`
}

type Meta struct {
	Version    string `yaml:"version"`
	Date       string `yaml:"date"`
	Vocabulary string `yaml:"vocabulary"`
	Status     string `yaml:"status"`
}

// Base — загруженная база правил.
type Base struct {
	Meta   Meta
	Rules  []*Rule
	Notes  map[string]Note
	Source Source // источник справочника titles
	Sign   string // titles.signature

	titles  map[string][]TitleEntry
	patches map[string][]TitleEntry
}

type rawBase struct {
	Meta   Meta `yaml:"meta"`
	Titles struct {
		Source    Source                  `yaml:"source"`
		Signature string                  `yaml:"signature"`
		Templates map[string]yaml.Node    `yaml:"templates"`
		Patches   map[string][]TitleEntry `yaml:"patches"`
	} `yaml:"titles"`
	Notes map[string]Note `yaml:"notes"`
	Rules []*Rule         `yaml:"rules"`
}

// Load читает rules.yaml. Изменение правил не требует пересборки — только перезапуска.
func Load(path string) (*Base, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var raw rawBase
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	b := &Base{Meta: raw.Meta, Rules: raw.Rules, Notes: raw.Notes, Source: raw.Titles.Source,
		Sign: raw.Titles.Signature, titles: map[string][]TitleEntry{}, patches: raw.Titles.Patches}
	// Шаблон — либо список строк, либо {extends, override}.
	pending := map[string]yaml.Node{}
	for name, node := range raw.Titles.Templates {
		if node.Kind == yaml.SequenceNode {
			var list []TitleEntry
			if err := node.Decode(&list); err != nil {
				return nil, fmt.Errorf("titles.%s: %w", name, err)
			}
			b.titles[name] = list
		} else {
			pending[name] = node
		}
	}
	for name, node := range pending {
		var ext struct {
			Extends  string       `yaml:"extends"`
			Override []TitleEntry `yaml:"override"`
		}
		if err := node.Decode(&ext); err != nil {
			return nil, fmt.Errorf("titles.%s: %w", name, err)
		}
		base, ok := b.titles[ext.Extends]
		if !ok {
			return nil, fmt.Errorf("titles.%s: неизвестный шаблон %s", name, ext.Extends)
		}
		b.titles[name] = patchRows(base, ext.Override)
	}
	for _, r := range b.Rules {
		if r.Then.Roles == nil {
			r.Then.Roles = map[string]string{}
		}
	}
	return b, nil
}

// Titles возвращает шаблон подписантов по имени (nil, если нет).
func (b *Base) Titles(name string) []TitleEntry { return b.titles[name] }

// HasPatch — есть ли поправка подписантов с таким именем.
func (b *Base) HasPatch(name string) bool { _, ok := b.patches[name]; return ok }

// SignRows — кто что подписывает по итогу движка: шаблон сценария с поправками слоя signers.
func (b *Base) SignRows(r Result) []TitleEntry {
	rows := b.titles[r.Signs]
	for _, p := range r.SignsPatches {
		rows = patchRows(rows, b.patches[p])
	}
	return rows
}

// patchRows — копия list, в которой строки с тем же титулом заменены непустыми полями из over.
func patchRows(list, over []TitleEntry) []TitleEntry {
	out := slices.Clone(list)
	set := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	for _, o := range over {
		for i := range out {
			if out[i].Title != o.Title {
				continue
			}
			set(&out[i].File, o.File)
			set(&out[i].Signs, o.Signs)
			set(&out[i].OnBehalf, o.OnBehalf)
			set(&out[i].Detail, o.Detail)
			set(&out[i].DriverOptional, o.DriverOptional)
			set(&out[i].Note, o.Note)
		}
	}
	return out
}

// FactKeys — все ключи фактов в условии правила.
func (r *Rule) FactKeys() []string {
	var keys []string
	for k, v := range r.When {
		if k != "any" {
			keys = append(keys, k)
			continue
		}
		for _, sub := range asList(v) {
			if m, ok := sub.(map[string]any); ok {
				for sk := range m {
					keys = append(keys, sk)
				}
			}
		}
	}
	sort.Strings(keys)
	return slices.Compact(keys)
}

// Result — вывод движка.
type Result struct {
	RoleRule    *Rule    // сработавшее правило слоя roles (nil — правило не найдено)
	SignerRules []*Rule  // сработавшие правила слоя signers
	FormRules   []*Rule  // сработавшие исключения приказа № 262
	RuleIDs     []string // roles, signers и form — всё о транспортной накладной

	// Экспедиторские документы (только при экспедиторе).
	FwdDocRules    []*Rule  // сработавшие правила слоя fwd_docs
	FwdFormRules   []*Rule  // сработавшие исключения приказа № 115
	ForwardingDocs []string // epe, eer, warehouse_receipt — в порядке правил
	ForwardingForm string

	CarriageDoc   string
	Form          string
	Roles         map[string]string
	Signs         string
	SignsPatches  []string // поправки к шаблону подписантов — в порядке правил
	ForwarderMark bool
	Certainty     string

	// Ask — каких фактов не хватает, чтобы выбрать сценарий (слой roles).
	Ask []string
	// Pending — уточнения, которые ещё не спрошены, когда сценарий выбран:
	// loader, consignee_party, receiver_representative, исключения приказа № 262, etrn_in_contract.
	Pending []string
	// Error — ошибка базы правил (сработало больше одного правила слоя roles).
	Error string
}

// FwdRuleIDs — сработавшие правила слоёв fwd_docs и fwd_form.
func (r Result) FwdRuleIDs() []string {
	ids := []string{}
	for _, x := range append(slices.Clone(r.FwdDocRules), r.FwdFormRules...) {
		ids = append(ids, x.ID)
	}
	return ids
}

// AllRuleIDs — все сработавшие правила (для журнала).
func (r Result) AllRuleIDs() []string { return append(slices.Clone(r.RuleIDs), r.FwdRuleIDs()...) }

var certaintyOrder = []string{"unknown", "disputed", "clear"}

const derivedSame = "same_shipper_consignee"

// Evaluate применяет правила к фактам.
func (b *Base) Evaluate(in Facts) Result {
	answered := func(k string) bool { _, ok := in[k]; return ok }
	facts := Facts{}
	for k, v := range in {
		if v != "" && v != Unknown {
			facts[k] = v
		}
	}

	res := Result{Roles: map[string]string{}}
	var fired []*Rule
	ask := map[string]bool{}

	// Транспортная компания сводится к существующим сценариям по виду договора (VOCABULARY.md,
	// «Производные факты»). Вариант tk означает, что ТК принимает груз, поэтому при экспедиции
	// forwarder_takes_possession = yes.
	if facts["transport"] == "tk" {
		switch facts["tk_contract"] {
		case "carriage":
			facts["transport"] = "hired_carrier"
		case "forwarding":
			facts["transport"] = "forwarder"
			facts["forwarder_takes_possession"] = "yes"
		default:
			if !answered("tk_contract") {
				ask["tk_contract"] = true
			}
		}
	}
	for _, r := range b.Rules {
		if r.Layer != "roles" {
			continue
		}
		ok, missing := match(r.When, facts)
		switch {
		case ok:
			fired = append(fired, r)
		case len(missing) > 0:
			for _, k := range missing {
				ask[k] = true
			}
		}
	}
	if len(fired) > 1 {
		var ids []string
		for _, r := range fired {
			ids = append(ids, r.ID)
		}
		res.Error = "сработало несколько правил слоя roles: " + fmt.Sprint(ids)
		return res
	}
	if len(fired) == 0 {
		res.Certainty = "unknown"
		for k := range ask {
			res.Ask = append(res.Ask, k)
		}
		sort.Strings(res.Ask)
		// Экспедиторские документы следуют из договора экспедиции, а не из сценария ЭТрН:
		// их показываем и тогда, когда сценарий не выбран (например, неизвестно, чьими машинами везёт ТК).
		if facts["transport"] == "forwarder" {
			b.forwarding(facts, answered, &res)
		}
		return res
	}

	r := fired[0]
	res.RoleRule = r
	res.RuleIDs = []string{r.ID}
	res.CarriageDoc = r.Then.CarriageDoc
	res.Signs = r.Then.Signs
	res.ForwarderMark = r.Then.ForwarderMark
	certs := []string{r.Certainty}
	for k, v := range r.Then.Roles {
		res.Roles[k] = v
	}

	// 1а. Уточнение ролей фактами. В правилах loader: shipper — «то же лицо, что грузоотправитель».
	if s := res.Roles["shipper"]; s != "" && s != "none" {
		if res.Roles["loader"] == "shipper" {
			res.Roles["loader"] = s
		}
		if ld, ok := facts["loader"]; ok {
			if ld == "shipper" {
				res.Roles["loader"] = s
			} else {
				res.Roles["loader"] = ld
			}
		}
		switch facts["consignee_party"] {
		case "customer_itself":
			if fc, ok := res.Roles["forwarder_client"]; ok {
				res.Roles["consignee"] = fc
			} else {
				res.Roles["consignee"] = s
			}
		case "customer_buyer":
			if s == "buyer" {
				res.Roles["consignee"] = "third_party"
			} else {
				res.Roles["consignee"] = "buyer"
			}
		case "third_party":
			res.Roles["consignee"] = "third_party"
		case "shipper_itself":
			res.Roles["consignee"] = s
		case "terminal":
			certs = append(certs, "unknown")
		}
		for _, k := range []string{"loader", "consignee_party"} {
			if !answered(k) {
				res.Pending = append(res.Pending, k)
			}
		}
		if res.Roles["consignee"] == s {
			facts[derivedSame] = "yes"
		}
	}

	// 1б. Слой signers — кто подписывает титулы; только если у сценария есть шаблон подписантов.
	if res.Signs != "" {
		for _, sr := range b.Rules {
			if sr.Layer != "signers" {
				continue
			}
			ok, missing := match(sr.When, facts)
			if ok {
				res.SignerRules = append(res.SignerRules, sr)
				res.RuleIDs = append(res.RuleIDs, sr.ID)
				res.SignsPatches = append(res.SignsPatches, sr.Then.SignsPatch)
				certs = append(certs, sr.Certainty)
			}
			for _, k := range missing {
				if !answered(k) && !slices.Contains(res.Pending, k) {
					res.Pending = append(res.Pending, k)
				}
			}
		}
	}

	// 2. Слой form — только для ЭТрН.
	if res.CarriageDoc == "etrn" {
		for _, fr := range b.Rules {
			if fr.Layer != "form" {
				continue
			}
			ok, missing := match(fr.When, facts)
			if ok {
				res.FormRules = append(res.FormRules, fr)
				res.RuleIDs = append(res.RuleIDs, fr.ID)
			}
			for _, k := range missing {
				if k != derivedSame && !answered(k) && !slices.Contains(res.Pending, k) {
					res.Pending = append(res.Pending, k)
				}
			}
		}
		if c := best(res.FormRules); c != "" {
			certs = append(certs, c)
		}
		switch {
		case len(res.FormRules) == 0:
			res.Form = "electronic"
		case facts["etrn_in_contract"] == "yes":
			res.Form = "electronic_by_contract"
		default:
			res.Form = "paper_allowed"
		}
		if len(res.FormRules) > 0 && !answered("etrn_in_contract") {
			res.Pending = append(res.Pending, "etrn_in_contract")
		}
	}

	// 3–4. Экспедиторские документы и исключения приказа № 115.
	certs = append(certs, b.forwarding(facts, answered, &res)...)

	res.Certainty = certs[0]
	for _, c := range certs {
		if slices.Index(certaintyOrder, c) < slices.Index(certaintyOrder, res.Certainty) {
			res.Certainty = c
		}
	}
	return res
}

// best — наибольшая уверенность среди сработавших исключений слоя ("" — ни одного): бумага
// допустима, если подходит хотя бы одно исключение, поэтому спорное исключение не ослабляет бесспорное.
func best(rules []*Rule) string {
	c := ""
	for _, r := range rules {
		if c == "" || slices.Index(certaintyOrder, r.Certainty) > slices.Index(certaintyOrder, c) {
			c = r.Certainty
		}
	}
	return c
}

// forwarding — слои fwd_docs и fwd_form (экспедиторские документы и исключения приказа № 115).
// Заполняет res, возвращает уверенность сработавших правил.
func (b *Base) forwarding(facts Facts, answered func(string) bool, res *Result) (certs []string) {
	pend := func(missing []string) {
		for _, k := range missing {
			if !answered(k) && !slices.Contains(res.Pending, k) {
				res.Pending = append(res.Pending, k)
			}
		}
	}
	for _, fr := range b.Rules {
		if fr.Layer != "fwd_docs" {
			continue
		}
		ok, missing := match(fr.When, facts)
		if ok {
			res.FwdDocRules = append(res.FwdDocRules, fr)
			res.ForwardingDocs = append(res.ForwardingDocs, fr.Then.ForwardingDocs)
			certs = append(certs, fr.Certainty)
		}
		pend(missing)
	}
	if len(res.FwdDocRules) > 0 {
		for _, fr := range b.Rules {
			if fr.Layer != "fwd_form" {
				continue
			}
			ok, missing := match(fr.When, facts)
			if ok {
				res.FwdFormRules = append(res.FwdFormRules, fr)
			}
			pend(missing)
		}
		if c := best(res.FwdFormRules); c != "" {
			certs = append(certs, c)
		}
		switch {
		case len(res.FwdFormRules) == 0:
			res.ForwardingForm = "electronic"
		case facts["fwd_docs_in_contract"] == "yes":
			res.ForwardingForm = "electronic_by_contract"
		default:
			res.ForwardingForm = "paper_allowed"
		}
		if len(res.FwdFormRules) > 0 && !answered("fwd_docs_in_contract") {
			res.Pending = append(res.Pending, "fwd_docs_in_contract")
		}
	}
	return certs
}

// match проверяет условие: ok — выполнено; иначе missing — каких фактов не хватает
// (пусто, если условие точно не выполнено).
func match(cond map[string]any, facts Facts) (ok bool, missing []string) {
	for k, v := range cond {
		if k == "any" {
			anyOK := false
			var anyMissing []string
			for _, sub := range asList(v) {
				m, _ := sub.(map[string]any)
				sok, smiss := match(m, facts)
				if sok {
					anyOK = true
					break
				}
				anyMissing = append(anyMissing, smiss...)
			}
			if anyOK {
				continue
			}
			if len(anyMissing) == 0 {
				return false, nil
			}
			missing = append(missing, anyMissing...)
			continue
		}
		f, has := facts[k]
		if !has {
			missing = append(missing, k)
			continue
		}
		if !slices.Contains(allowed(v), f) {
			return false, nil
		}
	}
	return len(missing) == 0, missing
}

func asList(v any) []any {
	if l, ok := v.([]any); ok {
		return l
	}
	return []any{v}
}

func allowed(v any) []string {
	var out []string
	for _, x := range asList(v) {
		switch t := x.(type) {
		case bool:
			if t {
				out = append(out, "yes")
			} else {
				out = append(out, "no")
			}
		default:
			out = append(out, fmt.Sprint(t))
		}
	}
	return out
}
