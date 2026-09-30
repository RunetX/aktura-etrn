# Сборка из исходников: тесты, бинарник, образ без оболочки и root.
#
#   docker build -t aktura-etrn .
#   docker run -p 8080:8080 aktura-etrn
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/aktura ./cmd/aktura

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/aktura /aktura
COPY rules/rules.yaml rules/questions.yaml /rules/
EXPOSE 8080
ENTRYPOINT ["/aktura", "-addr", ":8080", "-rules", "/rules/rules.yaml", "-questions", "/rules/questions.yaml"]
