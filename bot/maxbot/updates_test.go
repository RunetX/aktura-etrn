package maxbot

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/RunetX/aktura-etrn/bot"
)

type rec struct {
	starts    []string
	texts     []string
	callbacks []bot.Callback
}

func (r *rec) Start(_ context.Context, user int64, source string) {
	r.starts = append(r.starts, source)
}
func (r *rec) Text(_ context.Context, _ int64, text string) { r.texts = append(r.texts, text) }
func (r *rec) Callback(_ context.Context, cb bot.Callback)  { r.callbacks = append(r.callbacks, cb) }

// Разбор апдейтов MAX в формате API: метка из bot_started доходит до ядра, нажавший берётся
// из callback.user, сообщения не из диалога и от ботов отбрасываются.
func TestDispatch(t *testing.T) {
	raw := `{"updates":[
	 {"update_type":"bot_started","user":{"user_id":7},"payload":"klerk"},
	 {"update_type":"bot_started","user":{"user_id":8},"payload":null},
	 {"update_type":"message_created","message":{"sender":{"user_id":7},"recipient":{"chat_type":"dialog"},"body":{"mid":"m1","text":"/help"}}},
	 {"update_type":"message_created","message":{"sender":{"user_id":7},"recipient":{"chat_type":"chat"},"body":{"text":"в чате"}}},
	 {"update_type":"message_created","message":{"sender":{"user_id":9,"is_bot":true},"recipient":{"chat_type":"dialog"},"body":{"text":"бот"}}},
	 {"update_type":"message_callback","user_id":1,"callback":{"callback_id":"c1","payload":"1:r","user":{"user_id":7}},"message":{"recipient":{"chat_type":"dialog"},"body":{"mid":"m2"}}}
	],"marker":42}`
	var res struct {
		Updates []update `json:"updates"`
		Marker  *int64   `json:"marker"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		t.Fatal(err)
	}
	r := &rec{}
	b := &Bot{}
	for _, u := range res.Updates {
		b.dispatch(context.Background(), r, u)
	}
	if len(r.starts) != 2 || r.starts[0] != "klerk" || r.starts[1] != "" {
		t.Errorf("старты %q", r.starts)
	}
	if len(r.texts) != 1 || r.texts[0] != "/help" {
		t.Errorf("тексты %q", r.texts)
	}
	if len(r.callbacks) != 1 || r.callbacks[0].UserID != 7 || r.callbacks[0].MessageID != "m2" || r.callbacks[0].AnswerID != "c1" {
		t.Errorf("нажатия %+v", r.callbacks)
	}
	if res.Marker == nil || *res.Marker != 42 {
		t.Errorf("маркер %v", res.Marker)
	}
}
