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

## Browser access

There is no authentication, so nothing may let another site act through a visitor's browser.
Clients that send neither `Sec-Fetch-Site` nor `Origin`, such as curl and SDKs, are unaffected by the
origin rules.

- **Host.** `routes()` answers every path with a plain-text 403 unless the `Host` name, without port and
  ignoring case, can't be rebound by a public site: an IP literal, a single label (`localhost`, `tower`), or a name
  under `.local`, `.lan`, `.home`, `.internal`, `.intranet`, `.corp`, `.localdomain` or `.home.arpa`. Anything else
  has to be in `LLAMA_GUI_ALLOWED_HOSTS`, where `*` turns the check off.
- **Cross-site** (`proxy.CrossSite`) is any `Sec-Fetch-Site` other than `same-origin` or `none`. Without that header,
  which browsers omit on plain HTTP to a LAN host, it is an `Origin` whose host differs from `Host`. A reverse proxy
  must therefore forward `Host` unchanged.
- **API.** Cross-site writes get a 403.
- **Proxy.** Upstream's `Access-Control-*` headers are always stripped, since it allows every origin. A cross-site
  request other than GET or HEAD gets a 403 in upstream's error shape, unless its `Origin` is in
  `LLAMA_GUI_ALLOWED_ORIGINS` (or that is `*`) and the path is an inference route, the set the proxy measures. So
  `/models*`, `/slots*`, `/props`, `/cors-proxy`, `/tools` and `/lora-adapters` are never reachable cross-site, even
  with `*`. WebSocket handshakes count as writes. For an allowed origin on an inference route the proxy answers
  `OPTIONS` itself and sets `Access-Control-Allow-Origin` to the exact origin with `Vary: Origin`. Other cross-site
  preflights get a 403, and cross-site reads get no CORS headers, so the page can't read them.

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
| `internal/library` | GGUF parsing, scan of `/models`, Hugging Face client, fit estimates, starter catalog, download queue |
| `internal/hardware` | GPU, CPU, RAM and disk detection |
| `internal/logs` | Parsing router output into structured lines and events, and the pipeline storing them |
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

- **Queue time** = proxy TTFT − `prompt_ms`; for a non-streamed answer, total − `prompt_ms` − `predicted_ms`. **Prefill** = `prompt_ms`. **Decode** = `predicted_ms`.
- **Throughput** = delta of `tokens_predicted_total` (or `prompt_tokens_total`) plus the delta of `n_decoded` (or `n_prompt_tokens_processed`) on running slots: the counters only move when a task ends.
- **KV tile** = sum of slot `n_prompt_tokens` over sum of `n_ctx`.

- **Request to slot** is best effort: when a new `id_task` appears on a model's slot, it is assigned to the
  oldest unassigned in-flight request for that model. Slot cancel closes that request's connection.
- **TTFT** runs from the moment the body has been read to the first non-empty SSE delta, or to the first body
  byte when not streaming. `requests.error` holds the classification (`400 ctx`, `503 busy`, `200 cancelled`);
  a plain success leaves it empty, and a request that ended before upstream answered has status 499.

The proxy decides on-demand loading per request: the router runs with `--no-models-autoload`, the proxy adds
`autoload=true` when the profile allows it, and removes any `autoload` the client sent.

## Storage

- `/data/llama-gui.db`: SQLite through `modernc.org/sqlite` (pure Go), WAL, one writer connection.
  Tables: `kv`, `profiles`, `requests`, `request_bodies`, `log_lines`, `events`. Retention prunes by age.
- `/builds/<tag>-<backend>/`, where `<tag>` is the `b*` tag holding the binaries: extracted release plus
  `manifest.json`, written last so a partial install
  never looks installed. The active build's id is in `kv`, and losing `/builds` only means re-downloading.
- `/models`: GGUF files in the router's layout (a file, or a directory for multimodal and shards).
- `/data/preset.ini`: rendered from `profiles`, atomically, on every change. `/data/slots/`: slot saves.
- Speculative decoding: `--spec-type` is per model and can't be changed per request. `speculative.types` and `speculative.n_max` in a request had no effect on `b12132`, and `llama-bench` has no such flags, so comparing means loading the model twice. A reply's `timings` has `draft_n` and `draft_n_accepted`, `/metrics` has `spec_decode_*`, and a slot has `speculative` and `params["speculative.types"]`.
- The starter catalog's repos are answered from a snapshot kept in `kv`, so the Library opens instantly and survives a restart. It refreshes in the background once a day, and a failed refresh keeps the old answers. Other repos go to Hugging Face, with a short in-memory cache.
- `/library/repo` and `/library/suggestions` match local files by name and size in `local_path`, as repos reuse file names for different builds. A download whose name is taken by a different file goes to `<stem>--<user>-<repo>/`.
- Profile extra arguments can never name a file or a remote source, or turn on what reaches beyond the model (`--tools`, `--agent`, MCP, RPC, `--connect`). The router expands short names in a preset (`md` is `--model-draft`), so every alias is reserved; a test checks this against the captured `--help`. `LLAMA_GUI_LLAMA_ARGS` is the operator's own and is not limited.

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

