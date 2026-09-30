package llm

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// GigaChat — GigaChat API v1 (OpenAPI: https://developers.sber.ru/docs/files/openapi/gigachat/api.yml).
// Токен OAuth живёт 30 минут и переиспользуется. Структурированный ответ — response_format
// с type json_schema и schema без обёртки (не как у OpenAI):
// https://developers.sber.ru/docs/ru/gigachat/guides/structured-output
// Сертификаты сервисов выпущены НУЦ Минцифры: нужен GIGACHAT_CA_FILE с корневым и промежуточным
// сертификатами (api.giga.chat промежуточный не отдаёт) — https://developers.sber.ru/docs/ru/gigachat/certificates
type GigaChat struct {
	AuthKey  string // Authorization: Basic <ключ авторизации>
	Scope    string
	Model    string
	OAuthURL string // пусто — боевые адреса
	BaseURL  string
	HTTP     *http.Client
	Usage

	mu      sync.Mutex
	token   string
	expires time.Time
}

const (
	gigaOAuthURL = "https://ngw.devices.sberbank.ru:9443/api/v2/oauth"
	gigaBaseURL  = "https://api.giga.chat"
)

func (g *GigaChat) Complete(ctx context.Context, system, user string) (string, error) {
	tok, err := g.accessToken(ctx)
	if err != nil {
		return "", err
	}
	base := g.BaseURL
	if base == "" {
		base = gigaBaseURL
	}
	type msg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	body := map[string]any{
		"model":           g.Model,
		"messages":        []msg{{"system", system}, {"user", user}},
		"temperature":     0.01, // должна быть > 0; ниже 0.001 — фактически детерминированно
		"max_tokens":      1500,
		"response_format": map[string]any{"type": "json_schema", "schema": schema, "strict": true},
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			Prompt     int64 `json:"prompt_tokens"`
			Completion int64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if err := post(ctx, g.HTTP, base+"/v1/chat/completions", map[string]string{"Authorization": "Bearer " + tok}, body, &out); err != nil {
		return "", err
	}
	g.add(out.Usage.Prompt, out.Usage.Completion)
	if len(out.Choices) == 0 {
		return "", fmt.Errorf("gigachat: пустой ответ")
	}
	if fr := out.Choices[0].FinishReason; fr != "stop" {
		return "", fmt.Errorf("gigachat: ответ не завершён (%s)", fr)
	}
	return out.Choices[0].Message.Content, nil
}

func (g *GigaChat) accessToken(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.token != "" && time.Until(g.expires) > time.Minute {
		return g.token, nil
	}
	u := g.OAuthURL
	if u == "" {
		u = gigaOAuthURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(url.Values{"scope": {g.Scope}}.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("RqUID", uuid4())
	req.Header.Set("Authorization", "Basic "+g.AuthKey)
	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresAt   int64  `json:"expires_at"`
	}
	if err := do(g.HTTP, req, &out); err != nil {
		return "", fmt.Errorf("gigachat oauth: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("gigachat oauth: нет access_token")
	}
	g.token, g.expires = out.AccessToken, expiry(out.ExpiresAt)
	return g.token, nil
}

// expiry: по схеме expires_at — unix-время в миллисекундах, в примере документации — в секундах.
func expiry(v int64) time.Time {
	switch {
	case v <= 0:
		return time.Now().Add(25 * time.Minute)
	case v > 1e12:
		return time.UnixMilli(v)
	default:
		return time.Unix(v, 0)
	}
}

func uuid4() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// clientWithCA — HTTP-клиент, доверяющий системным корням и сертификатам из PEM-файла.
func clientWithCA(path string) (*http.Client, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("%s: нет сертификатов PEM", path)
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Timeout: 60 * time.Second, Transport: tr}, nil
}
