package maxbot

// Приём апдейтов MAX своим запросом GET /updates. SDK (v2.2.4) при разборе теряет поле payload
// события bot_started — метку из ссылки https://max.ru/<бот>?start=<метка> (до 128 символов),
// а по ней журнал отличает, откуда пришли. Отправка сообщений — по-прежнему через SDK.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const apiBase = "https://platform-api2.max.ru"

// update — только те поля, что нужны боту.
type update struct {
	UpdateType string  `json:"update_type"`
	Payload    *string `json:"payload"` // bot_started: метка из ссылки
	User       *struct {
		UserID int64 `json:"user_id"`
	} `json:"user"`
	Message *struct {
		Sender struct {
			UserID int64 `json:"user_id"`
			IsBot  bool  `json:"is_bot"`
		} `json:"sender"`
		Recipient struct {
			ChatType string `json:"chat_type"`
		} `json:"recipient"`
		Body struct {
			Mid  string `json:"mid"`
			Text string `json:"text"`
		} `json:"body"`
	} `json:"message"`
	Callback *struct {
		CallbackID string `json:"callback_id"`
		Payload    string `json:"payload"`
		User       struct {
			UserID int64 `json:"user_id"`
		} `json:"user"`
	} `json:"callback"`
}

func (b *Bot) updates(ctx context.Context, marker int64) ([]update, int64, error) {
	q := url.Values{}
	q.Set("limit", "50")
	q.Set("timeout", strconv.Itoa(int(longPollTimeout.Seconds())))
	if marker > 0 {
		q.Set("marker", strconv.FormatInt(marker, 10))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiBase+"/updates?"+q.Encode(), nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", b.token)
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, 0, fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	var res struct {
		Updates []update `json:"updates"`
		Marker  *int64   `json:"marker"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
		return nil, 0, err
	}
	var next int64
	if res.Marker != nil {
		next = *res.Marker
	}
	return res.Updates, next, nil
}
