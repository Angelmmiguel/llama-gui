# Architecture

llama-gui is one Go binary with a SvelteKit SPA embedded. It installs prebuilt
llama.cpp releases, runs `llama-server` in router mode on loopback, and is the only
entrypoint: it serves the dashboard, a management API, and a measured reverse proxy.

## Routes

| Path | Handler |
| --- | --- |
| `/ui/*` | Embedded SPA (`web`), with `/ui` stripped before the embed handler; `/ui` redirects to `/ui/` |
| `/api/v1/*` | Management API (`internal/api`) |
| `GET /` with `Accept: text/html` | Redirect to `/ui/` |
| everything else | Reverse proxy to the router (`internal/proxy`) |

`/slots`, `/metrics`, `/models`, `/props` and `/` are upstream paths, which is why the
dashboard has its own prefix. Upstream has no `/api` routes.

## Packages

Dependencies only point downward. Nothing imports `internal/app` except `cmd/llama-gui`;
its `reaper` subpackage has no dependencies and is for anything that spawns a process.

| Package | Owns |
| --- | --- |
| `cmd/llama-gui` | Subcommands; the non-`serve` ones are thin clients of the local API |
| `cmd/fake-llama-server` | Test double replaying `internal/llama/testdata`, used instead of a real build |
| `internal/app` | Composition root: builds every component, wires them, runs and stops them |
| `internal/app/reaper` | PID 1 orphan reaping, and the `Start`/`Wait` every spawned process must use so it is not stolen |
| `internal/api` | `/api/v1` JSON handlers and the SSE stream |
| `internal/proxy` | Reverse proxy, per-request measurement, optional body capture |
| `internal/telemetry` | 1 s sampler, ring buffers, publish/subscribe hub |
| `internal/supervisor` | Runs the router process: start, stop, restart with backoff, idle unload |
| `internal/profiles` | Profile model, validation, rendering to the router's preset INI |
| `internal/builds` | Installed builds on `/builds`: install, probe, activate, remove, retention |
| `internal/releases` | GitHub client: channels, tag classification, assets, compare |
| `internal/hardware` | GPU, CPU, RAM and disk detection |
| `internal/logs` | Parsing router output into structured lines and events |
| `internal/llama` | Typed, tolerant HTTP client for the router's API |
| `internal/store` | SQLite: migrations, retention, every query |
| `internal/config` | Flags and env |
| `web` | The embedded SPA and its handler |

## Upstream behaviour

Captured from a real `b12132` CPU build in `debian:13-slim`; the raw responses are in
`internal/llama/testdata`. Treat that directory as the reference for field shapes.

- **Parse tolerantly, always.** Any build can run underneath: unknown fields are ignored,
  missing fields become zero values, and an unparseable log line is kept as raw text.
- **Releases.** `v*` is stable and has one asset, `nightly-tag.txt`, holding its `b*` tag.
  Binaries live on `b*` tags. Classify by tag name, never by GitHub's `prerelease` flag,
  because old `b*` tags were full releases. Older `master-*` tags are ignored. The API's order is not
  publication order, a stable tag is found every page or two at `per_page=100`, and `digest`
  (`sha256:<hex>`) is null on old assets.
- **Assets.** `llama-<tag>-bin-ubuntu-{x64,arm64,vulkan-x64,vulkan-arm64,cuda-12.8-x64,cuda-13.4-x64,
  cuda-13.4-arm64,rocm-10.0-x64,sycl-fp16-x64,sycl-fp32-x64,openvino-<ver>-x64}.tar.gz`, extracting
  to a `llama-<tag>/` directory. CUDA also needs `cudart-llama-<tag>-bin-ubuntu-cuda-<ver>-<arch>.tar.gz`,
  whose top directory is the asset's name rather than `llama-<tag>/`, extracted next to the binaries
  (rpath is `$ORIGIN`). Match with patterns; the versions change.
- **Probe.** `llama-server --version` prints `version: 0.9.0-dev (build 12132, commit 2ada83079)`.
  `--list-devices` and `--help` have no stable schema, so parse them loosely.
- **Starting.** Without a model argument `llama-server` is the router. Flags used:
  `--host 127.0.0.1 --port N --models-preset F --models-max N --models-memory-margin MiB
  --no-models-autoload --metrics --slot-save-path D --log-jsonl -lv N`. Children inherit them.
- **`GET /models`.** `data[].status.value` is `unloaded`, `loading`, `loaded`, `sleeping` or
  `failed`-like with `exit_code`. `status.args` holds the child's `--port`, which maps log lines to
  models. `memory.{min,max,effective}.devices[]` has `kind`, `name`, `model`, `context` and `compute`
  bytes, including a `Host` device on CPU; `mmproj` is not included.
- **`POST /models/load` and `/models/unload`.** Body `{"model": id}`.
- **`GET /slots?model=ID` and `GET /metrics?model=ID`.** Always add `autoload=false`, and poll only
  loaded models: `/slots` is not exempt from the idle timer, so never use `--sleep-idle-seconds`.
