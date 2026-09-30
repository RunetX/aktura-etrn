package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestYandex(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "Api-Key k1" {
			t.Errorf("Authorization = %q", a)
		}
		if r.Header.Get("x-data-logging-enabled") != "false" {
			t.Error("не отключено логирование запросов на стороне Яндекса")
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"result":{"alternatives":[{"message":{"role":"assistant","text":"{\"facts\":[]}"},"status":"ALTERNATIVE_STATUS_FINAL"}],"usage":{"inputTextTokens":"87","completionTokens":"12","totalTokens":"99"},"modelVersion":"x"}}`)
	}))
	defer srv.Close()

	y := &Yandex{Key: "k1", Folder: "b1g", Model: "yandexgpt/latest", URL: srv.URL}
	text, err := y.Complete(context.Background(), "SYS", "USER")
	if err != nil {
		t.Fatal(err)
	}
	if text != `{"facts":[]}` {
		t.Errorf("текст ответа %q", text)
	}
	if n, in, out := y.Totals(); n != 1 || in != 87 || out != 12 {
		t.Errorf("расход: %d запросов, %d/%d токенов", n, in, out)
	}
	if got["modelUri"] != "gpt://b1g/yandexgpt/latest" {
		t.Errorf("modelUri = %v", got["modelUri"])
	}
	if opts := got["completionOptions"].(map[string]any); opts["maxTokens"] != "1500" {
		t.Errorf("maxTokens должен быть строкой: %#v", opts["maxTokens"])
	}
	if _, ok := got["jsonSchema"].(map[string]any)["schema"]; !ok || got["jsonObject"] != nil {
		t.Error("нужен ровно один из jsonSchema/jsonObject — jsonSchema")
	}
	msgs := got["messages"].([]any)
	if len(msgs) != 2 || msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["text"] != "USER" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestCount(t *testing.T) {
	var v struct{ A, B, C count }
	if err := json.Unmarshal([]byte(`{"A":"87","B":12,"C":"x"}`), &v); err != nil || v.A != 87 || v.B != 12 || v.C != 0 {
		t.Errorf("%+v %v", v, err)
	}
}

func TestYandexErrors(t *testing.T) {
	for name, c := range map[string]struct {
		code int
		body string
	}{
		"HTTP 401":     {401, `{"error":"unauthorized"}`},
		"пусто":        {200, `{"result":{"alternatives":[]}}`},
		"не завершено": {200, `{"result":{"alternatives":[{"message":{"text":"{"},"status":"ALTERNATIVE_STATUS_TRUNCATED_FINAL"}]}}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(c.code)
			io.WriteString(w, c.body)
		}))
		y := &Yandex{Key: "k", Folder: "f", Model: "m", URL: srv.URL}
		if _, err := y.Complete(context.Background(), "s", "u"); err == nil {
			t.Errorf("%s: нет ошибки", name)
		}
		srv.Close()
	}
}

func TestGigaChat(t *testing.T) {
	oauthCalls := 0
	var got map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("POST /oauth", func(w http.ResponseWriter, r *http.Request) {
		oauthCalls++
		r.ParseForm()
		if r.Header.Get("Authorization") != "Basic AK" || r.PostFormValue("scope") != "GIGACHAT_API_PERS" || len(r.Header.Get("RqUID")) != 36 {
			t.Errorf("oauth: заголовки %v, scope %q", r.Header, r.PostFormValue("scope"))
		}
		// expires_at в миллисекундах, через 30 минут
		json.NewEncoder(w).Encode(map[string]any{"access_token": "TOK", "expires_at": time.Now().Add(30 * time.Minute).UnixMilli()})
	})
	mux.HandleFunc("POST /v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer TOK" {
			t.Errorf("chat: Authorization = %q", r.Header.Get("Authorization"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"facts\":[]}","role":"assistant"},"index":0,"finish_reason":"stop"}],"model":"GigaChat:2","object":"chat.completion","usage":{"prompt_tokens":55,"completion_tokens":30,"total_tokens":85}}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &GigaChat{AuthKey: "AK", Scope: "GIGACHAT_API_PERS", Model: "GigaChat-2-Max", OAuthURL: srv.URL + "/oauth", BaseURL: srv.URL}
	for range 2 {
		text, err := g.Complete(context.Background(), "SYS", "USER")
		if err != nil {
			t.Fatal(err)
		}
		if text != `{"facts":[]}` {
			t.Errorf("текст ответа %q", text)
		}
	}
	if n, in, out := g.Totals(); n != 2 || in != 110 || out != 60 {
		t.Errorf("расход: %d запросов, %d/%d токенов", n, in, out)
	}
	if oauthCalls != 1 {
		t.Errorf("токен запрошен %d раз, должен переиспользоваться", oauthCalls)
	}
	rf := got["response_format"].(map[string]any)
	if rf["type"] != "json_schema" || rf["schema"] == nil || rf["json_schema"] != nil || rf["strict"] != true {
		t.Errorf("response_format = %v", rf)
	}
	if got["model"] != "GigaChat-2-Max" || got["temperature"].(float64) <= 0 {
		t.Errorf("model/temperature: %v %v", got["model"], got["temperature"])
	}
	if msgs := got["messages"].([]any); msgs[0].(map[string]any)["role"] != "system" || msgs[1].(map[string]any)["content"] != "USER" {
		t.Errorf("messages = %v", msgs)
	}
}

func TestExpiry(t *testing.T) {
	now := time.Now()
	if d := time.Until(expiry(now.Add(30 * time.Minute).UnixMilli())); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("миллисекунды: %v", d)
	}
	if d := time.Until(expiry(now.Add(30 * time.Minute).Unix())); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("секунды: %v", d)
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv("AKTURA_LLM", "")
	if m, _, err := FromEnv(); m != nil || err != nil {
		t.Error("без AKTURA_LLM — режим без ИИ")
	}
	t.Setenv("AKTURA_LLM", "yandex")
	t.Setenv("YANDEX_API_KEY", "")
	if _, _, err := FromEnv(); err == nil {
		t.Error("yandex без ключа должен давать ошибку")
	}
	t.Setenv("YANDEX_API_KEY", "k")
	t.Setenv("YANDEX_FOLDER_ID", "f")
	if m, name, err := FromEnv(); err != nil || m == nil || !strings.Contains(name, "YandexGPT") {
		t.Errorf("yandex: %v %q %v", m, name, err)
	}
	t.Setenv("AKTURA_LLM", "openai")
	if _, _, err := FromEnv(); err == nil {
		t.Error("неизвестный провайдер должен давать ошибку")
	}
}
