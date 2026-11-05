# Development commands. Run them inside the Nix shell (`nix develop`, or let
# direnv load it for you).

# Private overrides such as LLAMA_GUI_ADDR live in .env.local (gitignored).
# just loads it on every run, so a change takes effect without re-entering the
# shell; variables already set in the environment win.
set dotenv-load := true
set dotenv-filename := ".env.local"

default: test

# Run the server with its state, builds and models under ./tmp.
serve *args:
    go run ./cmd/llama-gui serve --data ./tmp/data --builds ./tmp/builds --models ./tmp/models {{args}}

# Run the Go server and the Vite dev server together, until Ctrl-C.
dev:
    #!/usr/bin/env bash
    set -euo pipefail
    just serve &
    just web-dev &
    trap 'kill 0' EXIT INT TERM
    wait -n

# Run the Go and web test suites.
test: web-test
    go test -race ./...

# Vet and lint everything.
lint: web-check
    go vet ./...
    golangci-lint run

# Build the binary into bin/, with the web app embedded.
build: web-build
    go build -o bin/llama-gui ./cmd/llama-gui

# Build the container image for linux/amd64, the platform Unraid runs. The
# Dockerfile builds the web app and the binary itself, so nothing here
# depends on a prior `just build`.
image version="latest":
    docker build --platform linux/amd64 -t ghcr.io/angelmmiguel/llama-gui:{{version}} -t ghcr.io/angelmmiguel/llama-gui:latest .

# Report files that gofmt would change.
fmt:
    gofmt -l .

# Web

# Install the web app's dependencies exactly as the lockfile pins them.
[working-directory('web')]
web-install:
    pnpm install --frozen-lockfile

# Serve the app on :5173, proxying /api to the Go server.
[working-directory('web')]
web-dev:
    pnpm dev

# Type-check, lint and check the formatting of the web app.
[working-directory('web')]
web-check:
    pnpm check
    pnpm lint

# Run the web test suite.
[working-directory('web')]
web-test:
    pnpm test

# Browser specs against the built app the binary embeds. The browsers come
# from the flake (PLAYWRIGHT_BROWSERS_PATH).
[working-directory('web')]
web-e2e: web-build
    pnpm test:e2e

# Compile the app into web/build, where the Go binary embeds it from. The
# build script recreates build/.gitkeep afterwards: adapter-static wipes the
# directory, and the Go embed needs the placeholder present on a clean
# checkout.
[working-directory('web')]
web-build:
    pnpm build
