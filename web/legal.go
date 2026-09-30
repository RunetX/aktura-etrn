package web

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"regexp"
	"strings"
	ttemplate "text/template"
)

// Документы о персональных данных (этап 4): политика (ч. 2 ст. 18.1 152-ФЗ) и согласие на
// Метрику — отдельным документом (ч. 1 ст. 9 в ред. 156-ФЗ). Описание для ИИ и комментарий
// к отзыву не предназначены для ПДн (политика, редакция 2), отдельных согласий на них нет.
// Тексты вшиты в бинарник: редакция меняется только новым файлом *.vN.txt (прежние остаются
// как история), а редакция и sha256 политики пишутся рядом с отзывом — какой текст видел человек.

//go:embed legal/*.txt
var legalFS embed.FS

// Operator — реквизиты оператора персональных данных. Задаются окружением на хосте
// (AKTURA_OPERATOR, AKTURA_CONTACT, AKTURA_OPERATOR_ADDRESS) и в репозиторий не попадают.
type Operator struct {
	Name, Contact, Address string
}

// Documents — виды документов и действующие редакции.
var Documents = map[string]string{
	"privacy":         "privacy.v4",
	"consent_metrika": "consent_metrika.v1",
}

type legalDoc struct {
	Kind, Version, Title string
	Body                 string // текст с подставленными реквизитами
	SHA                  string
	Blocks               []block
}

type block struct {
	Head  string
	Para  template.HTML
	Items []template.HTML
}

func loadDocs(op Operator) (map[string]*legalDoc, error) {
	docs := map[string]*legalDoc{}
	for kind, ver := range Documents {
		raw, err := legalFS.ReadFile("legal/" + ver + ".txt")
		if err != nil {
			return nil, err
		}
		t, err := ttemplate.New(ver).Option("missingkey=error").Parse(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", ver, err)
		}
		var b strings.Builder
		if err := t.Execute(&b, op); err != nil {
			return nil, fmt.Errorf("%s: %w", ver, err)
		}
		body := b.String()
		sum := sha256.Sum256([]byte(body))
		title, rest, _ := strings.Cut(body, "\n")
		docs[kind] = &legalDoc{Kind: kind, Version: ver, Title: strings.TrimSpace(title), Body: body,
			SHA: hex.EncodeToString(sum[:]), Blocks: parseBlocks(rest)}
	}
	return docs, nil
}

var reURL = regexp.MustCompile(`https://[^\s)]+[^\s).,;]`)

// linkify экранирует текст и делает ссылки из https-адресов.
func linkify(s string) template.HTML {
	var b strings.Builder
	last := 0
	for _, m := range reURL.FindAllStringIndex(s, -1) {
		b.WriteString(html.EscapeString(s[last:m[0]]))
		u := html.EscapeString(s[m[0]:m[1]])
		fmt.Fprintf(&b, `<a href="%s" rel="noopener" target="_blank">%s</a>`, u, u)
		last = m[1]
	}
	b.WriteString(html.EscapeString(s[last:]))
	return template.HTML(b.String())
}

// parseBlocks — разметка документов: «## » — заголовок, «- » — пункт списка,
// пустая строка разделяет абзацы.
func parseBlocks(s string) []block {
	var out []block
	for _, chunk := range strings.Split(strings.TrimSpace(s), "\n\n") {
		lines := strings.Split(strings.TrimSpace(chunk), "\n")
		switch {
		case strings.HasPrefix(lines[0], "## "):
			out = append(out, block{Head: strings.TrimPrefix(lines[0], "## ")})
			if len(lines) > 1 {
				out = append(out, block{Para: linkify(strings.Join(lines[1:], " "))})
			}
		case strings.HasPrefix(lines[0], "- "):
			var items []template.HTML
			for _, l := range lines {
				if strings.HasPrefix(l, "- ") {
					items = append(items, linkify(strings.TrimPrefix(l, "- ")))
				} else if n := len(items); n > 0 {
					items[n-1] += " " + linkify(l)
				}
			}
			out = append(out, block{Items: items})
		default:
			out = append(out, block{Para: linkify(strings.Join(lines, "\n"))})
		}
	}
	return out
}

func (s *Server) docPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		d := s.docs[kind]
		p := page{Doc: d}
		s.renderPage(w, r, p, nil, nil)
	}
}
