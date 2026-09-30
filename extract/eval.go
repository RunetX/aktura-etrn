package extract

import (
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

// Проверка ИИ-слоя на золотом наборе (бриф §9, §10 этап 3): критерий — ни одного уверенного
// неверного факта. «Не уверен» (факт не извлечён) допустимо: его спросит опросник.

// Situation — ситуация золотого набора (только то, что нужно для проверки извлечения).
type Situation struct {
	ID      string            `yaml:"id"`
	Story   string            `yaml:"story"`
	Facts   map[string]string `yaml:"facts"`
	Assumed map[string]string `yaml:"assumed"`
	// TextFacts — сказано в тексте, но движку не нужно (golden.yaml, шапка)
	TextFacts map[string]string `yaml:"text_facts"`
	Legs      []struct {
		Facts map[string]string `yaml:"facts"`
	} `yaml:"legs"`
}

func LoadGolden(path string) ([]Situation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Situations []Situation `yaml:"situations"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return doc.Situations, nil
}

// Verdict — оценка одного извлечённого факта.
type Verdict string

const (
	Correct Verdict = "correct" // совпал с facts или assumed ситуации
	Wrong   Verdict = "wrong"   // противоречит facts или assumed — уверенная ошибка
	Extra   Verdict = "extra"   // в разметке такого факта нет — разобрать вручную
)

type Mark struct {
	Fact, Value, Want string
	Verdict           Verdict
}

// Expected — разметка ситуации: facts, assumed, text_facts и факты плеч. Если плечи расходятся
// в значении факта, годится любое из них.
func (s Situation) Expected() map[string][]string {
	out := map[string][]string{}
	add := func(m map[string]string) {
		for k, v := range m {
			out[k] = append(out[k], v)
		}
	}
	add(s.Facts)
	add(s.Assumed)
	add(s.TextFacts)
	for _, l := range s.Legs {
		add(l.Facts)
	}
	return out
}

// Score сравнивает извлечённые факты с разметкой.
func Score(s Situation, got map[string]string) []Mark {
	want := s.Expected()
	keys := make([]string, 0, len(got))
	for k := range got {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Mark
	for _, k := range keys {
		m := Mark{Fact: k, Value: got[k]}
		switch vs, ok := want[k]; {
		case !ok:
			m.Verdict = Extra
		case contains(vs, got[k]):
			m.Verdict, m.Want = Correct, got[k]
		default:
			m.Verdict, m.Want = Wrong, fmt.Sprint(vs)
		}
		out = append(out, m)
	}
	return out
}

func contains(vs []string, v string) bool {
	for _, x := range vs {
		if x == v {
			return true
		}
	}
	return false
}
