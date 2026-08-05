# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /app
ENV CGO_ENABLED=0 GOOS=linux

# Зависимости отдельным слоем + кэш модулей: пересобирается только при
# изменении go.mod, а не на каждую правку кода.
COPY go.mod go.su[m] ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download 2>/dev/null || true

COPY . .
# Кэш модулей и кэш компиляции переживают пересборки — сборка идёт секунды.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    go mod tidy && go build -trimpath -ldflags="-s -w" -o /userbot ./cmd

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata
COPY --from=build /userbot /userbot
VOLUME /data
ENTRYPOINT ["/userbot"]
