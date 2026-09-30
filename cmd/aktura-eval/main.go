// Команда aktura-eval — проверка ИИ-слоя на золотом наборе (бриф §9, §10 этап 3):
// подаёт модели story каждой ситуации и сверяет извлечённые факты с разметкой.
// Критерий этапа — ни одного уверенного неверного факта (wrong); выход 1, если они есть.
// «extra» — факта нет в разметке: разобрать вручную (часто разметка неполна).
//
// Запросы к модели платные: без -run команда только считает, сколько запросов сделает.
// Полный прогон — перед выкаткой; при правке подсказок — -only по затронутым ситуациям.
//
//	AKTURA_LLM=yandex YANDEX_API_KEY=... YANDEX_FOLDER_ID=... go run ./cmd/aktura-eval -only G-001 -run
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/llm"
	"github.com/RunetX/aktura-etrn/quiz"
)

func main() {
	golden := flag.String("golden", "rules/golden.yaml", "золотой набор")
	questions := flag.String("questions", "rules/questions.yaml", "вопросы опросника")
	only := flag.String("only", "", "только ситуации с этими id через запятую")
	verbose := flag.Bool("v", false, "показывать принятые и отброшенные предложения модели")
	run := flag.Bool("run", false, "отправить запросы модели (платно); без флага — только оценка")
	flag.Parse()

	m, name, err := llm.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	if m == nil {
		log.Fatal("модель не настроена: задайте AKTURA_LLM и ключи (см. llm/llm.go)")
	}
	s, err := quiz.Load(*questions)
	if err != nil {
		log.Fatal(err)
	}
	sits, err := extract.LoadGolden(*golden)
	if err != nil {
		log.Fatal(err)
	}
	ext := extract.New(m, s)

	n := 0
	for _, sit := range sits {
		if *only == "" || strings.Contains(","+*only+",", ","+sit.ID+",") {
			n++
		}
	}
	fmt.Printf("запросов к модели %s: %d, инструкция %d символов на каждый запрос\n", name, n, utf8.RuneCountInString(ext.Prompt()))
	if !*run {
		fmt.Println("это оценка: запросы платные, для прогона добавьте -run")
		return
	}

	total := map[extract.Verdict]int{}
	missed, failed := 0, 0
	for _, sit := range sits {
		if *only != "" && !strings.Contains(","+*only+",", ","+sit.ID+",") {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		r, err := ext.Extract(ctx, sit.Story)
		cancel()
		if err != nil {
			fmt.Printf("%s  ОШИБКА МОДЕЛИ: %v\n", sit.ID, err)
			failed++
			continue
		}
		marks := extract.Score(sit, r.Facts)
		var parts []string
		for _, mk := range marks {
			total[mk.Verdict]++
			switch mk.Verdict {
			case extract.Wrong:
				parts = append(parts, fmt.Sprintf("WRONG %s=%s (нужно %s)", mk.Fact, mk.Value, mk.Want))
			case extract.Extra:
				parts = append(parts, fmt.Sprintf("extra %s=%s", mk.Fact, mk.Value))
			}
		}
		n := 0
		for k := range sit.Facts {
			if _, ok := r.Facts[k]; !ok {
				n++
			}
		}
		missed += n
		fmt.Printf("%s  верно %d, не извлечено %d из %d  %s\n", sit.ID, count(marks, extract.Correct), n, len(sit.Facts), strings.Join(parts, "; "))
		if *verbose {
			for _, it := range r.Accepted {
				fmt.Printf("      принято %s=%s «%s»\n", it.Fact, it.Value, it.Quote)
			}
			for _, rj := range r.Rejected {
				fmt.Printf("      отброшено %s=%s «%s»: %s\n", rj.Fact, rj.Value, rj.Quote, rj.Reason)
			}
		}
	}
	fmt.Printf("\nмодель %s: верно %d, уверенных ошибок %d, вне разметки %d, не извлечено %d, сбоев %d\n",
		name, total[extract.Correct], total[extract.Wrong], total[extract.Extra], missed, failed)
	if u, ok := m.(interface{ Totals() (int64, int64, int64) }); ok {
		req, in, out := u.Totals()
		fmt.Printf("расход по ответам API: %d запросов, токенов на входе %d, на выходе %d (цена — в тарифах и биллинге провайдера)\n", req, in, out)
	}
	if total[extract.Wrong] > 0 {
		os.Exit(1)
	}
}

func count(ms []extract.Mark, v extract.Verdict) int {
	n := 0
	for _, m := range ms {
		if m.Verdict == v {
			n++
		}
	}
	return n
}
