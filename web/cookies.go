package web

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

// Яндекс Метрика — только с согласия (ч. 3 ст. 6, ч. 1 ст. 9 152-ФЗ; Условия Метрики п. 5.4).
// Выбор посетителя хранится в cookie сайта; без «да» ни скрипт, ни cookie Метрики на страницу
// не попадают, и CSP не разрешает скрипты вовсе.

const choiceCookie = "aktura_metrika"

// metrikaChoice — «yes», «no» или пусто (не выбирал).
func metrikaChoice(r *http.Request) string {
	c, err := r.Cookie(choiceCookie)
	if err != nil || (c.Value != "yes" && c.Value != "no") {
		return ""
	}
	return c.Value
}

func (s *Server) metrikaAllowed(r *http.Request) bool {
	return s.metrika != "" && metrikaChoice(r) == "yes"
}

type cookiesView struct {
	Choice string
	Saved  bool
}

func (s *Server) cookiesPage(w http.ResponseWriter, r *http.Request) {
	s.renderPage(w, r, page{Cookies: &cookiesView{Choice: metrikaChoice(r), Saved: r.URL.Query().Get("saved") != ""}}, nil, nil)
}

// cookiesSet — выбор в баннере или на странице настроек. После выбора в баннере опросник
// показывается с тем же состоянием (ответы пришли в скрытых полях).
func (s *Server) cookiesSet(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	v := r.PostFormValue("metrika")
	if v != "yes" && v != "no" {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secure := r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
	http.SetCookie(w, &http.Cookie{Name: choiceCookie, Value: v, Path: "/", MaxAge: int((365 * 24 * time.Hour).Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	if v == "no" {
		dropMetrikaCookies(w, r)
	}
	// Выбор применяется к этому же ответу: CSP и счётчик считаются по cookie запроса.
	r.AddCookie(&http.Cookie{Name: choiceCookie, Value: v})
	w.Header().Set("Content-Security-Policy", csp)
	if v == "yes" {
		w.Header().Set("Content-Security-Policy", csp+cspMetrika)
	}

	switch ret := r.PostFormValue("ret"); {
	case ret == "quiz":
		answers, hist := s.state(r)
		s.render(w, r, answers, hist)
	case ret == "/privacy" || strings.HasPrefix(ret, "/consent/"):
		http.Redirect(w, r, ret, http.StatusSeeOther)
	default:
		http.Redirect(w, r, "/cookies?saved=1", http.StatusSeeOther)
	}
}

// dropMetrikaCookies — удалить cookie Метрики на домене сайта (названия — по документации
// https://yandex.ru/support/metrica/ru/general/cookie-usage). Метрика ставит их на домен
// сайта, поэтому удаляем в двух вариантах: без Domain и с ним.
func dropMetrikaCookies(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, "_ym") {
			continue
		}
		http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", MaxAge: -1})
		if strings.Contains(host, ".") && net.ParseIP(host) == nil {
			http.SetCookie(w, &http.Cookie{Name: c.Name, Value: "", Path: "/", Domain: host, MaxAge: -1})
		}
	}
}

// metrikaJS — код счётчика: из интерфейса Метрики, но без Вебвизора, карты кликов и ecommerce
// (Вебвизор записывает содержимое полей — описание ситуации и отзыв). Отдаётся отдельным
// файлом, потому что CSP не разрешает встроенные скрипты.
func (s *Server) metrikaJS(w http.ResponseWriter, r *http.Request) {
	if !s.metrikaAllowed(r) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	fmt.Fprintf(w, `(function(m,e,t,r,i,k,a){
  m[i]=m[i]||function(){(m[i].a=m[i].a||[]).push(arguments)};
  m[i].l=1*new Date();
  for (var j = 0; j < document.scripts.length; j++) {if (document.scripts[j].src === r) { return; }}
  k=e.createElement(t),a=e.getElementsByTagName(t)[0],k.async=1,k.src=r,a.parentNode.insertBefore(k,a)
})(window, document, 'script', 'https://mc.yandex.ru/metrika/tag.js?id=%[1]s', 'ym');
ym(%[1]s, 'init', {ssr:true, webvisor:false, clickmap:false, trackHash:false, referrer: document.referrer, url: location.href, accurateTrackBounce:true, trackLinks:true});
`, s.metrika)
}
