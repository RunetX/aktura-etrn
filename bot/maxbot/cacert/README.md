# Сертификаты Минцифры для platform-api2.max.ru

TLS-цепочка API MAX подписана Russian Trusted Root/Sub CA, которых нет в стандартных хранилищах
(и в distroless-образе). Положите сюда официальные PEM **перед сборкой** — они вшиваются
через `go:embed` (`../mintsifra.go`):

    russian_trusted_root_ca.pem
    russian_trusted_sub_ca.pem

Источник — Госуслуги (https://www.gosuslugi.ru/crt):

    https://gu-st.ru/content/lending/russian_trusted_root_ca_pem.crt
    https://gu-st.ru/content/lending/russian_trusted_sub_ca_pem.crt

Расширение переименовать в `.pem`. Файлы в git не попадают (как в notes-parser): подлинность
проверяется при скачивании. Без них клиент MAX доверяет только системному хранилищу, и в
контейнере TLS не пройдёт — при запуске в журнале видно число вшитых сертификатов (нужно 2).

Склеивать для curl: `for f in *.pem; do cat "$f"; echo; done` — в конце файлов нет перевода строки.
