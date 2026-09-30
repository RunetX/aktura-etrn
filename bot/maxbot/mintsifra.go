package maxbot

// TLS-доверие для platform-api2.max.ru: цепочка завязана на Russian Trusted Root/Sub CA
// Минцифры, которых нет в стандартных хранилищах и в distroless-образе. PEM вшиваются
// из cacert/ (см. README там); взято из notes-parser/internal/maxx/mintsifra.go.

import (
	"crypto/tls"
	"crypto/x509"
	"embed"
	"net/http"
	"path"
	"time"
)

//go:embed all:cacert
var cacertFS embed.FS

// mintsifraClient — клиент с вшитыми CA поверх системного пула и число вшитых сертификатов;
// nil, если PEM в сборку не положили (тогда доверие системного хранилища).
func mintsifraClient() (*http.Client, int) {
	entries, _ := cacertFS.ReadDir("cacert")
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != ".pem" {
			continue
		}
		b, err := cacertFS.ReadFile("cacert/" + e.Name())
		if err == nil && pool.AppendCertsFromPEM(b) {
			n++
		}
	}
	if n == 0 {
		return nil, 0
	}
	return &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{RootCAs: pool},
			Proxy:           http.ProxyFromEnvironment,
		},
	}, n
}