JSON under `/api/v1`; errors are `{"error": "message"}` with a matching status. Hugging Face failures keep their
meaning: 404, 401 when a token may be needed, 429 with `Retry-After` when known, 504 on a timeout and otherwise 502.

| Route | Purpose |
| --- | --- |
| `GET /status` | `install_issue` while a first install waits to retry; `state`, `version`, `uptime_seconds`, `capture`, `log_verbosity`, `router_since`, `pid`, `restart_count`, `last_error`, `active_build` `{id, tag, backend}` or null, `models_loaded`, `update_available`, `update` `{available, latest, stable, nightly, checked_at, error}` |
| `GET /hardware` | Detected GPUs, CPU, RAM, disks |
| `GET /builds` | `{builds, active}`; each is a manifest plus `active`, `supported` and `size_bytes` on disk |
| `GET /builds/available?channel=&limit=&version=` | `{releases}`, newest first. `options` is only on each channel's newest and on an exact `version`, as it costs a download per stable release |
| `GET /builds/compare?from=&to=` | `{ahead_by, commits}` between two tags |
| `POST /builds/check` | Looks upstream now and answers like `update` in `/status`. Never installs, whatever the policy |
| `POST /builds/install` `{version, backend}` | 202 with the job; 200 if already installed. `backend` defaults to `auto` |
| `GET /builds/jobs` · `DELETE /builds/jobs/{id}` | `{jobs}` with progress; cancel |
| `POST /builds/{id}/activate` · `DELETE /builds/{id}` | Switch, answering 202 while the router restarts; remove |
| `GET,POST /profiles` · `GET,PUT,DELETE /profiles/{id}` | `{profiles, skipped}`; bad input is a 400 with the message, a duplicate alias a 409. Changes reach a running router through `/models?reload=1` |
| `GET /profiles/{id}/command` | `{command}`, the equivalent command line |
| `POST /profiles/preview` | Unsaved values as `{command, error}`: an invalid profile still renders, with the reason |
| `POST /profiles/estimate` | Memory for unsaved values: `source` (`measured`, `scaled` from a measurement, `estimate`), `rough`, `layers`, `router_up`, `gpu` (null on CPU) and `ram` as `{capacity, other, weights, kv, compute}`, `verdict` (`fits`, `tight`, `over`), `verdict_on`, `free_bytes`. 404 when the file is not in the models directory |
| `GET /models` · `POST /models/{id}/load` · `POST /models/{id}/unload` | `{models}`, empty while the router is down; 202 |
| `GET /library/local?ctx=&kv=&parallel=` · `DELETE /library/local?path=` | `{models, hardware}`: the scan with `profile`, `router_id`, `status` and `fit`, measured once loaded. `ctx` is the total, `-c`. Delete answers 409 while the model is loaded |
| `GET /library/search?q=&filter=&cursor=&ctx=&kv=` | `{source, results, next_cursor, hardware}`. Source is `catalog` with no `q`, `repo` for a `user/repo` query, and `search` otherwise, which has no variants and pages with `cursor`. `filter` is chat, vision, embedding, code or moe |
| `GET /library/repo?repo=&ctx=&kv=` | `{repo, hardware}`: `variants` with `fit` and `download_files` (the mmproj included), `best`, `metadata` read with range requests and cached, or `metadata_error` with estimated fits |
| `GET /library/repo/mtp?repo=` | `{variants}`: for each variant `heads`, `companion`, `none` or `unknown`, from the start of its file header. Names can't be trusted, and asked only for a row someone opened, as this reads every variant |
| `GET /library/suggestions?ctx=&kv=` | `{suggestions, hardware}` from the starter catalog |
| `GET,POST /library/downloads` · `DELETE /library/downloads/{id}?remove_partials=` | `{jobs}` with progress; `{repo, files}` answers 202 with the job, 409 if already on disk or queued, 507 without the space; cancel, keeping partials to resume unless told otherwise |
| `GET /library/disk` | `{path, gguf_bytes, free_bytes, total_bytes}` |
| `GET /slots` · `POST /slots/{model}/{slot}/{action}` | `{models, in_flight, time}`; action is save or restore with `{filename}`, erase or cancel. A filename is a single path element |
| `GET /requests?since=&until=&model=&endpoint=&status=&limit=&cursor=&format=csv` | `{requests, next_cursor}`, or CSV. `status` is a class such as `2xx`; `endpoint` takes a comma-separated list |
| `GET /requests/summary` | Same filters, default last hour: `kpis`, `ttft` and `total` percentiles, `statuses`, per-model `models` medians, `scatter` (newest 500), `truncated` |
| `GET /requests/{id}` | The request plus `gaps`, and `body` `{request, response}` or null |
| `GET /logs?since=&until=&level=&source=&model=&q=&limit=&cursor=` | `{lines, next_cursor}`, newest first; `level` and `source` take comma-separated lists |
| `GET /logs/stream?level=&source=&model=&q=&backlog=` | SSE: backlog then live, `event: log` and `event: event` |
| `GET /logs/volume` | `{minutes, today}`: 60 per-minute counts by level, and warnings and errors since local midnight |
| `GET /logs/download` | Same filters as `/logs`, as text, oldest first |
| `GET /events?since=` | `{events}`, newest first |
| `GET /telemetry` · `GET /telemetry/stream` | Current snapshot, and SSE at 1 Hz |
| `GET /telemetry/history?range=` | `{range, step_seconds, until, points}` for `5m`, `15m`, `1h`, `6h` or `24h`: a point per finished period, oldest first, zero where nothing was sampled. Stored every 10 s, kept for the retention period |
| `GET /bench/runs?model=&limit=` | `{runs}`, newest first |
| `POST /bench/runs` | Starts a run from a setup, `{model_file, flash_attn, cache_types, depths, repetitions, n_prompt, n_gen, ngl}`: a 202 with the run. Bad input is a 400, a missing model file a 404, and a run already going, no build or a build whose `llama-bench` lacks a flag a 409 |
| `GET /bench/runs/{id}` | The run with `series` `{decode, prefill}`, `markdown` and, once it has ended and there is something to compare, `takeaway` |
| `GET /bench/current` · `DELETE /bench/current` | `{run}`, as above: the running one or the last to finish this process, null before any. Delete cancels it, or answers 409 |
| `POST /bench/estimate` | `{tests, seconds, command}` for a setup, without running anything |
| `GET /bench/history?model=` | `{history}`, the best `tg128` at depth 0 per build, oldest first: `build`, `run_id`, `started_at`, `tokens_per_sec`, `delta_percent` |
| `POST /server/{start,stop,restart}` | Router control. Start and restart answer 202 while it happens, or 409 with no build or during a benchmark |
| `GET /healthz` | Liveness, used by `llama-gui healthcheck` |

