# llama-gui

A self-hosted dashboard and control plane for [llama.cpp](https://github.com/ggml-org/llama.cpp).

llama-gui is one Go binary with the web app embedded. It downloads and updates
llama.cpp, manages your models and launch profiles, and sits in front of
`llama-server` as the single entrypoint, so it can measure every request and
read every log line. Deploy it as a container, point your clients at it, and
never touch a `llama-server` command line again.

> **Status:** early. Every page works and is tested against a real CPU build of llama.cpp. GPU backends,
> the Unraid template and replay in the Web UI are still to come; [PLAN.md](PLAN.md) tracks the work.

## Features

| Page | What it does |
| --- | --- |
| **Overview** | Live throughput, TTFT, requests and context use, slot occupancy, VRAM residency per GPU, request waterfall, loaded models, log tail |
| **Slots** | 90-second activity heatmap, per-slot context maps, slot inspector, save, restore, erase and cancel |
| **Requests** | Every call with llama.cpp's timings: KPIs, prompt size vs TTFT, where the time goes, token stream, CSV and cURL export |
| **Logs** | Tail parsed by source and level, filters, volume chart, event timeline, download |
| **Compare** | Run one prompt through two profiles on the Bench page, the way to measure speculative decoding |
| **Playground** | Quick chat with a loaded model: request settings, streaming, and each reply's timings, cache use and raw response |
| **Library** | Local GGUFs and Hugging Face search, checked against your hardware at your context size, with suggestions and a download queue |
| **Profiles** | Per-model launch settings with live memory impact and the resulting command |
| **Builds** | Pick the llama.cpp version and backend, see what changed, update, roll back |
| **Bench** | `llama-bench` matrix runs and a history across builds to catch regressions |

## How it works

```
 API clients, browser
         │  :8080
 ┌───────▼───────────────────────────────────────────────┐
 │ llama-gui                                             │
 │   /ui/*   dashboard (SvelteKit, embedded)             │
 │   /api/*  management API                              │
 │   /*      measured reverse proxy to llama-server      │
 └───────┬───────────────────────────────────────────────┘
         │  loopback only, spawned and supervised
 ┌───────▼───────────────────────────────────────────────┐
 │ llama-server, router mode (--models-preset)           │
 │   └─ one child process per loaded model               │
 └───────────────────────────────────────────────────────┘
```

Clients use the same OpenAI and Anthropic compatible paths as `llama-server`,
just on llama-gui's port. The proxy records timings, status and tokens. A sampler
polls `/slots`, `/metrics` and `/models`. The supervisor owns the process and
parses its output. Your profiles are rendered into a router preset, and history
is kept in a SQLite database.

### llama.cpp is not in the image

Releases of llama-gui are independent of llama.cpp versions. The image ships no
llama.cpp at all. Prebuilt binaries are downloaded on the fly from llama.cpp's
GitHub releases into their own volume, `/builds`, and you choose which one runs
from the Builds page, the CLI or an env variable. You can install several, switch
between them and roll back, all without updating the llama-gui image.

Because any version can run underneath, llama-gui never assumes a fixed flag set
or log format. It probes each build's capabilities, hides what a build does not
support, and shows anything it cannot parse as raw text rather than failing.

## Deployment

```bash
mkdir -p data builds models && chown 99:100 data builds models
docker run -d --name llama-gui --gpus all \
  -p 8080:8080 \
  -v "$PWD/data:/data" -v "$PWD/builds:/builds" -v "$PWD/models:/models" \
  ghcr.io/angelmmiguel/llama-gui
```

Open http://localhost:8080. On the first start llama-gui detects your GPU and
installs the matching build of the current stable llama.cpp. It runs as
`nobody:users` (99:100), so all three directories must belong to that user.

| Mount | Holds | Notes |
| --- | --- | --- |
| `/data` | SQLite database, profiles, slot saves | Back this one up |
| `/builds` | Downloaded llama.cpp binaries | Must allow execution; a CUDA build takes over 1 GB; safe to lose, since builds download again |
| `/models` | GGUF files | Large; fast storage helps |

> **There is no authentication yet.** Anyone who can reach the port can use the
> API, change models and install builds. Keep it on a trusted network or behind a
> reverse proxy that authenticates for you. See [Future features](#future-features).

### Unraid

[unraid/llama-gui.xml](unraid/llama-gui.xml) is a Docker template with every
option below. Add this repository under *Apps → Settings → Template
Repositories*, or copy the file to
`/boot/config/plugins/dockerMan/templates-user/`.

| GPU | Setup |
| --- | --- |
| NVIDIA | Install the Nvidia Driver plugin. Add `--runtime=nvidia` to *Extra Parameters*. Set `NVIDIA_VISIBLE_DEVICES=all` and `NVIDIA_DRIVER_CAPABILITIES=all`. |
| AMD, Intel | Add `--device=/dev/dri` and `--group-add=<render gid>` to *Extra Parameters*. |
| None | Nothing to do. CPU builds work out of the box. |

Use `/mnt/cache/...` rather than `/mnt/user/...` for all three paths when you
can: SQLite and memory-mapped weights both behave poorly on the array's FUSE
layer. The *Keep weights in RAM* (`--mlock`) profile option also needs
`--cap-add=IPC_LOCK --ulimit memlock=-1`.

### NixOS

The flake has a NixOS module. It runs llama-gui as a systemd service with its state in
`/var/lib/llama-gui`, and the llama.cpp from nixpkgs instead of downloaded releases:

```nix
{
  inputs.llama-gui.url = "github:Angelmmiguel/llama-gui";

  outputs = { nixpkgs, llama-gui, ... }: {
    nixosConfigurations.tower = nixpkgs.lib.nixosSystem {
      modules = [
        llama-gui.nixosModules.default
        {
          services.llama-gui = {
            enable = true;
            host = "0.0.0.0";
            openFirewall = true;
          };
        }
      ];
    };
  };
}
```

`services.llama-gui.llamaCppPackage` is the llama.cpp that runs. It becomes the only build: the Builds
page shows where it comes from, and llama-gui does not install, switch or update builds. A rebuild that
changes the package restarts the service on it.

The default follows the machine:

| Machine | Default |
| --- | --- |
| NVIDIA driver in `services.xserver.videoDrivers` | `pkgs.pkgsCuda.llama-cpp` |
| `nixpkgs.config.rocmSupport = true` | `pkgs.llama-cpp`, which is then the ROCm build |
| Anything else | `pkgs.llama-cpp`, for the CPU |

The CUDA build is unfree, which the NVIDIA driver already needs, and is not in the binary cache, so it
compiles on the machine. It builds for every GPU architecture unless you narrow it, which is much faster:

```nix
nixpkgs.config.cudaCapabilities = [ "8.9" ];   # an RTX 40xx
```

On AMD and Intel GPUs nothing is detected, so pick a build. Both of these come from the binary cache:

```nix
services.llama-gui.llamaCppPackage = pkgs.pkgsRocm.llama-cpp;                       # ROCm, for the GPUs it supports
services.llama-gui.llamaCppPackage = pkgs.llama-cpp.override { vulkanSupport = true; };  # Vulkan, almost any GPU
```

For anything nixpkgs does not offer, override a package. `override` changes the inputs of the nixpkgs
package (`cudaSupport`, `rocmSupport`, `vulkanSupport`, `blasSupport`, …), and `overrideAttrs` the
derivation itself, such as its CMake flags:

```nix
services.llama-gui.llamaCppPackage =
  (pkgs.llama-cpp.override { vulkanSupport = true; }).overrideAttrs (old: {
    cmakeFlags = old.cmakeFlags ++ [ (lib.cmakeBool "GGML_NATIVE" true) ];
  });
```

A newer llama.cpp than nixpkgs has takes its tag and two hashes. Start with `lib.fakeHash`, and the build
fails naming the right one. nixpkgs bakes its own build number into `llama-server --version`, which is
what the Builds page shows, so replace it with the `b*` number of that release (its `nightly-tag.txt`
asset says which). For example:

```nix
services.llama-gui.llamaCppPackage = pkgs.llama-cpp.overrideAttrs (old: {
  version = "0.9.0";
  src = pkgs.fetchFromGitHub {
    owner = "ggml-org";
    repo = "llama.cpp";
    tag = "v0.9.0";
    hash = lib.fakeHash;
  };
  npmDepsHash = lib.fakeHash;  # the web UI under tools/ui
  cmakeFlags = map (
    flag: if lib.hasPrefix "-DLLAMA_BUILD_NUMBER" flag then lib.cmakeFeature "LLAMA_BUILD_NUMBER" "12132" else flag
  ) old.cmakeFlags;
});
```

The same works on `pkgs.pkgsCuda.llama-cpp` or any other variant. Whatever the build, its `bin` needs
`llama-server`, with router mode, and `llama-bench` for the Bench page. Set the option to `null` to download
releases as the image does, which needs `programs.nix-ld` because those binaries are built for Ubuntu.
With the NVIDIA driver the module also puts `nvidia-smi` on the service's `PATH`, which llama-gui reads GPUs
with.

Models and their launch profiles can be declared too, by alias, the model name clients ask for.
`settings` takes the fields of a profile as the API names them, and `ctx_size` is required:

```nix
services.llama-gui.profiles."qwen3-8b" = {
  # Hugging Face repo and quantisation, downloaded when missing.
  model = "hf.co/unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL";
  settings = {
    ctx_size = 32768;
    flash_attn = "on";
    cache_type_k = "q8_0";
    cache_type_v = "q8_0";
    load_at_start = true;
  };
};
```

After llama-gui starts and on every rebuild that changes them, `llama-gui-profiles.service` runs
`llama-gui profiles sync`, which downloads what is missing (with the vision projector of a multimodal
model, which the profile then uses), creates or replaces each profile under the id `nixos-<alias>`, reloads a changed one that is loaded, loads a `load_at_start` one that is not (the router only
reads that flag when it starts, which can be before the profile exists or its model is downloaded) and
deletes the `nixos-` profiles no longer declared. Running it again changes nothing. Profiles made in the web app stay, but edits there to declared
ones are undone on the next run. The rebuild waits for the first download of a model, and once a model is
on disk a machine without network keeps its profile as it is. For a GGUF put in the models directory some
other way, give `file`, relative to it, instead of `model`.

The other options are `port`, `modelsDir`, `user`, `group` and `extraPackages`. `settings` takes any variable
from the table below, and `environmentFile` takes secrets such as `LLAMA_GUI_HF_TOKEN`.

### Configuration

Everything has a default, and every variable has a matching `serve` flag.

| Variable | Default | Purpose |
| --- | --- | --- |
| `LLAMA_GUI_ADDR` | `:8080` in the image, `127.0.0.1:8080` otherwise | Listen address |
| `LLAMA_GUI_DATA` | `/data` | Database, profiles and slot saves |
| `LLAMA_GUI_BUILDS` | `/builds` | Downloaded llama.cpp binaries |
| `LLAMA_GUI_MODELS` | `/models` | GGUF directory |
| `LLAMA_GUI_LLAMA_VERSION` | `stable` | Version to install on first start: `stable`, `nightly`, a tag such as `v0.9.0` or `b12132`, or `none`. After that the Builds page decides. |
| `LLAMA_GUI_LLAMA_BACKEND` | `auto` | `auto`, `cpu`, `cuda`, `vulkan`, `rocm`, `sycl` or `openvino` |
| `LLAMA_GUI_LLAMA_UPDATES` | `notify` | `off`, `notify`, or `auto` to install new stable releases when idle |
| `LLAMA_GUI_LLAMA_DIR` | | Absolute path of a directory with `llama-server` (and `llama-bench`) built elsewhere, such as a Nix package's `bin`. It becomes the only build: the variables above, installs and updates no longer apply. |
| `LLAMA_GUI_KEEP_BUILDS` | `3` | Builds to keep for rollback; the active build is never removed |
| `LLAMA_GUI_MODELS_MAX` | `4` | Models loaded at once |
| `LLAMA_GUI_MEMORY_MARGIN` | `2048` | MiB kept free per GPU before evicting idle models |
| `LLAMA_GUI_LLAMA_ARGS` | | Extra arguments for every `llama-server`, as an escape hatch |
| `LLAMA_GUI_CAPTURE` | `meta` | `meta` keeps timings only; `bodies` also stores prompts and responses and enables replay |
| `LLAMA_GUI_RETENTION` | `7d` | How long request history and logs are kept |
| `LLAMA_GUI_LOG_VERBOSITY` | `3` | `llama-server -lv` level |
| `LLAMA_GUI_HF_TOKEN` | | Hugging Face token for gated repos (`HF_TOKEN` also works) |
| `LLAMA_GUI_GITHUB_TOKEN` | | Optional, raises GitHub's rate limit when checking releases |
| `LLAMA_GUI_ALLOWED_ORIGINS` | | Comma-separated origins such as `https://chat.example.com`, or `*`, whose web pages may call the inference routes. Management routes are never open to other sites. |
| `LLAMA_GUI_ALLOWED_HOSTS` | | Comma-separated host names to serve, or `*` for any. Needed only for public-looking names: IP addresses, single-label names such as `tower`, and names under `.local`, `.lan`, `.home`, `.internal`, `.intranet`, `.corp`, `.localdomain` and `.home.arpa` always work. |

llama-gui speaks plain HTTP. Put a reverse proxy in front of it for TLS, keep the
client's `Host` header when forwarding, and add a public name to
`LLAMA_GUI_ALLOWED_HOSTS`.

### Operating it

The image is a small Debian base with `bash`, `curl`, `jq`, `ps`, `top` and
`less`, and `nvidia-smi` comes from the NVIDIA runtime. For anything else, use
the binary from Unraid's container console or `docker exec`:

```
llama-gui serve                     run the service (the image's entrypoint)
llama-gui doctor                    check GPU, permissions, disk, network and libraries
llama-gui status                    server, build, loaded models and slots
llama-gui logs [-f]                 tail the llama-server log
llama-gui llama list|available      installed and released versions
llama-gui llama install <version>   download a build
llama-gui llama use <version>       switch build and restart
llama-gui models list               GGUF files on disk and whether they are loaded
llama-gui models pull <repo> [file...]  download from Hugging Face
llama-gui models rm <path>          delete a model
llama-gui models load|unload <id>   control the router
llama-gui profiles sync --prefix P <manifest.json>
                                    make the profiles with ids starting with P those of a manifest
llama-gui healthcheck               used by the image's HEALTHCHECK
```

## Future features

- **Authentication:** a dashboard login and API keys for the proxy.
- **SSO:** sign-in through self-hosted identity providers such as Authelia and PocketID.

## Development

Same toolchain as [mailarchive](https://github.com/Angelmmiguel/mailarchive): a
Nix flake, Go, and a Svelte 5 SvelteKit SPA compiled by adapter-static and
embedded with `go:embed`. The one Go dependency is a pure-Go SQLite driver, so the
binary stays static.

```bash
just web-install   # pnpm install, once
just dev           # Go server and Vite together, Vite proxies to Go
just test          # go test -race and vitest
just lint          # go vet, golangci-lint, svelte-check, eslint, prettier
just build         # bin/llama-gui with the web app embedded
just image         # the container image
nix build          # the package; `nix flake check` also boots the NixOS module in a VM
just demo          # the real service over a fake llama-server: emulated GPUs, sample models, traffic
```

## License

Apache 2.0, see [LICENSE](LICENSE).
