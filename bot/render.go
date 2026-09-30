package bot

import (
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/RunetX/aktura-etrn/quiz"
)

// maxMessage — предел одного сообщения в единицах UTF-16. Telegram — 4096, MAX — 4000
// (и считает сырой текст); берём с запасом.
const maxMessage = 3800

// shortLabel — подписи не длиннее этого (в символах) помещаются на кнопку целиком.
const shortLabel = 30

const perRow = 4

func (l *Logic) question(s *session, q *quiz.Question) (string, *Keyboard) {
	var b strings.Builder
	b.WriteString("Вопрос " + strconv.Itoa(len(s.st.History)+1) + "\n\n")
	b.WriteString(q.Text + "\n")
	if q.Hint != "" {
		b.WriteString("\n" + q.Hint + "\n")
	}
	kb := &Keyboard{}
	if q.Group != "" {
		b.WriteString("\nОтметьте всё, что подходит, и нажмите «Готово». Ничего не подходит — сразу «Готово».\n")
		var row []Button
		for i, o := range q.Options {
			n := strconv.Itoa(i + 1)
			mark := "☐"
			if s.checked[o.Fact] {
				mark = "☑"
			}
			option(&b, mark+" "+n+". ", o)
			row = append(row, Button{Text: mark + " " + n, Payload: Pack(vToggle, o.Fact)})
			if len(row) == perRow {
				kb.Row(row...)
				row = nil
			}
		}
		kb.Row(row...)
		kb.Row(Button{Text: "Готово", Payload: Pack(vDone, q.ID())})
	} else if short(q) {
		for _, o := range q.Options {
			kb.Row(Button{Text: o.Label, Payload: Pack(vAnswer, q.ID()+":"+o.Value)})
		}
	} else {
		var row []Button
		for i, o := range q.Options {
			n := strconv.Itoa(i + 1)
			option(&b, n+". ", o)
			row = append(row, Button{Text: n, Payload: Pack(vAnswer, q.ID()+":"+o.Value)})
			if len(row) == perRow {
				kb.Row(row...)
				row = nil
			}
		}
		kb.Row(row...)
	}
	ctl := []Button{{Text: "Не знаю", Payload: Pack(vUnknown, q.ID())}}
	if n := len(s.st.History); n > 0 {
		ctl = append(ctl, Button{Text: "← Назад", Payload: Pack(vBack, strconv.Itoa(n))})
	}
	kb.Row(ctl...)
	return b.String(), kb
}

// option — строка варианта, под ней подсказка к нему.
func option(b *strings.Builder, prefix string, o quiz.Option) {
	b.WriteString("\n" + prefix + o.Label)
	if o.Hint != "" {
		b.WriteString("\n   " + o.Hint)
	}
}

// short — все подписи вопроса помещаются на кнопки (подсказки к вариантам на кнопку не помещаются).
func short(q *quiz.Question) bool {
	for _, o := range q.Options {
		if len([]rune(o.Label)) > shortLabel || o.Hint != "" {
			return false
		}
	}
	return true
}

