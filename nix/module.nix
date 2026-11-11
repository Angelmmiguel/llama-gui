{
  config,
  lib,
  pkgs,
  ...
}:

let
  cfg = config.services.llama-gui;
  stateDir = "/var/lib/llama-gui";
  nvidia = lib.elem "nvidia" config.services.xserver.videoDrivers;

  # The input of `llama-gui profiles sync`, which owns the profiles with this prefix.
  profilePrefix = "nixos-";
  manifest = (pkgs.formats.json { }).generate "llama-gui-profiles.json" (
    lib.mapAttrsToList (
      alias: p:
      lib.optionalAttrs (p.model != null) { inherit (p) model; }
      // {
        profile =
          p.settings
          // {
            id = profilePrefix + alias;
            inherit alias;
          }
          // lib.optionalAttrs (p.file != null) { model_file = p.file; };
      }
    ) cfg.profiles
  );
in
{
  options.services.llama-gui = {
    enable = lib.mkEnableOption "llama-gui, a dashboard and control plane for llama.cpp";

    package = lib.mkOption {
      type = lib.types.package;
      description = "The llama-gui package to run.";
    };

    llamaCppPackage = lib.mkOption {
      type = lib.types.nullOr lib.types.package;
      # The NVIDIA driver already needs allowUnfree, which CUDA does too. Elsewhere CUDA would fail
      # evaluation without it, and it is not in the binary cache.
      default = if nvidia then pkgs.pkgsCuda.llama-cpp else pkgs.llama-cpp;
      defaultText = lib.literalExpression ''
        if lib.elem "nvidia" config.services.xserver.videoDrivers then pkgs.pkgsCuda.llama-cpp else pkgs.llama-cpp
      '';
      example = lib.literalExpression "pkgs.pkgsRocm.llama-cpp";
      description = ''
        The llama.cpp build that llama-gui runs. Its `bin` directory needs `llama-server`, and
        `llama-bench` for the Bench page.

        The default is a CUDA build with the NVIDIA driver, for the architectures in
        `nixpkgs.config.cudaCapabilities` if set and for all of them otherwise. Without it, the
        default is `pkgs.llama-cpp`, which `nixpkgs.config.rocmSupport` makes a ROCm build. Pick
        another such as `pkgs.pkgsRocm.llama-cpp` or `pkgs.llama-cpp.override { vulkanSupport = true; }`,
        or override one with `overrideAttrs` for another version or other build flags.

        llama-gui then uses only this build: it installs, switches and updates nothing, and its
        Builds page shows where the build comes from. Changing the package restarts the service.

        With `null`, llama-gui downloads prebuilt releases from GitHub, as it does in its container
        image. Those are built for Ubuntu and need `programs.nix-ld` to run on NixOS.
      '';
    };

    host = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1";
      example = "0.0.0.0";
      description = ''
        Address to listen on. llama-gui has no authentication: anyone who can reach it can use the
        API, change models and run inference. Keep it on a trusted network or behind an
        authenticating reverse proxy.
      '';
    };

    port = lib.mkOption {
      type = lib.types.port;
      default = 8080;
      description = "Port to listen on.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Whether to open {option}`services.llama-gui.port` in the firewall.";
    };

    modelsDir = lib.mkOption {
      type = lib.types.path;
      default = "${stateDir}/models";
      defaultText = lib.literalExpression ''"/var/lib/llama-gui/models"'';
      description = ''
        Directory of GGUF models. llama-gui downloads into it, so the service user must be able to
        write it.
      '';
    };

    settings = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.oneOf [
          lib.types.str
          lib.types.int
          lib.types.bool
        ]
      );
      default = { };
      example = {
        LLAMA_GUI_MODELS_MAX = 2;
        LLAMA_GUI_MEMORY_MARGIN = 1024;
        LLAMA_GUI_ALLOWED_HOSTS = "llm.example.com";
      };
      description = ''
        Environment variables for llama-gui, as listed in its README. They override what the
        other options set. Put secrets such as `LLAMA_GUI_HF_TOKEN` in
        {option}`services.llama-gui.environmentFile` instead, as these end up in the Nix store.
      '';
    };

    environmentFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = "/run/secrets/llama-gui.env";
      description = ''
        File of `NAME=value` lines loaded into the service's environment, for secrets such as
        `LLAMA_GUI_HF_TOKEN` and `LLAMA_GUI_GITHUB_TOKEN`.
      '';
    };

    extraPackages = lib.mkOption {
      type = lib.types.listOf lib.types.package;
      default = lib.optional nvidia config.hardware.nvidia.package.bin;
      defaultText = lib.literalExpression ''
        lib.optional (lib.elem "nvidia" config.services.xserver.videoDrivers) config.hardware.nvidia.package.bin
      '';
      description = ''
        Packages on the service's `PATH`. llama-gui reads NVIDIA GPUs through `nvidia-smi`.
      '';
    };

    profiles = lib.mkOption {
      type = lib.types.attrsOf (
        lib.types.submodule (
          { name, ... }:
          {
            options = {
              model = lib.mkOption {
                type = lib.types.nullOr lib.types.str;
                default = null;
                example = "hf.co/unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL";
                description = ''
                  The model, as a Hugging Face repository and quantisation: `user/repo:QUANT`,
                  optionally after `hf.co/` or `https://huggingface.co/`. It is downloaded when the
                  models directory lacks it, with the vision projector of a multimodal model, and
                  the profile's `model_file` and `mmproj` follow from it, as on the Profiles page.
                  Set `settings.mmproj` to pick another projector.

                  Once downloaded, a machine that cannot reach Hugging Face keeps the profile as it
                  is. Either this or {option}`file` is required.
                '';
              };

              file = lib.mkOption {
                type = lib.types.nullOr lib.types.str;
                default = null;
                example = "Qwen3-8B-UD-Q4_K_XL.gguf";
                description = ''
                  A GGUF put in {option}`services.llama-gui.modelsDir` some other way, relative to
                  it, instead of {option}`model`.
                '';
              };

              settings = lib.mkOption {
                type = lib.types.submodule {
                  freeformType = (pkgs.formats.json { }).type;
                  options = {
                    name = lib.mkOption {
                      type = lib.types.str;
                      default = name;
                      description = "Name shown in the web app.";
                    };
                    ctx_size = lib.mkOption {
                      type = lib.types.ints.positive;
                      description = "Context size, which llama-gui requires.";
                    };
                  };
                };
                example = {
                  ctx_size = 32768;
                  flash_attn = "on";
                  cache_type_k = "q8_0";
                  cache_type_v = "q8_0";
                  load_at_start = true;
                  extra_args = [ "--ctx-checkpoints 8" ];
                };
                description = ''
                  The rest of the profile, as the fields of `/api/v1/profiles` (the same as on the
                  Profiles page): `parallel`, `ngl`, `flash_attn`, `cache_type_k`, `mmproj`,
                  `load_at_start`, `idle_unload_seconds`, `extra_args` and so on. llama-gui
                  validates them, and the sync unit fails on a field it rejects.
                '';
              };
            };
          }
        )
      );
      default = { };
      example = lib.literalExpression ''
        {
          "qwen3-8b" = {
            model = "hf.co/unsloth/Qwen3-8B-GGUF:UD-Q4_K_XL";
            settings = {
              ctx_size = 32768;
              flash_attn = "on";
              load_at_start = true;
            };
          };
        }
      '';
      description = ''
        Launch profiles to keep in llama-gui, by alias: the model name clients ask for.

        After llama-gui starts, and on every rebuild that changes them, `llama-gui-profiles.service`
        downloads the models that are missing and creates or replaces each profile through the API.
        A changed profile that is loaded is reloaded with its new settings, and one with
        `load_at_start` that is not loaded is loaded, as llama.cpp only reads that flag when it
        starts, which can be before the profile exists or its model is downloaded. These profiles have
        ids starting with `nixos-`, and the unit deletes those that are no longer listed here.
        Profiles made from the web app are left alone, but edits there to these are undone on the
        next run.

        The first download of a large model can take a while, and the rebuild waits for it.
      '';
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "llama-gui";
      description = "User to run llama-gui as. It is created when left as the default.";
    };

    group = lib.mkOption {
      type = lib.types.str;
      default = "llama-gui";
      description = "Group to run llama-gui as. It is created when left as the default.";
    };
  };

  config = lib.mkIf cfg.enable {
    assertions = lib.concatLists (
      lib.mapAttrsToList (alias: p: [
        {
          assertion = builtins.match "[A-Za-z0-9][A-Za-z0-9._:-]{0,127}" alias != null;
          message = ''
            services.llama-gui.profiles."${alias}": an alias is up to 128 letters, digits, `.`, `_`,
            `:` and `-`, starting with a letter or digit.
          '';
        }
        {
          assertion = (p.model == null) != (p.file == null);
          message = "services.llama-gui.profiles.\"${alias}\": set either model or file.";
        }
      ]) cfg.profiles
    );

    warnings = lib.optional (cfg.llamaCppPackage == null && !config.programs.nix-ld.enable) ''
      services.llama-gui.llamaCppPackage is null, so llama-gui downloads prebuilt llama.cpp
      releases, which are built for Ubuntu and do not run on NixOS without programs.nix-ld.
    '';

    users.users = lib.mkIf (cfg.user == "llama-gui") {
      llama-gui = {
        isSystemUser = true;
        inherit (cfg) group;
        home = stateDir;
        # GPU device nodes.
        extraGroups = [
          "video"
          "render"
        ];
      };
    };
    users.groups = lib.mkIf (cfg.group == "llama-gui") { llama-gui = { }; };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ cfg.port ];

    systemd.tmpfiles.settings.llama-gui = lib.mkIf (!lib.hasPrefix "${stateDir}/" cfg.modelsDir) {
      ${cfg.modelsDir}.d = {
        inherit (cfg) user group;
        mode = "0750";
      };
    };

    systemd.services.llama-gui = {
      description = "llama-gui, a dashboard and control plane for llama.cpp";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];
      path = cfg.extraPackages;

      environment = lib.mapAttrs (_: toString) (
        {
          LLAMA_GUI_ADDR =
            if lib.hasInfix ":" cfg.host then
              "[${cfg.host}]:${toString cfg.port}"
            else
              "${cfg.host}:${toString cfg.port}";
          LLAMA_GUI_DATA = "${stateDir}/data";
          LLAMA_GUI_BUILDS = "${stateDir}/builds";
          LLAMA_GUI_MODELS = cfg.modelsDir;
        }
        // lib.optionalAttrs (cfg.llamaCppPackage != null) {
          LLAMA_GUI_LLAMA_DIR = "${lib.getBin cfg.llamaCppPackage}/bin";
        }
        // lib.mapAttrs (_: v: if lib.isBool v then lib.boolToString v else v) cfg.settings
      );

      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} serve";
        User = cfg.user;
        Group = cfg.group;
        StateDirectory = "llama-gui";
        StateDirectoryMode = "0750";
        WorkingDirectory = stateDir;
        EnvironmentFile = lib.mkIf (cfg.environmentFile != null) cfg.environmentFile;
        Restart = "on-failure";
        RestartSec = 5;
        # llama-gui stops llama-server within its own budget; this leaves room for it.
        TimeoutStopSec = 20;

        ReadWritePaths = lib.optional (!lib.hasPrefix "${stateDir}/" cfg.modelsDir) cfg.modelsDir;
        # Large models are mapped and, with --mlock, locked into memory.
        LimitMEMLOCK = "infinity";

        # GPUs need /dev and JIT-compiled kernels, so PrivateDevices and
        # MemoryDenyWriteExecute stay off.
        CapabilityBoundingSet = "";
        LockPersonality = true;
        NoNewPrivileges = true;
        PrivateTmp = true;
        ProtectClock = true;
        ProtectControlGroups = true;
        # A models directory under /home stays reachable, read-only but for itself.
        ProtectHome =
          if lib.hasPrefix "/home/" cfg.modelsDir || lib.hasPrefix "/root/" cfg.modelsDir then
            "read-only"
          else
            true;
        ProtectHostname = true;
        ProtectKernelLogs = true;
        ProtectKernelModules = true;
        ProtectKernelTunables = true;
        ProtectProc = "invisible";
        ProtectSystem = "strict";
        RemoveIPC = true;
        RestrictAddressFamilies = [
          "AF_INET"
          "AF_INET6"
          "AF_UNIX"
          "AF_NETLINK"
        ];
        RestrictNamespaces = true;
        RestrictRealtime = true;
        RestrictSUIDSGID = true;
        SystemCallArchitectures = "native";
        UMask = "0027";
      };
    };

    # Runs with no profiles too, so that removing the last one deletes it from llama-gui.
    systemd.services.llama-gui-profiles = {
      description = "Sync llama-gui's profiles with services.llama-gui.profiles";
      wantedBy = [ "llama-gui.service" ];
      after = [ "llama-gui.service" ];
      bindsTo = [ "llama-gui.service" ];
      restartTriggers = [ manifest ];
      environment = { inherit (config.systemd.services.llama-gui.environment) LLAMA_GUI_ADDR; };
      serviceConfig = {
        Type = "oneshot";
        RemainAfterExit = true;
        # llama-gui.service is up once its process has started, before it listens.
        ExecStartPre = pkgs.writeShellScript "llama-gui-wait" ''
          until ${lib.getExe cfg.package} healthcheck 2>/dev/null; do sleep 1; done
        '';
        ExecStart = "${lib.getExe cfg.package} profiles sync --prefix ${profilePrefix} ${manifest}";
        User = cfg.user;
        Group = cfg.group;
        # The first run downloads the models.
        TimeoutStartSec = "infinity";
        PrivateTmp = true;
        ProtectHome = "read-only";
        ProtectSystem = "strict";
        NoNewPrivileges = true;
      };
    };
  };
}
