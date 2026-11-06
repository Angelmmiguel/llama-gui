# llama-gui

Self-hosted dashboard and control plane for llama.cpp: one Go binary with the web app
embedded, fronting `llama-server` as the only entrypoint.

[PLAN.md](PLAN.md) is the TODO list. [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) is the design
reference: packages, routes, storage and the upstream behaviour we rely on. Update it in the same
change when you alter any of those. The design lives at
https://claude.ai/code/artifact/47b55bef-993c-4ead-9449-a7f06b2862b0.

## Stack

- **Backend:** Go, standard library only, except the pure-Go SQLite driver
  (`modernc.org/sqlite`). `net/http`'s mux, `log/slog`, `os/exec`, `archive/tar`. CGO stays off so the
  binary is static.
- **Frontend:** SvelteKit SPA in `web/` under base path `/ui`, compiled by adapter-static into
  `web/build`, which the Go binary embeds. Svelte 5 only: runes (`$state`, `$derived`, `$effect`,
  `$props`) everywhere, never `export let`, `$:` or `svelte/store`. Shared reactive state lives in
  `.svelte.ts` modules under `src/lib/state`. The API client in `src/lib/api` mirrors the Go routes.
  Visual language comes from `src/lib/styles/tokens.css` and the components in `src/lib/components`;
  routes only compose them. The CSP is authored in `svelte.config.js` and delivered by the Go handler.
- **Toolchain:** Nix flake. Run commands inside `nix develop` (or direnv).

## Commands

```
just test     # go test -race ./... and vitest
just lint     # go vet, golangci-lint, svelte-check, eslint, prettier
just dev      # Go server on :8080 and Vite on :5173 together
just build    # pnpm build, then go build with the web app embedded
just web-e2e  # Playwright specs against the built app
```

`just test` and `just lint` must be clean before work is considered done, along with `just fmt`
printing nothing. Web-only variants exist as `web-install`, `web-dev`, `web-check`, `web-test` and
`web-build`.

## Conventions

- Upstream can be any version: parse tolerantly, and never crash on or hide unknown output.
  `internal/llama/testdata` holds real captures, and tests use `cmd/fake-llama-server`, never a GPU.
- Security: tags, file names and paths from the network or a request are validated before touching
  the filesystem. Archive extraction rejects traversal and links out of the target. Never log prompts
  or tokens. Writes are atomic: temp file, then rename.
- Concise, idiomatic Go. Doc comments on exported identifiers; other comments only where the *why* is
  non-obvious. No narration comments, no abstractions with a single caller, no utils packages.
- Sentinel errors live in the package that owns the concept; wrap with `%w`.
- Every route and every store operation has tests. Inject time, commands and HTTP where a test needs it.
- Commit and push to `main`, only when asked. One change per commit.
