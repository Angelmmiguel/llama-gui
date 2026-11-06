# Plan

Source of truth for the look and the feature set: the
[design](https://claude.ai/code/artifact/47b55bef-993c-4ead-9449-a7f06b2862b0), with
artboards Overview, Slots, Requests, Logs, Library, Profiles, Builds, Bench and Nav. The
design's numbers and model names are mock data. Upstream facts below were checked against
llama.cpp's docs and source at `b12132` on 2026-11-05; none of it has been run yet.

## Decisions

1. **Name:** llama-gui for now: binary `llama-gui`, env prefix `LLAMA_GUI_`, image
   `ghcr.io/angelmmiguel/llama-gui`. The design's "llama.town" wordmark becomes llama-gui.
2. **Process management:** upstream's router mode. See [Router mode](#router-mode).
3. **llama.cpp binaries:** prebuilt only, installed on the fly into their own volume
   (`/builds`), so a version change never needs a new image. No source builds. This removes
   "Build & install", the compile options panel and the cmake command from the design.
4. **Prompt capture:** optional behind `LLAMA_GUI_CAPTURE=bodies`, off by default. The panels
   that need it explain how to enable it.
5. **Auth:** none for now. See [Future features](#future-features). The README warns about it,
   and `serve` logs a warning at startup when listening beyond loopback.
6. **Storage:** SQLite in `/data`, through a pure-Go driver (`modernc.org/sqlite`) so the
   binary stays static. It is the project's only Go dependency.

## Router mode

Router mode replaces a model process manager, so we don't write one. Upstream covers:

- Spawning one child per model from a preset, stopping children, and LRU eviction by count
  (`--models-max`) and by GPU memory (`--models-memory-margin`)
- Routing by the `model` field, including `/slots` and `/metrics` via `?model=`
- Load, unload, status and events: `/models/load`, `/models/unload`, `GET /models`, `/models/sse`
- Measured memory per device, split into model, context and compute bytes
- Load at start (`load-on-startup`) and forwarding each child's logs

These design features are not covered, and they are small pieces of our own code on top of the
router, not process management:

| Feature | Gap | Our code |
| --- | --- | --- |
| Unload after idle, per profile | `--sleep-idle-seconds` is not usable: `/slots` is not on the list of endpoints exempt from the idle timer, so our 1 s polling could keep the model awake | Timer from the proxy's last request per model, then `/models/unload` |
| Allow on-demand loading, per profile | Autoload is global or per request | Proxy adds `?autoload=false` for those models |
| Restore models after a build switch | `load-on-startup` only applies at startup | Remember what was loaded, call `/models/load` |
| Router crash | Nothing above the router restarts it | Supervisor restarts it with backoff |
| Child crash | Reported through status and `exit_code`, not restarted | Show it in the UI and events; reloads on next request |
| Logs per model | Lines arrive prefixed only with `[port]` | Map port to model from `/models` |
| Polling safety | `/slots?model=` can autoload an unloaded model | Poll only loaded models, always with `autoload=false` |
| Bench | Needs the GPU free | Unload everything through the router, run `llama-bench`, reload |

Constraints this adds:

- One active build at a time, as the design already assumes.
- Builds older than router mode cannot run. The Builds selector marks them unsupported; the exact
  floor is set in M3. Older builds with a router but no memory reporting show less on VRAM panels.
- The router overrides alias and port. The profile's alias becomes the preset section name.
- With `--fit` on by default, an instance takes the largest context that fits. Profiles always pin `-c`.

M3 starts by running this against a real build. If a gap turns out bigger than the table says, it
gets raised before building on top of it.

## Findings that change the design

1. **Release channels.** `releases/latest` is `v0.9.0`, which has one asset, `nightly-tag.txt`,
   pointing at `b12110`. Binaries live only on `bNNNN` prereleases, tagged on nearly every merge.
   The selector needs two channels: stable (`v*`, resolved to its `b*` tag) and nightly (`b*`).
   Older `b*` tags were published as full releases, so classify by tag name, never by GitHub's
   `prerelease` flag, or old builds would show up as stable.
2. **Prebuilts cover Linux GPUs.** `ubuntu-cuda-12.8` and `-13.4` (x64), `vulkan`, `rocm`, `sycl`,
   `openvino` and CPU all exist, plus arm64 variants. CUDA runtime libraries are a separate
   `cudart-*` tarball (~594 MB compressed for 12.8). The design's "CUDA 12.4" is stale, so options
   must come from the release's asset list.
3. **Base image.** From the ELF headers of `b12132`: the CUDA build needs glibc ≥ 2.38 and
   `GLIBCXX_3.4.32`, plus `libstdc++`, `libgcc_s`, `libgomp` and `libssl3`, and `libcuda.so.1` from
   the host runtime. So `scratch` (mailarchive's base) and Alpine cannot run it, and Debian 12
   (glibc 2.36) is too old. Use `debian:13-slim`. To confirm in M1.
4. **URL collisions.** `/slots`, `/metrics`, `/models` and `/props` are upstream API paths, and `/`
   is upstream's Web UI. So the dashboard lives under `/ui/`, not at the design's implied `/slots`.
5. **`/slots` fields.** It returns `n_ctx`, `is_processing`, `id_task`, `n_prompt_tokens`,
   `n_prompt_tokens_processed`, `n_prompt_tokens_cache` and `next_token.n_decoded`. There is no
   `n_past`, and no prompt without `LLAMA_SERVER_SLOTS_DEBUG`. Context maps can be derived; "Latest
   prompt" must come from proxy capture.
6. **`/metrics` fields.** There is no KV-cache metric, so the KV tile is the sum of slot tokens over
   `n_ctx`. Rates come from counter deltas. TTFT is not there at all; the proxy measures it.
7. **Time split.** `timings` gives `prompt_ms`, `predicted_ms`, `cache_n` and friends. Queue time is
   not reported, so queue = proxy TTFT − `prompt_ms`.
8. **Slot actions.** Upstream has save, restore and erase, but no cancel. Cancel means closing the
   proxied connection, which needs a request-to-slot mapping (spike in M3).
9. **Stale flags in the mock.** `-fa` takes `on|off|auto`, not `0,1`. Flags must be discovered per
   build, not hardcoded.
10. **Logs.** Children support `--log-jsonl` (`time`, `level`, `msg`). The router forwards child lines
    prefixed with `[port]`. Sources (`srv`, `slot`, `ggml`, `router`) must be classified from the
    message text.
11. **`mmproj` memory.** The router's measurements leave it out, so add the file size for vision models.
12. **Not designed but required.** First-run with nothing installed, empty and error states, the ⌘K
    palette and the server-controls menu (Nav shows only the buttons).
13. **Fonts.** The mock loads Geist from Google Fonts. Self-host it: the app must work offline and
    under a strict CSP.

## Milestones

Order keeps a usable product at each step: M3 is already a working managed llama.cpp.

### M0 · Scaffolding

- [x] `go.mod` (Go 1.26), `cmd/llama-gui`, `internal/`
- [x] `flake.nix` dev shell, including Chromium to try the app
- [x] SvelteKit + Svelte 5 + adapter-static in `web/` with `paths.base = /ui`, pnpm, Vitest,
      Playwright (`@playwright/test` pinned to the flake's `playwright-driver`), ESLint, Prettier
- [x] `web/embed.go`: serve the build with SPA fallback, cache rules and CSP header, as in mailarchive
- [x] `.envrc`, `justfile`, `.golangci.yml`, `.dockerignore`, `.gitignore` additions
- [x] `CLAUDE.md` with the conventions, `docs/ARCHITECTURE.md`
- [x] CI: Go race tests, web checks, e2e; publish the image once suites pass

### M1 · Core service and image

- [x] Config: flags and env with the precedence in the README, validation with clear errors
- [x] CLI skeleton: `serve`, `version`, `healthcheck`; other commands call the local API
- [x] HTTP server, graceful shutdown, slog
- [x] SQLite: open with WAL, embedded migrations, pruning by `LLAMA_GUI_RETENTION`
- [x] Route split: `/ui/*`, `/api/*`, everything else proxied; browser `GET /` redirects to `/ui/`
- [x] Multi-stage `Dockerfile`: web, static Go, `debian:13-slim` with runtime libraries, tools,
      `USER 99:100`, `HEALTHCHECK`, volumes `/data`, `/builds`, `/models`
- [x] **Spike:** CPU prebuilt confirmed in `debian:13-slim` with a real model
- [ ] **To test by hand on a GPU machine:** CUDA and Vulkan prebuilts in the image; what ROCm, SYCL and
      OpenVINO need from the host or image; `mesa-vulkan-drivers` for AMD and Intel. Assumed to work for now
- [x] PID 1 duties: reap zombies, forward signals, stop within Docker's timeout

### M2 · Hardware and builds backend

- [x] Detect GPUs: `nvidia-smi` (name, driver, CUDA version, memory, util, temp), `/dev/dri`, CPU
      features, RAM, disk
- [x] Release client: stable and nightly channels classified by tag name (old `b*` tags are full
      releases), pagination, caching, rate-limit handling, optional token
- [x] Choose assets per backend and architecture, including CUDA major from the driver version
- [x] Install on the fly: resumable download, GitHub digest check, safe extraction (no path
      traversal), plus `cudart`, into `/builds/<tag>-<backend>/`, while the current build keeps serving
- [x] Probe each build: version, `--help` flags, devices, router support
- [x] Probe before activation (`--version`, `--help`, `--list-devices`), not yet a generation test; switch, delete, retention (`LLAMA_GUI_KEEP_BUILDS`), never remove
      the active build
- [x] First start: honour `LLAMA_GUI_LLAMA_VERSION` and `LLAMA_GUI_LLAMA_BACKEND`, with `none` skipping
- [x] Update check on a schedule; `notify`, `off` and `auto` policies
- [x] CLI: `llama list|available|install|use|remove`, `doctor` (including that `/builds` allows execution)

### M3 · Supervisor, profiles and proxy

- [x] **Validate router mode first:** confirmed on real `b12132` and `b12110` CPU builds. There is no fixed
      minimum version: activation refuses a build whose `--help` has no `--models-preset`
- [x] Profile store and render to router preset INI, filtered by the build's supported flags
- [x] Start the router on loopback with `--metrics`, JSONL logs and memory margin; restart with
      backoff; crash reporting; drain on stop; restore loaded models after a restart
- [x] Model control: load and unload, `/models?reload=1`, idle unload timers, on-demand policy per
      profile, load at start
- [x] Reverse proxy: streaming (SSE, WebSocket for `/v1/realtime`), no buffering, cancellation
- [x] Measure per request into SQLite: endpoint, model, status, TTFT, end-to-end, inter-token gaps
      (first 120), `timings`, tokens
- [x] Classify errors (`400 ctx`, `503 busy`) and keep the original status
- [x] Optional body capture (`LLAMA_GUI_CAPTURE=bodies`) with size cap and purge
- [x] **Spike:** closing the connection cancels generation (confirmed on a real build); request to slot is
      best effort, oldest in-flight request first
- [x] CLI: `status`, `models load|unload`

### M4 · Telemetry backend

- [x] 1 s sampler for `/slots`, `/metrics`, `/models` per loaded model, plus GPU and RAM; keeps running
      with no browser open
- [x] Ring buffers: 60 s and 90 s series for charts, per-slot state history
- [x] Log pipeline: JSONL and text parsers, port to model, source and level classification, raw
      fallback, stored in SQLite within retention
- [x] Log-derived events (server start, model loaded, unloaded, over-context bursts, queue full, VRAM
      above 90%)
- [x] Request queries: filters, aggregates and percentiles from SQLite
- [x] SSE stream to the UI at 1 Hz with pause; REST for history
- [x] CLI: `logs [-f]`

### M5 · UI foundation

- [x] Tokens from the design: ground `#0A0B0D`, panel `#0F1114`, borders `#1D2026`/`#23272E`, text
      `#E8EAED`/`#8D95A1`; decode amber `#F0B35A`, prefill `#6CC4F2`, cached `#9D8CF0`, ok `#5EE0B5`,
      warn `#FFB38A`, error `#FF6B6B`/`#FF9F96`
- [x] Self-hosted Geist and Geist Mono
- [x] API client mirroring the Go routes; live state in `.svelte.ts` modules using runes
- [x] Components: band and panel, section heading, stat tile with sparkline, legend, segmented control,
      switch, chip, stepper, status dot, stacked bar, command block with Copy, live toggle, buttons
- [x] Motion from the design (pulse, blink, flow, breathe, rise), off under `prefers-reduced-motion`
- [x] Layouts wrap at phone width, wide tables scroll in their own box; accessibility as drawn (real
      buttons, `aria-*`, 44 px targets)
- [x] Nav: logo, three link groups, update dot on Builds, search button, server status button with uptime

### M6 · Monitoring pages

**Overview**

- [x] Header: build, backend, GPUs, models loaded; live toggle
- [x] Decode throughput: current, avg, peak, 60 s chart
- [x] Tiles with sparklines: Prefill, TTFT p50 and p95, Requests per minute and deferred, KV cache
- [x] Slots and context windows: stacked cached, prompt and generated bars with state and speed
- [x] VRAM residency: per-GPU cells for weights, KV, compute and free by model; System RAM, tensor split, temps
- [x] Request waterfall: queue, prefill and decode over 60 s, live stripes, link to Requests
- [x] Router models: quant, meta, slot pips, load and unload; link to Library
- [x] Launch profile summary with Edit link; server log tail with link to Logs

**Slots**

- [x] Header summary; Save all, Erase idle, live toggle
- [x] Activity heatmap: lane per slot, 90 cells, brightness by decode speed, busy %
- [x] Context map cards: grid of cells, state, `n_past` equivalent, speed, selection
- [x] Inspector: stats, sampling params, latest prompt (capture on), task id
- [x] Actions: Save KV, Restore, Erase, Cancel; disabled with a hint when `--slot-save-path` is missing

**Requests**

- [x] Filters: endpoint, model, range (1 h, 24 h, 7 d)
- [x] KPI strip: requests, success, TTFT, end-to-end, tokens, prompt cache hit
- [x] Prompt size vs TTFT scatter, cached vs cold, selected marker
- [x] Where the time goes per model (median); status code counts
- [x] Recent list with selection; Export CSV
- [x] Detail: token stream chart with prefill-collision spikes, timings grid, messages (capture on), Copy as cURL
- [ ] Replay in Web UI (capture on): the button is there, disabled. **Spike:** serve upstream's UI
      (`--api-prefix` or a second port) and prefill it

**Logs**

- [x] Volume chart by level over 60 min
- [x] Viewer: line numbers, timestamp, level, source, follow, filter text, level and source toggles, empty state
- [x] Events timeline; warnings and errors today; Download `.log`; verbosity shown

### M7 · Library

- [x] Local scan of `/models`: GGUF header parser (architecture, params, layers, embedding sizes, quant,
      `expert_count` for MoE), mmproj and shard grouping in the router's layout
- [x] Hugging Face search and repo lookup (paste `user/repo`), token support, file sizes per quant
- [x] Read remote GGUF metadata with HTTP range requests
- [x] Fit estimator: weights + KV at context and cache type + compute + headroom; verdicts Fits on GPU,
      Partial offload, Offload experts, Too large. Calibrate against the router's `memory` measurements
- [x] Download queue: resume, speed, ETA, cancel, atomic rename, `/models?reload=1`
- [x] UI: source tabs, search, fit controls (context, KV type), filters, result rows with fit bars
- [x] Expanded row: description, variants with memory bar and verdict, Download, Load and Unload
- [x] Suggestions: largest dense that fits, fast at long context, most capable with offload. Needs an
      embedded starter catalog, since no API provides this
- [x] Downloads panel, disk panel, how-to-read-verdicts panel; delete local model
- [x] CLI: `models list|pull|rm`

### M8 · Profiles and Builds pages

**Profiles**

- [x] List with status and + New profile
- [x] Form: model file, alias, `--jinja`, `--mlock`; context and slots with per-slot result and notes;
      GPU layers, split mode, tensor split, flash attention; KV types and `--cache-reuse` with warnings;
      batching, threads and sampling defaults; router behaviour switches
- [x] Hide or flag options the active build does not support; free-form extra args
- [x] Memory impact, live: VRAM and RAM bars including other loaded models, verdict
- [x] Resulting command with Copy; Import from command line parser
- [x] Dirty tracking, Save & reload model, Revert

**Builds** (adapted: prebuilts only)

- [x] Version selector across stable and nightly, including any older supported tag
- [x] Hero: running vs target, age, commits ahead; one install action in place of the design's two
- [x] Update steps with live progress: download, verify, smoke test, swap and restart; "up to date" state
- [x] What you're missing: timeline with stable releases as anchors and nightlies summarised, "affects you"
      from your backend, architectures and server keywords via compare API; notes panel
- [x] Installed builds: active badge, size, Switch, Delete, rollback hint
- [x] Backend picker in place of compile options: hardware detected, assets available for the selected
      version, unavailable ones disabled with the reason

### M9 · Bench and the undesigned parts

**Bench**

- [x] Run setup: model, flash attention, KV type, depth, repetitions; generated `llama-bench` command;
      test count and time estimate
- [x] Runner: unload models through the router, run, reload; proxy answers `503` with `Retry-After`
      meanwhile; progress bar; cancel
- [x] Parse `-o json`, tolerating unknown fields; store runs in SQLite with build id
- [x] Decode vs depth chart, prompt-processing bars, computed takeaway (only claims the data supports)
- [x] Results table with relative bars; Copy as Markdown
- [x] Across builds history with deltas; offer a re-run after each update

**Undesigned screens** (match the design's language)

- [x] First-run: detected hardware, build installing, no models yet, link to Library
- [x] Empty and error states on every page, server-down banner, CPU-only variants of GPU panels
- [x] Server controls menu: start, stop, restart, active build
- [x] ⌘K palette: pages, models, load and unload, restart

### M10 · Packaging

- [ ] `unraid/llama-gui.xml` and icon, with all variables, three paths, port and GPU notes
- [ ] README check: follow the deployment section on a real Unraid box, for NVIDIA and CPU
- [ ] Optional: multi-arch image (arm64 prebuilts exist)

### Quality gates for every milestone

- [x] `just test`, `just lint` and `just fmt` clean; `just web-e2e` for screen changes
- [x] A fake `llama-server` test binary replays recorded `/slots`, `/metrics`, `/models` and logs, so tests need no GPU
- [ ] Parser fixtures from at least the oldest supported and the newest build (only `b12132` so far)
- [x] Model dock at the top of the Overview, with its Load model drawer and empty panel
- [x] Library shows download and load progress where you started it, and a skeleton while suggestions are slow
- [x] Playground view for quick chats, with the timings of each reply
- [x] Speculative decoding in profiles: MTP, draft models and n-gram types
- [x] Library tells apart files with the same name from different repos, by size, and keeps them apart on disk
- [x] Live speculation in the dock and Slots: its type, and the share of drafts accepted
- [x] Draft tokens per request, in Requests, its export and the Playground
- [x] Compare two profiles on the Bench page, as llama-bench cannot run speculative decoding
- [x] Blurred placeholder behind "Load a model first" on a fresh Overview
- [x] Tests and the demo no longer use GitHub's rate limit
- [x] Say when GitHub's rate limit stops the first install, and when it tries again
- [x] Library split into My machine and Explore, with a fit filter and collapsed rows

### Found testing on a real GPU (RTX 3060, driver 615)

- [x] Read the CUDA version from `nvidia-smi -q -x`, as newer drivers relabelled the summary table
- [x] Query `kmd_version`, falling back to `driver_version` on older drivers
- [x] `doctor` warns when a GPU build sees no GPU
- [x] Keep weights in RAM through `--load-mode` on builds without `--mlock`
- [x] Cap a new profile's context at what the model supports
- [x] Page through Library search results
- [x] Requests keeps its place when a filter changes
- [x] Range filter on the Overview charts, from a stored history

## Future features

Not part of the milestones above.

- [ ] Authentication: dashboard login and API keys for the proxy, with upstream admin routes
      (`/models/*`, `POST /slots/*`, `POST /props`) closed to inference keys
- [ ] SSO through self-hosted identity providers such as Authelia and PocketID
- [ ] HTTPS-aware settings that come with login: Secure cookies and trusted proxies
