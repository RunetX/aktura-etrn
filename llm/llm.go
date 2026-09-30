// Package llm — клиенты языковых моделей за интерфейсом extract.Model (бриф §5: YandexGPT
// и GigaChat, выбор через конфиг). Без SDK: только HTTP. Формат запросов сверен
// с официальной документацией 2026-09-23 (ссылки — у каждого клиента).
//
// Настройка через переменные окружения:
//
//	AKTURA_LLM        yandex | gigachat | пусто (режим без ИИ)
//	YANDEX_API_KEY    API-ключ сервисного аккаунта (роль ai.languageModels.user)
//	YANDEX_FOLDER_ID  каталог
//	YANDEX_MODEL      модель, по умолчанию yandexgpt/latest
//	GIGACHAT_AUTH_KEY ключ авторизации (Basic) из личного кабинета
//	GIGACHAT_SCOPE    GIGACHAT_API_PERS (по умолчанию) | GIGACHAT_API_B2B | GIGACHAT_API_CORP
//	GIGACHAT_MODEL    по умолчанию GigaChat-2-Max
//	GIGACHAT_CA_FILE  PEM с корневым и промежуточным сертификатами НУЦ Минцифры
//	                  (Russian Trusted Root CA и Sub CA, https://www.gosuslugi.ru/crt)
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
	"time"

	"github.com/RunetX/aktura-etrn/extract"
)

// schema — JSON-схема ответа, которого ждёт extract.Parse.
var schema = map[string]any{
	"type": "object",
	"properties": map[string]any{
		"facts": map[string]any{
			"type": "array",
			"items": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"fact":  map[string]any{"type": "string"},
					"value": map[string]any{"type": "string"},
					"quote": map[string]any{"type": "string"},
				},
				"required": []string{"fact", "value", "quote"},
			},
		},
	},
	"required": []string{"facts"},
}

// FromEnv собирает модель по переменным окружения. nil без ошибки — ИИ не настроен.
// Второе значение — название модели для пользователя.
func FromEnv() (extract.Model, string, error) {
	env := func(k, def string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return def
	}
	switch p := os.Getenv("AKTURA_LLM"); p {
	case "":
		return nil, "", nil
	case "yandex":
		y := &Yandex{Key: os.Getenv("YANDEX_API_KEY"), Folder: os.Getenv("YANDEX_FOLDER_ID"), Model: env("YANDEX_MODEL", "yandexgpt/latest")}
		if y.Key == "" || y.Folder == "" {
			return nil, "", fmt.Errorf("AKTURA_LLM=yandex: нужны YANDEX_API_KEY и YANDEX_FOLDER_ID")
		}
		return y, "YandexGPT (Яндекс)", nil
	case "gigachat":
		g := &GigaChat{AuthKey: os.Getenv("GIGACHAT_AUTH_KEY"), Scope: env("GIGACHAT_SCOPE", "GIGACHAT_API_PERS"), Model: env("GIGACHAT_MODEL", "GigaChat-2-Max")}
		if g.AuthKey == "" {
			return nil, "", fmt.Errorf("AKTURA_LLM=gigachat: нужен GIGACHAT_AUTH_KEY")
		}
		if ca := os.Getenv("GIGACHAT_CA_FILE"); ca != "" {
			c, err := clientWithCA(ca)
			if err != nil {
				return nil, "", err
			}
			g.HTTP = c
		}
		return g, "GigaChat (Сбер)", nil
	default:
		return nil, "", fmt.Errorf("AKTURA_LLM=%q: известны yandex, gigachat", p)
	}
}

// Usage — расход токенов по ответам API (запросы к моделям платные).
type Usage struct {
	requests, input, output atomic.Int64
}

func (u *Usage) add(in, out int64) {
	u.requests.Add(1)
	u.input.Add(in)
	u.output.Add(out)
}

// Totals — запросов, входных и выходных токенов с момента создания клиента.
func (u *Usage) Totals() (requests, input, output int64) {
	return u.requests.Load(), u.input.Load(), u.output.Load()
}

var defaultClient = &http.Client{Timeout: 60 * time.Second}

// post отправляет JSON и разбирает JSON-ответ; не-200 — ошибка с началом тела ответа.
func post(ctx context.Context, c *http.Client, url string, hdr map[string]string, body, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	return do(c, req, out)
}

func do(c *http.Client, req *http.Request, out any) error {
	if c == nil {
		c = defaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		if len(raw) > 300 {
			raw = raw[:300]
		}
		return fmt.Errorf("%s: HTTP %d: %s", req.URL.Host, resp.StatusCode, raw)
	}
	return json.Unmarshal(raw, out)
}
