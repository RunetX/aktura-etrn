// Команда aktura — веб-опросник «Кто есть кто в ЭТрН».
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/RunetX/aktura-etrn/bot"
	"github.com/RunetX/aktura-etrn/bot/maxbot"
	"github.com/RunetX/aktura-etrn/bot/tgbot"
	"github.com/RunetX/aktura-etrn/engine"
	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/llm"
	"github.com/RunetX/aktura-etrn/present"
	"github.com/RunetX/aktura-etrn/quiz"
	"github.com/RunetX/aktura-etrn/store"
	"github.com/RunetX/aktura-etrn/web"
)

func main() {
	addr := flag.String("addr", ":8080", "адрес HTTP-сервера")
	rules := flag.String("rules", "rules/rules.yaml", "база правил")
	questions := flag.String("questions", "rules/questions.yaml", "вопросы опросника")
	aiRate := flag.Int("ai-per-minute", 20, "общий предел разборов описаний в минуту (ИИ-слой)")
	aiDay := flag.Int("ai-per-day", 50, "общий предел разборов описаний в сутки — потолок расходов на модель")
	data := flag.String("data", "", "каталог журнала ситуаций и отзывов; пусто — не ведутся")
	probe := flag.String("probe", "", "проверить здоровье по адресу (для healthcheck контейнера) и выйти")
	flag.Parse()

	if *probe != "" {
		c := http.Client{Timeout: 3 * time.Second}
		resp, err := c.Get(*probe)
		if err != nil || resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		os.Exit(0)
	}

	b, err := engine.Load(*rules)
	if err != nil {
		log.Fatal(err)
	}
	s, err := quiz.Load(*questions)
	if err != nil {
		log.Fatal(err)
	}
	// Реквизиты оператора персональных данных, Метрика и ИИ-слой — из окружения на хосте;
	// в репозиторий не попадают. Без реквизитов сервер откажется включать сбор данных.
	opts := []web.Option{web.WithOperator(web.Operator{
		Name: os.Getenv("AKTURA_OPERATOR"), Contact: os.Getenv("AKTURA_CONTACT"), Address: os.Getenv("AKTURA_OPERATOR_ADDRESS"),
	})}
	if id := os.Getenv("AKTURA_METRIKA_ID"); id != "" {
		opts = append(opts, web.WithMetrika(id))
		log.Printf("Яндекс Метрика %s — только с согласия посетителя", id)
	}
	opts = append(opts, web.WithBots(os.Getenv("AKTURA_MAX_BOT_URL"), os.Getenv("AKTURA_TG_BOT_URL")))
	var st *store.Store
	if *data != "" {
		st, err = store.Open(*data)
		if err != nil {
			log.Fatal(err)
		}
		st.PurgeDaily(log.Printf)
		opts = append(opts, web.WithStore(st))
		log.Printf("журнал ситуаций и отзывы: %s", *data)
	}
	// ИИ-слой (llm/llm.go). GigaChat по своим условиям не принимает персональные данные третьих
	// лиц (соглашения для юрлиц п. 9.6, для физлиц п. 8.7) — на сайте только Yandex; GigaChat — для aktura-eval.
	if os.Getenv("AKTURA_LLM") == "gigachat" {
		log.Fatal("AKTURA_LLM=gigachat нельзя для сайта: условия GigaChat запрещают передавать персональные данные посетителей")
	}
	m, name, err := llm.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	if m != nil {
		opts = append(opts, web.WithExtractor(extract.New(m, s), name, *aiRate, *aiDay, 25*time.Second))
		log.Printf("ИИ-слой: %s, не больше %d разборов в минуту и %d в сутки", name, *aiRate, *aiDay)
	} else {
		log.Printf("ИИ-слой не настроен: режим без ИИ")
	}
	h, err := web.New(b, s, opts...)
	if err != nil {
		log.Fatal(err)
	}
	startBots(b, s, st)
	log.Printf("база правил %s от %s, %d правил; слушаю %s", b.Meta.Version, b.Meta.Date, len(b.Rules), *addr)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      40 * time.Second, // разбор описания моделью — до 25 с
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Fatal(srv.ListenAndServe())
}

// startBots запускает ботов MAX и Telegram, если заданы токены (AKTURA_MAX_TOKEN,
// AKTURA_TG_TOKEN). Один токен — один запущенный процесс: второй получит 409 и дубли.
// Бот держит в памяти ID пользователя мессенджера на время опроса, поэтому без реквизитов
// оператора (политика обработки данных) не запускается.
func startBots(b *engine.Base, s *quiz.Set, st *store.Store) {
	maxTok, tgTok := os.Getenv("AKTURA_MAX_TOKEN"), os.Getenv("AKTURA_TG_TOKEN")
	if maxTok == "" && tgTok == "" {
		return
	}
	if os.Getenv("AKTURA_OPERATOR") == "" || os.Getenv("AKTURA_CONTACT") == "" {
		log.Fatal("боты обрабатывают ID пользователей мессенджеров: задайте реквизиты оператора (AKTURA_OPERATOR, AKTURA_CONTACT)")
	}
	site := os.Getenv("AKTURA_SITE")
	if site == "" {
		site = "https://aktura.ru"
	}
	// Метки ссылок на ботов (?start=klerk) через запятую; «site» разрешена всегда.
	var sources []string
	for _, v := range strings.Split(os.Getenv("AKTURA_BOT_SOURCES"), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			sources = append(sources, v)
		}
	}
	ctx := context.Background()
	pr := present.New(b, s)
	if maxTok != "" {
		mb, err := maxbot.New(maxTok)
		if err != nil {
			log.Fatal(err)
		}
		l := bot.New(pr, mb, bot.Config{Via: "max", Store: st, Site: site, Sources: sources})
		go l.Run(ctx)
		go mb.Run(ctx, l)
		log.Printf("бот MAX запущен")
	}
	if tgTok != "" {
		tb, err := tgbot.New(tgTok, os.Getenv("AKTURA_TG_PROXY"))
		if err != nil {
			log.Fatal(err)
		}
		l := bot.New(pr, tb, bot.Config{Via: "telegram", Store: st, Site: site, Sources: sources})
		go l.Run(ctx)
		go tb.Run(ctx, l)
		log.Printf("бот Telegram запущен (прокси: %v)", os.Getenv("AKTURA_TG_PROXY") != "")
	}
}
