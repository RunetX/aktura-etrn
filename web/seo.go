package web

import (
	"fmt"
	"net/http"
	"strings"
)

// Для поисковиков: robots.txt и sitemap.xml. Адрес сайта — из Host запроса (сайт только по https).

func origin(r *http.Request) string {
	return "https://" + r.Host
}

func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, `User-agent: *
Disallow: /step
Disallow: /describe
Disallow: /feedback
Disallow: /cookies
Disallow: /metrika.js
Disallow: /healthz
Clean-param: _ym_status-check&_ym_lang /

Sitemap: %s/sitemap.xml
`, origin(r))
}

func (s *Server) sitemap(w http.ResponseWriter, r *http.Request) {
	pages := []string{"/"}
	if s.docs != nil {
		pages = append(pages, "/privacy")
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range pages {
		fmt.Fprintf(&b, "  <url><loc>%s%s</loc></url>\n", origin(r), p)
	}
	b.WriteString("</urlset>\n")
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(b.String()))
}
