# ==============================================================================
# Gateway Go (transporte Chatwoot + gate de identidade). Binário estático
# (CGO off — o sqlite é modernc, Go puro) sobre Alpine: o wget do busybox
# serve o HEALTHCHECK do compose e o ca-certificates fala TLS com o Mongo/SAC.
# ==============================================================================
FROM golang:1.26-alpine AS build

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
