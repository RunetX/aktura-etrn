// Package quiz — пошаговый опросник без ИИ (бриф §5, §6): задаёт только те вопросы,
// ответов на которые не хватает движку.
package quiz

import (
	"fmt"
	"os"
	"slices"

	"gopkg.in/yaml.v3"

	"github.com/RunetX/aktura-etrn/engine"
)

type Option struct {
	Value string `yaml:"value"` // значение факта (обычный вопрос)
	Fact  string `yaml:"fact"`  // факт-галочка (групповой вопрос)
	Label string `yaml:"label"`
	Hint  string `yaml:"hint"` // подсказка к варианту «как понять» (необязательна)
}

type Question struct {
	Fact      string   `yaml:"fact"`
	Group     string   `yaml:"group"`
	Text      string   `yaml:"text"`
	Hint      string   `yaml:"hint"`
	AI        string   `yaml:"ai"`         // указание ИИ-слою; "skip" — ИИ вопрос не заполняет
	AIExclude []string `yaml:"ai_exclude"` // варианты, которые ИИ не выбирает
	Options   []Option `yaml:"options"`
}

// Facts — факты, на которые отвечает вопрос.
func (q *Question) Facts() []string {
	if q.Group == "" {
		return []string{q.Fact}
	}
	var out []string
	for _, o := range q.Options {
		out = append(out, o.Fact)
	}
	return out
}

// ID — идентификатор вопроса для формы.
func (q *Question) ID() string {
	if q.Group != "" {
		return q.Group
	}
	return q.Fact
}

type Labels struct {
	RoleKeys    map[string]string `yaml:"role_keys"`
	RoleValues  map[string]string `yaml:"role_values"`
	CarriageDoc map[string]string `yaml:"carriage_doc"`
	Form        map[string]string `yaml:"form"`
	FormNotes   map[string]string `yaml:"form_notes"` // пояснение под формой накладной
	FwdDocs     map[string]string `yaml:"forwarding_docs"`
	FwdForm     map[string]string `yaml:"forwarding_form"`
	Certainty   map[string]string `yaml:"certainty"`
	Signers     map[string]string `yaml:"signers"`
}

type Set struct {
	Version   string     `yaml:"version"`
	Date      string     `yaml:"date"`
	Questions []Question `yaml:"questions"`
	Labels    Labels     `yaml:"labels"`
}

func Load(path string) (*Set, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Set
	if err := yaml.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

// Question возвращает вопрос по ID (nil, если нет).
func (s *Set) Question(id string) *Question {
	for i := range s.Questions {
		if s.Questions[i].ID() == id {
			return &s.Questions[i]
		}
	}
	return nil
}

// Next — следующий вопрос или nil, если спрашивать больше нечего.
// Пока сценарий не выбран, спрашивается то, чего не хватает слою roles (Result.Ask);
// когда выбран — уточнения (Result.Pending). Если сценарий так и не выбран (ответили «не знаю»),
// а экспедитор есть — уточнения к экспедиторским документам (тоже Result.Pending).
// Уже отвеченное (в т. ч. «не знаю») не спрашивается.
func (s *Set) Next(b *engine.Base, answers engine.Facts) *Question {
	r := b.Evaluate(answers)
	if r.Error != "" {
		return nil
	}
	if r.RoleRule != nil {
		return s.first(r.Pending, answers)
	}
	if q := s.first(r.Ask, answers); q != nil {
		return q
	}
	return s.first(r.Pending, answers)
}

// first — первый по порядку вопрос, который закрывает ещё не отвеченный факт из need.
func (s *Set) first(need []string, answers engine.Facts) *Question {
	for i := range s.Questions {
		q := &s.Questions[i]
		for _, f := range q.Facts() {
			if _, done := answers[f]; !done && slices.Contains(need, f) {
				return q
			}
		}
	}
	return nil
}
