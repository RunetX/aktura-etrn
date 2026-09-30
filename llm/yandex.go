package llm

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Yandex — YandexGPT через нативный API AI Studio (TextGeneration.completion).
// https://aistudio.yandex.ru/docs/en/ai-studio/text-generation/api-ref/TextGeneration/completion.html
// Структурированный ответ — jsonSchema:
// https://aistudio.yandex.ru/docs/en/ai-studio/concepts/generation/structured-output.html
type Yandex struct {
	Key    string // Authorization: Api-Key <ключ>
	Folder string
	Model  string // например yandexgpt/latest → gpt://<каталог>/yandexgpt/latest
	URL    string // пусто — боевой адрес
	HTTP   *http.Client
	Usage
}

const yandexURL = "https://llm.api.cloud.yandex.net/foundationModels/v1/completion"

func (y *Yandex) Complete(ctx context.Context, system, user string) (string, error) {
	url := y.URL
	if url == "" {
		url = yandexURL
	}
	type msg struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	body := map[string]any{
		"modelUri": fmt.Sprintf("gpt://%s/%s", y.Folder, y.Model),
		// maxTokens — int64, в JSON строкой (по справочнику API)
		"completionOptions": map[string]any{"stream": false, "temperature": 0, "maxTokens": "1500"},
		"messages":          []msg{{"system", system}, {"user", user}},
		"jsonSchema":        map[string]any{"schema": schema},
	}
	// Ответ REST обёрнут в result; на случай ответа без обёртки читаем и верхний уровень.
	type alt struct {
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		Status string `json:"status"`
	}
	type usage struct {
		Input  count `json:"inputTextTokens"`
		Output count `json:"completionTokens"`
	}
	var out struct {
		Result struct {
			Alternatives []alt `json:"alternatives"`
			Usage        usage `json:"usage"`
		} `json:"result"`
		Alternatives []alt `json:"alternatives"`
		Usage        usage `json:"usage"`
	}
	hdr := map[string]string{
		"Authorization": "Api-Key " + y.Key,
		// Без него сервис сохраняет запросы и может использовать их для обучения (условия Foundation
		// Models п. 5.4.1): https://aistudio.yandex.ru/docs/ru/ai-studio/operations/disable-logging.html
		"x-data-logging-enabled": "false",
	}
	if err := post(ctx, y.HTTP, url, hdr, body, &out); err != nil {
		return "", err
	}
	alts, u := out.Result.Alternatives, out.Result.Usage
	if len(alts) == 0 {
		alts, u = out.Alternatives, out.Usage
	}
	y.add(int64(u.Input), int64(u.Output))
	if len(alts) == 0 {
		return "", fmt.Errorf("yandex: пустой ответ")
	}
	if s := alts[0].Status; s != "" && s != "ALTERNATIVE_STATUS_FINAL" {
		return "", fmt.Errorf("yandex: статус ответа %s", s)
	}
	return alts[0].Message.Text, nil
}

// count — число токенов: по справочнику API int64 приходит строкой ("87"); число тоже принимается,
// а нечитаемое значение считается нулём — учёт расхода не должен ронять разбор ответа.
type count int64

func (c *count) UnmarshalJSON(b []byte) error {
	n, err := strconv.ParseInt(strings.Trim(string(b), `"`), 10, 64)
	if err == nil {
		*c = count(n)
	}
	return nil
}
