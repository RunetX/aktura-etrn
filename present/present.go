// Package present — общее для интерфейсов опросника (сайт, боты): переходы опроса
// (ответ, «Не знаю», «Назад») и представление ответов и результата подписями из questions.yaml.
// Выводы даёт только движок; здесь — лишь подписи к ним.
package present

import (
	"slices"
	"strings"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/quiz"
)

type Presenter struct {
	Base *engine.Base
	Set  *quiz.Set
	// допустимые значения каждого факта (из вопросов) — всё прочее отбрасывается
	valid map[string][]string
}

func New(b *engine.Base, s *quiz.Set) *Presenter {
	p := &Presenter{Base: b, Set: s, valid: map[string][]string{}}
	for _, q := range s.Questions {
		for _, o := range q.Options {
			if q.Group != "" {
				p.valid[o.Fact] = []string{"yes", "no", engine.Unknown}
			} else {
				p.valid[q.Fact] = append(p.valid[q.Fact], o.Value)
			}
		}
		if q.Group == "" {
			p.valid[q.Fact] = append(p.valid[q.Fact], engine.Unknown)
		}
	}
	return p
}

// Valid — значение v допустимо для факта.
func (p *Presenter) Valid(fact, v string) bool { return slices.Contains(p.valid[fact], v) }

// ---------- переходы опроса ----------

// State — ответы и история заданных вопросов (ID по порядку).
type State struct {
	Answers engine.Facts
	History []string
}

// Back снимает ответы на последний заданный вопрос.
func (p *Presenter) Back(st State) State {
	if n := len(st.History); n > 0 {
		for _, f := range p.Set.Question(st.History[n-1]).Facts() {
			delete(st.Answers, f)
		}
		st.History = st.History[:n-1]
	}
	return st
}

// Unknown — «Не знаю» на все факты вопроса.
func (p *Presenter) Unknown(st State, q *quiz.Question) State {
	for _, f := range q.Facts() {
		st.Answers[f] = engine.Unknown
	}
	st.History = append(st.History, q.ID())
	return st
}

// Group — ответ на вопрос-группу: отмеченные факты «да», остальные «нет».
func (p *Presenter) Group(st State, q *quiz.Question, checked []string) State {
	for _, f := range q.Facts() {
		if slices.Contains(checked, f) {
			st.Answers[f] = "yes"
		} else {
			st.Answers[f] = "no"
		}
	}
	st.History = append(st.History, q.ID())
	return st
}

// Answer — ответ на обычный вопрос; false, если значение недопустимо (состояние не меняется).
func (p *Presenter) Answer(st State, q *quiz.Question, v string) (State, bool) {
	if q.Group != "" || !p.Valid(q.Fact, v) {
		return st, false
	}
	st.Answers[q.Fact] = v
	st.History = append(st.History, q.ID())
	return st, true
}

// ---------- представление ----------

type KV struct{ Key, Value string }

type TitleRow struct{ Title, File, Who, Detail, Driver, Note string }

type RuleView struct {
	ID, Title, Certainty, CertClass, Notes string
	Sources                                []engine.Source
}

type Result struct {
	Found         bool
	CarriageDoc   string
	Form          string
	FormNote      string
	Certainty     string
	CertClass     string
	Roles         []KV
	ForwarderMark bool
	Titles        []TitleRow
	Rules         []RuleView
	Missing       []string
	Terminal      bool
	Error         string
	// Экспедиторские документы (только при экспедиторе).
	FwdDocs []string
	FwdForm string
}