func (l *Logic) summary(s *session) string {
	var b strings.Builder
	for _, kv := range l.pr.Summary(s.st.Answers) {
		b.WriteString("• " + kv.Key + " — " + kv.Value + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// resultText — итог тем же составом, что на сайте (web/templates/page.html), без разметки.
func (l *Logic) resultText(s *session) string {
	r := l.pr.Result(s.st.Answers)
	var b strings.Builder
	line := func(parts ...string) { b.WriteString(strings.Join(parts, "") + "\n") }
	if r.Error != "" {
		line("Ошибка базы правил: ", r.Error)
	}
	if !r.Found {
		line("Правило не найдено")
		line("Уверенность: ", r.Certainty)
		if len(r.Missing) > 0 {
			line("\nДля ответа не хватает ответов на вопросы:")
			for _, m := range r.Missing {
				line("• ", m)
			}
		}
		line("\nНажмите «Назад», чтобы изменить ответ, или уточните факты у контрагента.")
		if len(r.FwdDocs) == 0 {
			return strings.TrimRight(b.String(), "\n")
		}
		line("Это о транспортной накладной. Экспедиторские документы от этого не зависят — они ниже.")
	} else {
		line("ИТОГ")
		line(r.CarriageDoc)
		if r.Form != "" {
			line(r.Form)
		}
		if r.FormNote != "" {
			line(r.FormNote)
		}
		line("Уверенность: ", r.Certainty)
		if r.Terminal {
			line("\nКто грузополучатель при доставке на терминал другой транспортной компании или службы доставки, нормы прямо не определяют.")
		}
	}
	if len(r.Roles) > 0 {
		line("\nКТО ЕСТЬ КТО")
		for _, kv := range r.Roles {
			line("• ", kv.Key, ": ", kv.Value)
		}
		if r.ForwarderMark {
			line("В разделе «Грузоотправитель» проставляется отметка «является экспедитором», клиент указывается в сведениях о заказчике.")
		}
	}
	if len(r.Titles) > 0 {
		line("\nКТО ЧТО ПОДПИСЫВАЕТ")
		for _, t := range r.Titles {
			line("• ", strings.ReplaceAll(t.Title, "T", "Т"), " · ", t.File, ": ", t.Who)
			if t.Detail != "" {
				line("  ", t.Detail)
			}
			if t.Driver != "" {
				line("  ", t.Driver)
			}
		}
		line("ПП РФ № 931, прил. 1, п. 3. Подпись — УКЭП или УНЭП; представитель — по доверенности, в т. ч. машиночитаемой.")
	}
	if len(r.FwdDocs) > 0 {
		line("\nЭКСПЕДИТОРСКИЕ ДОКУМЕНТЫ")
		for _, d := range r.FwdDocs {
			line("• ", d)
		}
		if r.FwdForm != "" {
			line(r.FwdForm)
		}
		line("87-ФЗ ст. 2 п. 3; ПП РФ № 469. Документы договора экспедиции — отдельно от транспортной накладной.")
	}
	if len(r.Rules) > 0 {
		line("\nОСНОВАНИЯ")
		for _, rule := range r.Rules {
			line("\n", rule.ID, ". ", rule.Title, " — ", rule.Certainty)
			for _, src := range rule.Sources {
				act := src.Act
				if src.URL != "" {
					act += " (" + src.URL + ")"
				}
				line("— ", act)
				line("  «", src.Quote, "»")
				if src.Checked != "" {
					line("  Проверено ", src.Checked)
				}
			}
		}
	}
	line("\nСправочный инструмент, не юридическая консультация. База правил ", l.pr.Base.Meta.Version,
		" от ", l.pr.Base.Meta.Date, ", вопросы ", l.pr.Set.Version, ".")
	return strings.TrimRight(b.String(), "\n")
}

func (l *Logic) resultKeyboard(s *session) *Keyboard {
	step := strconv.Itoa(len(s.st.History))
	kb := (&Keyboard{}).Row(
		Button{Text: "← Назад", Payload: Pack(vBack, step)},
		Button{Text: "Заново", Payload: Pack(vRestart, "")},
	)
	if l.cfg.Store != nil && !s.sent {
		kb.Row(Button{Text: "В моей ситуации это не так", Payload: Pack(vWrong, step)})
	}
	if l.cfg.Site != "" {
		kb.Row(Button{Text: "Сайт " + strings.TrimPrefix(strings.TrimPrefix(l.cfg.Site, "https://"), "http://"), URL: l.cfg.Site})
	}
	return kb
}

// split режет текст на части не длиннее max единиц UTF-16: по абзацам, затем по строкам,
// в крайнем случае — по символам.
func split(text string, max int) []string {
	size := func(s string) int { return len(utf16.Encode([]rune(s))) }
	if size(text) <= max {
		return []string{text}
	}
	var out []string
	var cur strings.Builder
	flush := func() {
		if t := strings.Trim(cur.String(), "\n"); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, ln := range strings.SplitAfter(text, "\n") {
		if size(cur.String())+size(ln) > max {
			flush()
		}
		for size(ln) > max { // строка длиннее сообщения
			r := []rune(ln)
			n := 0
			for i := range r {
				if size(string(r[:i+1])) > max {
					break
				}
				n = i + 1
			}
			out = append(out, string(r[:n]))
			ln = string(r[n:])
		}
		cur.WriteString(ln)
	}
	flush()
	return out
}
