// Package extract — ИИ-слой, этап 3 (бриф §5): свободный текст → ответы на вопросы опросника.
//
// ИИ ничего не решает: он только предлагает значения фактов словаря, а принимается предложение
// лишь если факт и значение есть в вопросах опросника и подкреплены дословной цитатой из текста
// пользователя. Всё прочее отбрасывается — недостающее опросник спросит сам. Роли и выводы
// по-прежнему даёт только движок.
package extract

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"

	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/quiz"
)

// Skip — значение поля ai вопроса: ИИ его не заполняет, вопрос всегда задаёт опросник.
const Skip = "skip"

// MaxText — предел длины описания (в символах): длиннее обрезается.
const MaxText = 2000

// Model — языковая модель за интерфейсом (бриф §5: выбор через конфиг).
// Complete возвращает текст ответа модели на системную и пользовательскую реплики.
type Model interface {
	Complete(ctx context.Context, system, user string) (string, error)
}

// Item — предложенный моделью факт.
type Item struct {
	Fact  string `json:"fact"`
	Value string `json:"value"`
	Quote string `json:"quote"`
}

// Rejected — отброшенное предложение и причина.
type Rejected struct {
	Item
	Reason string
}

type Result struct {
	Facts    engine.Facts // принятые факты — только они идут в опросник
	Accepted []Item
	Rejected []Rejected
	Text     string // текст, отправленный модели (после удаления персональных данных)
}

type Extractor struct {
	model  Model
	set    *quiz.Set
	valid  map[string][]string
	system string
}

func New(m Model, s *quiz.Set) *Extractor {
	e := &Extractor{model: m, set: s, valid: map[string][]string{}}
	for _, q := range s.Questions {
		if q.AI == Skip {
			continue
		}
		for _, o := range q.Options {
			if q.Group != "" {
				e.valid[o.Fact] = []string{"yes"}
			} else if !slices.Contains(q.AIExclude, o.Value) {
				e.valid[q.Fact] = append(e.valid[q.Fact], o.Value)
			}
		}
	}
	e.system = e.buildPrompt()
	return e
}

// Prompt — системная реплика: инструкция и перечень вопросов с вариантами, собранный из questions.yaml.
func (e *Extractor) Prompt() string { return e.system }

func (e *Extractor) buildPrompt() string {
	var b strings.Builder
	b.WriteString(`Ты извлекаешь факты из описания грузоперевозки. Ты не консультируешь и не делаешь выводов о документах, ролях и нормах права.

Ниже — вопросы анкеты. Для каждого вопроса, ответ на который ПРЯМО сказан в описании, выбери один вариант из перечисленных.
Правила:
- Отвечай только на то, что прямо сказано. Если ответ можно лишь предположить — пропусти вопрос.
- Не используй знания о законах и типичных случаях: только текст описания.
- Не делай выводов из того, что записано или должно быть записано в ЭТрН и других документах («в ЭТрН грузоотправителем указан…»): человек часто спрашивает именно потому, что там записано неверно.
- Мнения, требования, обещания и сомнения («считает», «требует», «обещает», «спрашивает») — не факты.
- Для каждого ответа приведи quote — точный фрагмент описания (без изменений, 2–15 слов), на котором он основан.
- «Продавец», «покупатель» — стороны сделки с товаром; «организация» — тот, кто описывает ситуацию, если он везёт груз себе.
- Верни только JSON без пояснений: {"facts":[{"fact":"...","value":"...","quote":"..."}]}. Если ничего не сказано прямо — {"facts":[]}.

Вопросы (fact — ключ, value — допустимые значения):
`)
	for _, q := range e.set.Questions {
		if q.AI == Skip {
			continue
		}
		b.WriteString("\n")
		if q.Group != "" {
			fmt.Fprintf(&b, "# %s (для каждого пункта, который прямо упомянут, value = \"yes\")\n", q.Text)
			for _, o := range q.Options {
				fmt.Fprintf(&b, "fact=%s: %s\n", o.Fact, o.Label)
			}
			continue
		}
		fmt.Fprintf(&b, "fact=%s: %s\n", q.Fact, q.Text)
		// Подсказка для людей нужна и модели: без неё (−25% токенов) на золотом наборе
		// появлялись уверенные ошибки (прогоны 24.09.2026).
		if q.Hint != "" {
			fmt.Fprintf(&b, "  подсказка: %s\n", q.Hint)
		}
		if q.AI != "" {
			fmt.Fprintf(&b, "  как узнать: %s\n", q.AI)
		}
		for _, o := range q.Options {
			if !slices.Contains(q.AIExclude, o.Value) {
				fmt.Fprintf(&b, "  value=%s: %s\n", o.Value, o.Label)
			}
		}
	}
	return b.String()
}

