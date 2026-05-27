# syntax=docker/dockerfile:1

# --- Stage 1: build the Vue client -----------------------------------------
FROM node:22-alpine AS client
WORKDIR /client
COPY client/package.json client/package-lock.json ./
RUN npm ci
COPY client/ ./
RUN npm run build

# --- Stage 2: build the Go daemon (static, pure-Go SQLite → CGO off) -------
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags "-s -w -X main.buildVersion=docker" -o /out/stugan ./cmd/stugan

# --- Stage 3: minimal runtime ----------------------------------------------
FROM alpine:3.20
# CA certificates are required for IRC over TLS and HTTPS link previews.
RUN apk add --no-cache ca-certificates && adduser -D -u 10001 stugan
WORKDIR /app
COPY --from=build /out/stugan /app/stugan
COPY --from=client /client/dist /app/client/dist

# Config, history, scripts, and uploads live here — mount a volume.
ENV STUGAN_HOME=/data
VOLUME /data
EXPOSE 8080
USER stugan

# NOTE: in config.toml set `listen = "0.0.0.0:8080"` so the daemon is
# reachable from outside the container, and set `origin_patterns` /
# `public_url` if serving from a non-localhost host.
ENTRYPOINT ["/app/stugan"]
