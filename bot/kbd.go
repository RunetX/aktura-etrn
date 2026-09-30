package bot

import (
	"context"
	"strings"
)

// Элементы интерфейса, общие для мессенджеров (по образцу notes-parser/internal/kbd).
// Ядро собирает клавиатуру и разбирает payload, транспорты переводят её в формат своего API.

const (
	// Version — версия формата payload: кнопка живёт в истории чата сколько угодно долго,
	// нажатие из прошлого релиза надо уметь отличить.
	Version = "1"
	// PayloadLimit — предел Telegram на callback_data в байтах (у MAX 1024, равняемся по строгому).
	// Payload — только ASCII: идентификаторы вопросов и значения из questions.yaml.
	PayloadLimit = 64
)

// Button — кнопка: подпись; Payload — ASCII из Pack; URL — кнопка-ссылка (тогда Payload пуст).
type Button struct {
	Text    string
	Payload string
	URL     string
}

// Keyboard — клавиатура под сообщением, строками сверху вниз.
type Keyboard struct{ Rows [][]Button }

func (k *Keyboard) Row(btns ...Button) *Keyboard {
	if len(btns) > 0 {
		k.Rows = append(k.Rows, btns)
	}
	return k
}

// Empty — клавиатуры нет (nil-safe).
func (k *Keyboard) Empty() bool { return k == nil || len(k.Rows) == 0 }

// Callback — нажатие, приведённое к общему виду.
type Callback struct {
	UserID int64
	// AnswerID — чем гасить «спиннер»: callback_query.id в Telegram, callback_id в MAX.
	AnswerID string
	// MessageID — сообщение с клавиатурой; пусто — сообщение недоступно.
	MessageID string
	Payload   string
}

// Command — пункт меню команд бота (без ведущего слэша).
type Command struct{ Name, Description string }

// Transport — отправка сообщений в конкретном мессенджере. ID сообщений строковые
// (Telegram — число строкой, MAX — mid). Текст — без разметки.
type Transport interface {
	// Send отправляет сообщение и возвращает его ID (пусто — не отправлено).
	Send(ctx context.Context, userID int64, text string, kb *Keyboard) string
	// Edit переписывает сообщение; kb == nil — снять кнопки.
	Edit(ctx context.Context, userID int64, messageID, text string, kb *Keyboard)
	// AnswerCallback гасит «спиннер»; пустой toast — молча.
	AnswerCallback(ctx context.Context, cb Callback, toast string)
}

// Pack собирает payload «<версия>:<verb>[:<arg>]».
func Pack(verb, arg string) string {
	if arg == "" {
		return Version + ":" + verb
	}
	return Version + ":" + verb + ":" + arg
}

// Parse разбирает payload; ok == false — мусор или кнопка чужой версии.
func Parse(payload string) (verb, arg string, ok bool) {
	parts := strings.SplitN(payload, ":", 3)
	if len(parts) < 2 || parts[0] != Version || parts[1] == "" {
		return "", "", false
	}
	if len(parts) == 3 {
		arg = parts[2]
	}
	return parts[1], arg, true
}

// Handler — то, что транспорт зовёт на входящие события (реализует Logic).
type Handler interface {
	// Start — пользователь запустил бота (MAX: bot_started); /start приходит как Text.
	// source — метка из ссылки на бота (?start=…), как пришла; пусто — без метки.
	Start(ctx context.Context, user int64, source string)
	Text(ctx context.Context, user int64, text string)
	Callback(ctx context.Context, cb Callback)
}
