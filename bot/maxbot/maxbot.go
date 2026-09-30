// Package maxbot — транспорт бота для MAX: long polling, личные сообщения и кнопки.
// По образцу notes-parser/internal/maxx; каналов, чатов обсуждения и вложений здесь нет.
package maxbot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	maxapi "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"

	"github.com/RunetX/aktura-etrn/bot"
)

const (
	// longPollTimeout — сколько сервер держит GetUpdates; меньше таймаута HTTP-клиента (30 с).
	longPollTimeout = 20 * time.Second
	pollErrorPause  = 5 * time.Second
	retryAfter      = 5 * time.Second
)

type Bot struct {
	api   *maxapi.Api
	lim   *bot.Limiter
	token string
	http  *http.Client // для своего приёма апдейтов (updates.go)
}

// New создаёт клиента MAX. Доверие к TLS — вшитые сертификаты Минцифры (cacert/).
func New(token string) (*Bot, error) {
	opts := []maxapi.Opt{maxapi.WithPollingTimeout(longPollTimeout)}
	c, n := mintsifraClient()
	if c != nil {
		opts = append(opts, maxapi.WithHTTPClient(c))
	} else {
		c = &http.Client{Timeout: 30 * time.Second}
	}
	log.Printf("бот MAX: вшито сертификатов Минцифры: %d", n)
	api, err := maxapi.NewApi(token, opts...)
	if err != nil {
		return nil, fmt.Errorf("MAX: %w", err)
	}
	return &Bot{api: api, lim: bot.NewLimiter(time.Second), token: token, http: c}, nil
}

// Run публикует меню команд и принимает апдейты до отмены ctx. Маркер не сохраняется:
// после рестарта сервер повторит неподтверждённые апдейты — повторное нажатие ядро
// распознаёт как устаревшее.
func (b *Bot) Run(ctx context.Context, h bot.Handler) {
	cmds := make([]model.BotCommand, 0, len(bot.Commands))
	for _, c := range bot.Commands {
		cmds = append(cmds, model.BotCommand{Name: c.Name, Description: c.Description})
	}
	if _, err := b.api.Bots.PatchCommands(ctx, model.BotPatchCommands{Commands: cmds}); err != nil {
		log.Printf("бот MAX: меню команд не опубликовано: %v", err)
	}
	var marker int64
	fails := 0
	for ctx.Err() == nil {
		updates, next, err := b.updates(ctx, marker)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if fails++; fails == 1 || fails%12 == 0 {
				log.Printf("бот MAX: приём апдейтов (сбоев подряд %d): %v", fails, err)
			}
			select {
			case <-time.After(pollErrorPause):
			case <-ctx.Done():
				return
			}
			continue
		}
		fails = 0
		if next != 0 {
			marker = next
		}
		for _, u := range updates {
			b.dispatch(ctx, h, u)
		}
	}
}

func (b *Bot) dispatch(ctx context.Context, h bot.Handler, u update) {
	switch u.UpdateType {
	case "bot_started":
		if u.User != nil && u.User.UserID != 0 {
			var source string
			if u.Payload != nil {
				source = *u.Payload
			}
			h.Start(ctx, u.User.UserID, source)
		}
	case "message_created":
		m := u.Message
		if m == nil || m.Sender.IsBot || m.Recipient.ChatType != "dialog" {
			return
		}
		h.Text(ctx, m.Sender.UserID, m.Body.Text)
	case "message_callback":
		if u.Callback == nil || u.Callback.User.UserID == 0 {
			return
		}
		var mid string
		if u.Message != nil {
			if t := u.Message.Recipient.ChatType; t != "" && t != "dialog" {
				return // кнопки бота живут только в личном диалоге
			}
			mid = u.Message.Body.Mid
		}
		h.Callback(ctx, bot.Callback{UserID: u.Callback.User.UserID, AnswerID: u.Callback.CallbackID, MessageID: mid, Payload: u.Callback.Payload})
	}
}

func (b *Bot) Send(ctx context.Context, user int64, text string, kb *bot.Keyboard) string {
	if err := b.lim.Wait(ctx, user); err != nil {
		return ""
	}
	msg := maxapi.NewMessage().SetUser(user).SetText(text).SetDisableLinkPreview(true)
	if mk := keyboard(kb); mk != nil {
		msg.AddKeyboard(mk)
	}
	res, err := b.api.Messages.Send(ctx, msg)
	if err != nil && tooMany(err) {
		select {
		case <-time.After(retryAfter):
		case <-ctx.Done():
			return ""
		}
		res, err = b.api.Messages.Send(ctx, msg)
	}
	if err != nil {
		log.Printf("бот MAX: отправка: %v", err)
		return ""
	}
	return res.Message.Body.Mid
}

// Edit переписывает сообщение целиком: клавиатура в MAX — вложение, отдельно её не снять.
func (b *Bot) Edit(ctx context.Context, user int64, mid, text string, kb *bot.Keyboard) {
	if err := b.lim.Wait(ctx, user); err != nil {
		return
	}
	body := model.NewMessageBody{Text: text, Attachments: []model.Attachment{}}
	if mk := keyboard(kb); mk != nil {
		body.Attachments = append(body.Attachments, mk.Build())
	}
	if _, err := b.api.Messages.EditMessage(ctx, mid, body); err != nil {
		log.Printf("бот MAX: правка сообщения: %v", err)
	}
}

func (b *Bot) AnswerCallback(ctx context.Context, cb bot.Callback, toast string) {
	answer := model.CallbackAnswer{}
	if toast != "" {
		answer.Notification = &toast
	}
	if _, err := b.api.Messages.AnswerOnCallback(ctx, cb.AnswerID, answer); err != nil {
		// нажатие живёт недолго — протухший callback_id обычен
		return
	}
}

func keyboard(kb *bot.Keyboard) *model.Keyboard {
	if kb.Empty() {
		return nil
	}
	mk := model.NewKeyboard()
	for _, row := range kb.Rows {
		r := mk.AddRow()
		for _, btn := range row {
			if btn.URL != "" {
				r.AddLink(btn.Text, btn.URL)
			} else {
				r.AddCallBack(btn.Text, btn.Payload)
			}
		}
	}
	return mk
}

// tooMany — эвристика 429: SDK не отдаёт HTTP-код (как в notes-parser/internal/maxx).
func tooMany(err error) bool {
	var e *maxapi.Error
	if !errors.As(err, &e) {
		return false
	}
	all := strings.ToLower(e.Code + " " + e.Err + " " + e.Message)
	return strings.Contains(all, "too.many") || strings.Contains(all, "too many") || strings.Contains(all, "429")
}
