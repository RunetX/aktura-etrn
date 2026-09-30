// Package tgbot — транспорт бота для Telegram: long polling, личные сообщения и кнопки.
// С российского хоста Telegram Bot API доступен только через прокси (AKTURA_TG_PROXY).
package tgbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	tg "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/RunetX/aktura-etrn/bot"
)

// pollTimeout — сколько Telegram держит getUpdates (минус секунда). Короткий: при обрыве пути
// до прокси зависший запрос обрывается таймаутом клиента (pollTimeout + 5 с), а не через минуту.
const pollTimeout = 20 * time.Second

type Bot struct {
	b   *tg.Bot
	h   bot.Handler
	lim *bot.Limiter
}

// New создаёт клиента Telegram; proxyURL — socks5://логин:пароль@хост:порт или http(s)://…,
// пусто — напрямую.
func New(token, proxyURL string) (*Bot, error) {
	t := &Bot{lim: bot.NewLimiter(time.Second)}
	opts := []tg.Option{
		tg.WithSkipGetMe(),
		tg.WithDefaultHandler(func(ctx context.Context, _ *tg.Bot, u *models.Update) { t.handle(ctx, u) }),
		tg.WithErrorsHandler(func(err error) { log.Printf("бот Telegram: %v", err) }),
	}
	if proxyURL != "" {
		u, err := url.Parse(proxyURL)
		if err != nil || u.Host == "" {
			return nil, errors.New("AKTURA_TG_PROXY не разбирается") // строку не печатаем: в ней пароль
		}
		switch u.Scheme {
		case "socks5", "socks5h", "http", "https":
		default:
			return nil, fmt.Errorf("AKTURA_TG_PROXY: неизвестная схема %q", u.Scheme)
		}
		client := &http.Client{Timeout: pollTimeout + 5*time.Second, Transport: &http.Transport{
			Proxy:               http.ProxyURL(u),
			DialContext:         (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
		}}
		opts = append(opts, tg.WithHTTPClient(pollTimeout, client))
	}
	b, err := tg.New(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("Telegram: %w", err)
	}
	t.b = b
	return t, nil
}

// Run публикует меню команд и принимает апдейты до отмены ctx.
func (t *Bot) Run(ctx context.Context, h bot.Handler) {
	t.h = h
	cmds := make([]models.BotCommand, 0, len(bot.Commands))
	for _, c := range bot.Commands {
		cmds = append(cmds, models.BotCommand{Command: c.Name, Description: c.Description})
	}
	if _, err := t.b.SetMyCommands(ctx, &tg.SetMyCommandsParams{Commands: cmds}); err != nil {
		log.Printf("бот Telegram: меню команд не опубликовано: %v", err)
	}
	t.b.Start(ctx)
}

func (t *Bot) handle(ctx context.Context, u *models.Update) {
	switch {
	case u.Message != nil:
		m := u.Message
		if m.Chat.Type != models.ChatTypePrivate || m.From == nil || m.From.IsBot {
			return
		}
		t.h.Text(ctx, m.From.ID, m.Text)
	case u.CallbackQuery != nil:
		cq := u.CallbackQuery
		var mid string
		if m := cq.Message.Message; m != nil {
			if m.Chat.Type != models.ChatTypePrivate {
				return
			}
			mid = strconv.Itoa(m.ID)
		}
		t.h.Callback(ctx, bot.Callback{UserID: cq.From.ID, AnswerID: cq.ID, MessageID: mid, Payload: cq.Data})
	}
}

func (t *Bot) Send(ctx context.Context, user int64, text string, kb *bot.Keyboard) string {
	p := &tg.SendMessageParams{ChatID: user, Text: text, ReplyMarkup: markup(kb),
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tg.True()}}
	var m *models.Message
	err := t.retry(ctx, user, func(ctx context.Context) (err error) { m, err = t.b.SendMessage(ctx, p); return })
	if err != nil {
		log.Printf("бот Telegram: отправка: %v", err)
		return ""
	}
	return strconv.Itoa(m.ID)
}

func (t *Bot) Edit(ctx context.Context, user int64, messageID, text string, kb *bot.Keyboard) {
	id, err := strconv.Atoi(messageID)
	if err != nil {
		return
	}
	p := &tg.EditMessageTextParams{ChatID: user, MessageID: id, Text: text, ReplyMarkup: markup(kb),
		LinkPreviewOptions: &models.LinkPreviewOptions{IsDisabled: tg.True()}}
	if err := t.retry(ctx, user, func(ctx context.Context) error { _, err := t.b.EditMessageText(ctx, p); return err }); err != nil {
		// «message is not modified» и слишком старые сообщения — штатные отказы
		log.Printf("бот Telegram: правка сообщения: %v", err)
	}
}

func (t *Bot) AnswerCallback(ctx context.Context, cb bot.Callback, toast string) {
	// протухший callback_query — обычное дело, ошибку не печатаем; «спиннер» живёт недолго —
	// одна попытка с коротким сроком, без повторов
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	_, _ = t.b.AnswerCallbackQuery(ctx, &tg.AnswerCallbackQueryParams{CallbackQueryID: cb.AnswerID, Text: toast})
}

// attemptTimeout — срок одной попытки. Путь от хоста до прокси время от времени рвётся:
// запрос повисает на рукопожатии (в журнале danted — «negotiate timeout»), а здоровый
// проходит за доли секунды. Проверено с хоста 24.09.2026: 3 из 5 — 0,3 с, 2 — обрыв.
const attemptTimeout = 15 * time.Second

// retry — через лимитер пользователя; до трёх попыток при сетевой ошибке (срок каждой —
// attemptTimeout) и одна повторная после 429. Повтор отправки после обрыва ответа может
// дать дубль сообщения — это лучше, чем потерянный вопрос.
func (t *Bot) retry(ctx context.Context, user int64, call func(context.Context) error) error {
	if err := t.lim.Wait(ctx, user); err != nil {
		return err
	}
	attempt := func() error {
		actx, cancel := context.WithTimeout(ctx, attemptTimeout)
		defer cancel()
		return call(actx)
	}
	var err error
	for i := 0; i < 3; i++ {
		err = attempt()
		var ue *url.Error
		if !errors.As(err, &ue) || ctx.Err() != nil {
			break
		}
		log.Printf("бот Telegram: сетевая ошибка, попытка %d: %v", i+1, ue.Err)
	}
	var tm *tg.TooManyRequestsError
	if errors.As(err, &tm) {
		select {
		case <-time.After(time.Duration(tm.RetryAfter+1) * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
		err = attempt()
	}
	return err
}

// markup — клавиатура в формате Telegram; пустая — nil (кнопки снимаются).
func markup(kb *bot.Keyboard) models.ReplyMarkup {
	if kb.Empty() {
		return nil
	}
	rows := make([][]models.InlineKeyboardButton, 0, len(kb.Rows))
	for _, row := range kb.Rows {
		r := make([]models.InlineKeyboardButton, 0, len(row))
		for _, b := range row {
			if b.URL != "" {
				r = append(r, models.InlineKeyboardButton{Text: b.Text, URL: b.URL})
			} else {
				r = append(r, models.InlineKeyboardButton{Text: b.Text, CallbackData: b.Payload})
			}
		}
		rows = append(rows, r)
	}
	return models.InlineKeyboardMarkup{InlineKeyboard: rows}
}