- **`POST /slots/N?action=save|restore|erase`.** The model goes in the JSON body, not the query.
- **Slot fields.** `n_ctx` is per slot. While processing, `n_prompt_tokens` is the total in the slot
  and grows as tokens are generated; `n_prompt_tokens_cache` + `n_prompt_tokens_processed` +
  `next_token[0].n_decoded` split it into cached, prefilled and generated. Once idle, the two prompt
  counters reset to 0 and `n_prompt_tokens` is what remains cached. `id_task` stays set after finishing.
- **Metrics.** Prometheus text, counters and gauges under `llamacpp:`. No KV metric and no TTFT.
- **Responses.** The last SSE chunk (or the JSON body) carries `timings`: `cache_n`, `prompt_n`,
  `prompt_ms`, `predicted_n`, `predicted_ms`, `predicted_per_second`. `usage` is non-stream or opt-in.
- **Cancellation.** Closing the proxied connection stops generation (confirmed by test).
- **Logs.** With `--log-jsonl`, a router line is `{"type":"log","time":<µs since start>,"level":"info",
  "msg":"srv  name: text\n"}`. A forwarded child line has `level:"none"` and `msg` = `[<port>] <child
  JSON line>\n`, so it is parsed twice. `[    0]` is a short-lived memory measurement child. Other `type`
  values exist (`fit_memory_breakdown`). The message starts with a source tag (`srv`, `slot`, `cmn`) or
  a function name (`load_tensors:`). `time` is relative, so stamp lines on arrival.

## Metric derivations

- **Queue time** = proxy TTFT − `prompt_ms`. **Prefill** = `prompt_ms`. **Decode** = `predicted_ms`.
- **KV tile** = sum of slot `n_prompt_tokens` over sum of `n_ctx`.
- **Rates** come from counter deltas between samples.
- **Request to slot** is best effort: when a new `id_task` appears on a model's slot, it is assigned to the
  oldest unassigned in-flight request for that model. Slot cancel closes that request's connection.

## Storage

- `/data/llama-gui.db`: SQLite through `modernc.org/sqlite` (pure Go), WAL, one writer connection.
  Tables: `kv`, `profiles`, `requests`, `request_bodies`, `log_lines`, `events`. Retention prunes by age.
- `/builds/<tag>-<backend>/`, where `<tag>` is the `b*` tag holding the binaries: extracted release plus
  `manifest.json`, written last so a partial install
  never looks installed. The active build's id is in `kv`, and losing `/builds` only means re-downloading.
- `/models`: GGUF files in the router's layout (a file, or a directory for multimodal and shards).
- `/data/preset.ini`: rendered from `profiles`, atomically, on every change. `/data/slots/`: slot saves.

## Lifecycle

`app.Run`: open the store, detect hardware, resolve the active build, install on first start
(`LLAMA_GUI_LLAMA_VERSION`, `none` skips, never blocks the HTTP server), render the preset, start the
supervisor, sampler and log pipeline, serve HTTP. A build switch stops the router, restarts it from the new
build, and reloads what was loaded. Shutdown stops accepting requests, drains, stops the router, closes the store.

The router runs in its own process group on a loopback port picked per start. Stopping sends SIGTERM to the
group, then SIGKILL; a crash also kills the group, since model children outlive the router and hold the GPU.
Crashes restart with backoff (1 s doubling to 60 s, reset by a 60 s run) and reload what the last `/models` poll
saw. A missing build leaves it stopped with the error, not looping; a deliberate stop forgets what was loaded.

As PID 1 the binary reaps orphans and forwards SIGTERM to the router's process group.
Reaping peeks with `waitid(WNOWAIT)` and skips children started through `reaper.Start`, so
`exec.Cmd.Wait` keeps working; nothing in the process may spawn any other way.

## API

JSON under `/api/v1`; errors are `{"error": "message"}` with a matching status.

| Route | Purpose |
| --- | --- |
| `GET /status` | Server state, uptime, version, active build, capture mode |
| `GET /hardware` | Detected GPUs, CPU, RAM, disks |
| `GET /builds` · `GET /builds/available?channel=` | Installed and released versions |
| `POST /builds/install` `{version, backend}` · `GET /builds/jobs` | Install, with progress |
| `POST /builds/{id}/activate` · `DELETE /builds/{id}` | Switch, remove |
| `GET,POST /profiles` · `GET,PUT,DELETE /profiles/{id}` | Profiles |
| `GET /models` · `POST /models/{id}/load` · `POST /models/{id}/unload` | Router models |
| `GET /slots` · `POST /slots/{model}/{slot}/{action}` | Slots; action is save, restore, erase or cancel |
| `GET /requests` · `GET /requests/{id}` · `GET /requests/summary` | History with filters |
| `GET /logs` · `GET /logs/stream` · `GET /events` | Logs and derived events |
| `GET /telemetry` · `GET /telemetry/stream` | Current snapshot, and SSE at 1 Hz |
| `POST /server/{start,stop,restart}` | Router control |
| `GET /healthz` | Liveness, used by `llama-gui healthcheck` |

`web/src/lib/api` mirrors these routes one to one.
