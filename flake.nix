{
  description = "Dashboard and control plane for llama.cpp";

  inputs = {
    flake-utils.url = "github:numtide/flake-utils";
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = {
    self,
    flake-utils,
    nixpkgs,
  }: {
    # services.llama-gui, with this flake's llama-gui as the default package.
    nixosModules.default = { lib, pkgs, ... }: {
      imports = [ ./nix/module.nix ];
      services.llama-gui.package = lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.default;
    };

    overlays.default = final: _: {
      llama-gui = final.callPackage ./nix/package.nix { version = self.shortRev or "dirty"; };
    };
  } // flake-utils.lib.eachDefaultSystem (system: let
    pkgs = import nixpkgs { inherit system; };
  in {
    packages = nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux rec {
      default = llama-gui;
      llama-gui = pkgs.callPackage ./nix/package.nix { version = self.shortRev or "dirty"; };
      # The test double as a llama.cpp build: bin/llama-server, for the module's test.
      fake-llama-server = llama-gui.overrideAttrs {
        pname = "fake-llama-server";
        subPackages = [ "cmd/fake-llama-server" ];
        preBuild = "";
        postInstall = "mv $out/bin/fake-llama-server $out/bin/llama-server";
        meta.mainProgram = "llama-server";
      };
    };

    checks = nixpkgs.lib.optionalAttrs pkgs.stdenv.hostPlatform.isLinux {
      nixos-module = import ./nix/test.nix {
        inherit pkgs;
        module = self.nixosModules.default;
        package = self.packages.${system}.llama-gui;
        fakeLlamaServer = self.packages.${system}.fake-llama-server;
      };
    };

    devShells.default = pkgs.mkShell {
      packages = with pkgs; [
        # Go backend (server + CLI)
        go
        gopls
        gotools          # goimports, stringer, ...
        golangci-lint
        delve

        # SvelteKit frontend
        nodejs_22
        pnpm
        # Browsers for the Playwright e2e. @playwright/test in web/package.json
        # must be the exact version of playwright-driver here.
        playwright-driver.browsers

        # Misc
        sqlite           # CLI to inspect the database
        just
        curl
        jq
        docker-client    # `just image`, against the system's daemon
      ]
      # A real browser to try the app by hand or headless. Linux-only in
      # nixpkgs; elsewhere the Playwright browsers above still cover the e2e.
      ++ lib.optionals stdenv.hostPlatform.isLinux [
        chromium
      ];

      # Keep Go's module cache and build cache inside the project so a
      # fresh clone or a CI runner does not pollute $HOME.
      shellHook = ''
        export GOPATH="$PWD/.go"
        export GOMODCACHE="$GOPATH/pkg/mod"
        export GOCACHE="$GOPATH/cache"
        # Writable module cache dirs, so worktrees (and .go/) can be deleted.
        export GOFLAGS="-modcacherw''${GOFLAGS:+ $GOFLAGS}"
        export PATH="$GOPATH/bin:$PATH"

        # Pure Go only, SQLite included: the binary stays static.
        export CGO_ENABLED=0

        export PLAYWRIGHT_BROWSERS_PATH="${pkgs.playwright-driver.browsers}"
        export PLAYWRIGHT_SKIP_VALIDATE_HOST_REQUIREMENTS=true

        # Load private env files (gitignored).
        for f in .env .env.local; do
          if [ -f "$f" ]; then
            set -a
            . "$f"
            set +a
          fi
        done
      '';
    };

    formatter = pkgs.nixfmt-rfc-style;
  });
}