// Summary — «вопрос → выбранный ответ» в порядке вопросов.
func (p *Presenter) Summary(answers engine.Facts) []KV {
	var out []KV
	for _, q := range p.Set.Questions {
		if q.Group != "" {
			var marked []string
			asked, unknown := false, false
			for _, o := range q.Options {
				switch answers[o.Fact] {
				case "yes":
					marked = append(marked, o.Label)
					asked = true
				case "no":
					asked = true
				case engine.Unknown:
					asked, unknown = true, true
				}
			}
			switch {
			case !asked:
			case unknown:
				out = append(out, KV{q.Text, "Не знаю"})
			case len(marked) == 0:
				out = append(out, KV{q.Text, "ничего не отмечено"})
			default:
				out = append(out, KV{q.Text, strings.Join(marked, "; ")})
			}
			continue
		}
		v, ok := answers[q.Fact]
		if !ok {
			continue
		}
		label := "Не знаю"
		for _, o := range q.Options {
			if o.Value == v {
				label = o.Label
			}
		}
		out = append(out, KV{q.Text, label})
	}
	return out
}

// CertClass — класс оформления уверенности: ok, warn, bad.
func CertClass(c string) string {
	switch c {
	case "clear":
		return "ok"
	case "disputed":
		return "warn"
	}
	return "bad"
}

// Result — итог движка подписями из questions.yaml.
func (p *Presenter) Result(answers engine.Facts) *Result {
	r := p.Base.Evaluate(answers)
	L := p.Set.Labels
	v := &Result{Error: r.Error, Certainty: L.Certainty[r.Certainty], CertClass: CertClass(r.Certainty)}
	if r.RoleRule == nil {
		for _, q := range p.Set.Questions {
			for _, f := range q.Facts() {
				if slices.Contains(r.Ask, f) {
					v.Missing = append(v.Missing, q.Text)
					break
				}
			}
		}
		// Экспедиторские документы не зависят от сценария ЭТрН — показываем и без него.
		p.forwarding(r, v, nil)
		return v
	}
	v.Found = true
	v.CarriageDoc = L.CarriageDoc[r.CarriageDoc]
	v.Form = L.Form[r.Form]
	v.FormNote = L.FormNotes[r.Form]
	v.ForwarderMark = r.ForwarderMark
	v.Terminal = answers["consignee_party"] == "terminal"
	party := func(role string) string {
		val, ok := r.Roles[role]
		if !ok {
			return ""
		}
		if l := L.RoleValues[val]; l != "" {
			return l
		}
		return val
	}
	for _, k := range []string{"shipper", "consignee", "carrier", "loader", "forwarder_client"} {
		if pt := party(k); pt != "" && pt != "—" {
			v.Roles = append(v.Roles, KV{L.RoleKeys[k], pt})
		}
	}
	for _, t := range p.Base.SignRows(r) {
		who := L.Signers[t.Signs]
		role := t.Signs
		if t.OnBehalf != "" { // представитель: показываем, за кого он подписывает
			role = t.OnBehalf
		}
		if pt := party(role); pt != "" {
			who += " — " + pt
		}
		title := t.Title
		if title == "" {
			title = "—"
		}
		v.Titles = append(v.Titles, TitleRow{Title: title, File: t.File, Who: who, Detail: t.Detail,
			Driver: t.DriverOptional, Note: t.Note})
	}
	etrn := append(append([]*engine.Rule{r.RoleRule}, r.SignerRules...), r.FormRules...)
	p.forwarding(r, v, etrn)
	return v
}

// forwarding — экспедиторские документы и основания: правила ЭТрН (etrn), затем слоёв fwd_docs и fwd_form.
func (p *Presenter) forwarding(r engine.Result, v *Result, etrn []*engine.Rule) {
	L := p.Set.Labels
	for _, d := range r.ForwardingDocs {
		v.FwdDocs = append(v.FwdDocs, L.FwdDocs[d])
	}
	v.FwdForm = L.FwdForm[r.ForwardingForm]
	rules := append(append(etrn, r.FwdDocRules...), r.FwdFormRules...)
	for _, rule := range rules {
		v.Rules = append(v.Rules, RuleView{ID: rule.ID, Title: rule.Title, Certainty: L.Certainty[rule.Certainty],
			CertClass: CertClass(rule.Certainty), Notes: rule.Notes, Sources: rule.Sources})
	}
}
