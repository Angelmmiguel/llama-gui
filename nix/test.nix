# Boots a VM with the module and llama-gui over the fake llama-server, so it needs no model or GPU.
{
  pkgs,
  module,
  package,
  fakeLlamaServer,
}:

pkgs.testers.runNixOSTest {
  name = "llama-gui";

  nodes.machine =
    { lib, ... }:
    {
      imports = [ module ];
      services.llama-gui = {
        enable = true;
        inherit package;
        llamaCppPackage = fakeLlamaServer;
        settings = {
          LLAMA_GUI_MODELS_MAX = 2;
          # Nothing listens there: the VM has no network.
          LLAMA_GUI_GITHUB_API = "http://127.0.0.1:9";
        };
        profiles = {
          small = {
            file = "small.gguf";
            settings = {
              ctx_size = 4096;
              flash_attn = "on";
              # Zero values: llama-gui drops the first and keeps the pointer fields.
              parallel = 0;
              jinja = false;
              temp = 0;
              extra_args = [ "--ctx-checkpoints 8" ];
            };
          };
          gone.file = "gone.gguf";
          gone.settings.ctx_size = 2048;
        };
      };

      # Changes one profile and drops the other, for the sync unit to apply on a switch.
      specialisation.changed.configuration = {
        services.llama-gui.profiles = lib.mkForce {
          small = {
            file = "small.gguf";
            settings.ctx_size = 8192;
          };
        };
      };
    };

  testScript = ''
    import json

    machine.wait_for_unit("llama-gui.service")
    machine.wait_for_open_port(8080)

    def status(_):
        st = json.loads(machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/status"))
        return st["state"] == "running" and st["active_build"]["id"] == "external"

    retry(status, timeout_seconds=60)

    builds = json.loads(machine.succeed("curl -sf http://127.0.0.1:8080/api/v1/builds"))["builds"]
    assert len(builds) == 1 and builds[0]["path"] == "${fakeLlamaServer}/bin", builds

    code = machine.succeed(
        "curl -s -o /dev/null -w '%{http_code}' -X POST -d '{\"version\":\"stable\"}' "
        "http://127.0.0.1:8080/api/v1/builds/install"
    )
    assert code == "409", code

    api = "http://127.0.0.1:8080/api/v1"

    def profiles():
        out = json.loads(machine.succeed(f"curl -sf {api}/profiles"))["profiles"]
        return {p["id"]: p for p in out}

    def rerun_changes_nothing():
        machine.succeed("systemctl restart llama-gui-profiles.service")
        inv = machine.succeed("systemctl show -p InvocationID --value llama-gui-profiles").strip()
        log = machine.succeed(f"journalctl -o cat _SYSTEMD_INVOCATION_ID={inv}")
        assert "up to date" in log, log
        for word in ("created", "updated", "deleted", "reloaded", "pulling"):
            assert word not in log, log

    machine.wait_for_unit("llama-gui-profiles.service")
    ps = profiles()
    assert set(ps) == {"nixos-small", "nixos-gone"}, ps
    assert "parallel" not in ps["nixos-small"], ps
    assert ps["nixos-small"]["jinja"] is False and ps["nixos-small"]["temp"] == 0, ps
    rerun_changes_nothing()
    small = ps["nixos-small"]
    assert small["alias"] == "small" and small["name"] == "small", small
    assert small["model_file"] == "small.gguf" and small["ctx_size"] == 4096, small
    assert small["flash_attn"] == "on" and small["extra_args"] == ["--ctx-checkpoints 8"], small

    # One made from the web app, which the unit leaves alone.
    machine.succeed(
        f"curl -sf -X POST -H 'Content-Type: application/json' {api}/profiles "
        """-d '{"name":"mine","alias":"mine","model_file":"mine.gguf","ctx_size":1024}'"""
    )

    machine.succeed("/run/booted-system/specialisation/changed/bin/switch-to-configuration test")
    machine.wait_for_unit("llama-gui-profiles.service")
    ps = profiles()
    assert "nixos-gone" not in ps, ps
    assert ps["nixos-small"]["ctx_size"] == 8192 and "flash_attn" not in ps["nixos-small"], ps
    assert [p["alias"] for p in ps.values() if not p["id"].startswith("nixos-")] == ["mine"], ps
    rerun_changes_nothing()

    machine.succeed("test -d /var/lib/llama-gui/data -a -d /var/lib/llama-gui/models")
    machine.fail("test -e /var/lib/llama-gui/builds")
    machine.succeed("pgrep -u llama-gui -f '${fakeLlamaServer}/bin/llama-server'")
    machine.succeed("systemctl stop llama-gui.service")
    machine.fail("pgrep -f '${fakeLlamaServer}/bin/llama-server'")
  '';
}