`web/src/lib/api` mirrors these routes one to one. Cross-site state-changing requests get a 403, as
[Browser access](#browser-access) describes.

A telemetry snapshot has `time`, `server` `{state, error}`, `models[]` `{id, status, memory, slots, error}`,
`throughput` `{decode, prefill}`, `requests` `{in_flight, processing, deferred, per_minute, ttft_p50_ms, ttft_p95_ms}`,
`kv` `{used, total}`, `gpus`, `ram`, `in_flight`, `series` `{decode, prefill, spark: {prefill, ttft, rpm, kv}}` (60 s and
30 s, oldest first) and `slot_history[]` `{model, slot, samples: [{state, speed}]}` (90 s). A slot is `{id, state, cached,
prompt, generated, total, n_ctx, task, speed, params}`, with state `idle`, `prefill` or `decode`.

## Telemetry and logs

The sampler ticks at 1 Hz whether or not anyone is watching, polling `/slots` and `/metrics` only for loaded models. A
failure is per model: that model keeps its last slots and carries an `error`. Requests per minute and TTFT percentiles
come from the proxy's measurements as they finish, in memory, over 60 s and 5 min; history comes from SQLite.

Router output goes parser, bounded queue, batched `log_lines` writes (500 ms or 200 lines, dropping with a warning when
full), then the live tail. A batch's insert and publish share a lock with a tail's backlog query, so a line reaches a
reader exactly once. A line's model comes from the port in the last spawn line and then the sampler's `/models`. Lines
are stored as they are, but never copied into our own logs, since a high `-lv` can include prompts. Events come from
`logs.Detector`, the supervisor's state changes and installs.

Updates are checked at start and every 6 h: `notify` reports them in `/status`, comparing `b*` numbers with the active
build's channel, and `auto` also installs a newer stable onto a stable build and switches once nothing is in flight.
