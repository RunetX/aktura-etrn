package web

import (
	"log"
	"net/http"
	"strings"

	"github.com/RunetX/aktura-etrn/extract"
	"github.com/RunetX/aktura-etrn/store"
)

// MaxComment — предел комментария к отзыву (в символах).
const MaxComment = 1000

// feedback — «В моей ситуации это не так» (бриф §7, §8). Сохраняется комментарий, очищенный
// от e-mail, телефонов и номеров, ответы и итог — с редакцией и sha256 политики, которую
// видел человек (комментарий не предназначен для ПДн — политика, редакция 2).
func (s *Server) feedback(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	answers, hist := s.state(r)
	comment := strings.TrimSpace(r.PostFormValue("comment"))
	fb := &fbView{Comment: comment}
	doc := s.docs["privacy"]
	if comment == "" {
		fb.Error = "Напишите, что не так."
		s.renderPage(w, r, page{FB: fb}, answers, hist)
		return
	}
	res := s.base.Evaluate(answers)
	rec := store.Feedback{Day: s.store.Today(), Facts: answers, Rules: res.AllRuleIDs(),
		Comment: extract.Redact(truncateRunes(comment, MaxComment)), Policy: doc.Version, PolicySHA: doc.SHA,
		RulesVer: s.base.Meta.Version}
	if err := s.store.AddFeedback(rec); err != nil {
		log.Printf("отзыв: %v", err)
		fb.Error = "Не удалось сохранить отзыв. Попробуйте позже."
		s.renderPage(w, r, page{FB: fb}, answers, hist)
		return
	}
	log.Printf("отзыв принят (правила %v)", res.AllRuleIDs())
	s.renderPage(w, r, page{FB: &fbView{Sent: true}}, answers, hist)
}

func truncateRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
