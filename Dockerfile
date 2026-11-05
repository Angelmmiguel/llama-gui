# syntax=docker/dockerfile:1

# The web app, compiled into web/build for the Go binary to embed. Corepack
# picks the pnpm version pinned in package.json.
FROM node:22-alpine AS web
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
RUN corepack enable
WORKDIR /src/web
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# The server, statically linked: the SQLite driver is pure Go.
FROM golang:1.26-alpine AS server
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY web/*.go web/
COPY --from=web /src/web/build web/build
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /llama-gui ./cmd/llama-gui

# Not scratch: the llama.cpp builds downloaded at runtime link against these
# libraries, and the tools are for use from a container console.
FROM debian:13-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
        libstdc++6 libgcc-s1 libgomp1 libssl3 ca-certificates libvulkan1 curl jq procps less \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir -p /data /builds /models \
    && chown 99:100 /data /builds /models
COPY --from=server /llama-gui /usr/local/bin/llama-gui
# nobody:users, the owner Unraid gives appdata.
USER 99:100
ENV LLAMA_GUI_ADDR=:8080 LLAMA_GUI_DATA=/data LLAMA_GUI_BUILDS=/builds LLAMA_GUI_MODELS=/models
VOLUME /data /builds /models
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["/usr/local/bin/llama-gui", "healthcheck"]
ENTRYPOINT ["/usr/local/bin/llama-gui", "serve"]
