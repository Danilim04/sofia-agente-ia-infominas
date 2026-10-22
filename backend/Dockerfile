# ==============================================================================
# Gateway Go da Sofia (transporte Infodesk). Binário estático (CGO off — o
# sqlite é modernc, Go puro) sobre Alpine: o wget do busybox serve o
# HEALTHCHECK do compose e o ca-certificates fala TLS com o Infodesk.
# Roda como usuário não root (uid 10001). Imagem: sofia-bot:gateway-<tag>.
# A taxonomia vem do volume /config do compose (TAXONOMIA_PATH); o binário
# não embute config.
# ==============================================================================
# Patch fixo, igual ao `go` do go.mod (govulncheck: stdlib corrigida a partir do 1.26.10).
FROM golang:1.26.10-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/bot ./cmd/bot

FROM alpine:3.22

RUN apk add --no-cache ca-certificates tzdata && \
    adduser -D -u 10001 app && \
    mkdir -p /data && chown app:app /data

COPY --from=build /out/bot /usr/local/bin/bot
USER app

# Fallback SQLite (só quando PG_URL está vazio) — montar volume em /data.
# Com PG_URL o estado do gateway vive no Postgres (schema `gateway`).
ENV DB_PATH=/data/gateway.db
VOLUME /data

EXPOSE 8080
CMD ["bot"]