// Extract отправляет описание модели и проверяет ответ.
func (e *Extractor) Extract(ctx context.Context, text string) (Result, error) {
	text = Redact(truncate(strings.TrimSpace(text), MaxText))
	raw, err := e.model.Complete(ctx, e.system, text)
	if err != nil {
		return Result{Text: text}, err
	}
	r := e.Parse(raw, text)
	r.Text = text
	return r, nil
}

// Parse проверяет ответ модели относительно текста, который ей отправили.
// Принимается факт из вопросов, со значением из вариантов и с цитатой, найденной в тексте.
// Если модель дала одному факту разные значения — отбрасываются все.
func (e *Extractor) Parse(raw, text string) Result {
	r := Result{Facts: engine.Facts{}}
	var doc struct {
		Facts []Item `json:"facts"`
	}
	i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}")
	if i < 0 || j < i || json.Unmarshal([]byte(raw[i:j+1]), &doc) != nil {
		r.Rejected = append(r.Rejected, Rejected{Item{Quote: truncate(raw, 200)}, "ответ модели — не JSON"})
		return r
	}
	norm := normalize(text)
	conflict := map[string]bool{}
	for _, it := range doc.Facts {
		it.Fact, it.Value, it.Quote = strings.TrimSpace(it.Fact), strings.TrimSpace(it.Value), strings.TrimSpace(it.Quote)
		switch vals, ok := e.valid[it.Fact]; {
		case !ok:
			r.Rejected = append(r.Rejected, Rejected{it, "нет такого вопроса"})
			continue
		case !slices.Contains(vals, it.Value):
			r.Rejected = append(r.Rejected, Rejected{it, "нет такого варианта ответа"})
			continue
		}
		if q := normalize(it.Quote); q == "" || !strings.Contains(norm, q) {
			r.Rejected = append(r.Rejected, Rejected{it, "цитаты нет в тексте"})
			continue
		}
		if v, dup := r.Facts[it.Fact]; dup {
			if v != it.Value {
				conflict[it.Fact] = true
			}
			continue
		}
		r.Facts[it.Fact] = it.Value
		r.Accepted = append(r.Accepted, it)
	}
	for f := range conflict {
		delete(r.Facts, f)
		r.Accepted = slices.DeleteFunc(r.Accepted, func(it Item) bool {
			if it.Fact == f {
				r.Rejected = append(r.Rejected, Rejected{it, "разные ответы на один вопрос"})
				return true
			}
			return false
		})
	}
	return r
}

// normalize — для сверки цитаты: нижний регистр, ё → е, любые не-буквы и не-цифры — один пробел.
func normalize(s string) string {
	var b strings.Builder
	space := true
	for _, r := range strings.ToLower(s) {
		if r == 'ё' {
			r = 'е'
		}
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			space = false
		} else if !space {
			b.WriteByte(' ')
			space = true
		}
	}
	return strings.TrimSpace(b.String())
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

var (
	reEmail = regexp.MustCompile(`[\p{L}\p{N}._%+-]+@[\p{L}\p{N}.-]+\.\p{L}{2,}`)
	rePhone = regexp.MustCompile(`(?:\+7|\b8)[\s(-]*\d{3}[\s)-]*\d{3}[\s-]*\d{2}[\s-]*\d{2}\b`)
	// ИНН (10, 12), ОГРН (13, 15), счета (20) и прочие длинные номера
	reNumber = regexp.MustCompile(`\b\d{10,}\b`)
)

// Redact убирает из текста e-mail, телефоны и длинные номера (ИНН, ОГРН, счета) до отправки
// модели (бриф §8). ФИО и названия компаний так не распознать — об этом предупреждает форма.
func Redact(s string) string {
	s = reEmail.ReplaceAllString(s, "[e-mail]")
	s = rePhone.ReplaceAllString(s, "[телефон]")
	return reNumber.ReplaceAllString(s, "[номер]")
}
