{
  lib,
  buildGoModule,
  stdenvNoCC,
  fetchPnpmDeps,
  pnpmConfigHook,
  pnpm,
  nodejs_22,
  version ? "dev",
}:

let
  src = lib.fileset.toSource {
    root = ../.;
    fileset = lib.fileset.unions [
      ../go.mod
      ../go.sum
      ../cmd
      ../internal
      (lib.fileset.fileFilter (file: file.hasExt "go") ../web)
    ];
  };

  # The SvelteKit app, which the binary embeds from web/build.
  web = stdenvNoCC.mkDerivation (finalAttrs: {
    pname = "llama-gui-web";
    inherit version;
    src = lib.fileset.toSource {
      root = ../web;
      fileset = lib.fileset.difference ../web (
        lib.fileset.unions [
          (lib.fileset.maybeMissing ../web/node_modules)
          (lib.fileset.maybeMissing ../web/build)
          (lib.fileset.maybeMissing ../web/.svelte-kit)
        ]
      );
    };

    nativeBuildInputs = [
      nodejs_22
      pnpm
      pnpmConfigHook
    ];

    pnpmDeps = fetchPnpmDeps {
      inherit (finalAttrs) pname version src;
      inherit pnpm;
      fetcherVersion = 4;
      hash = "sha256-WS0hRP5jmQwvd5UxIycD7cVQNhW/Wuc2o2XVcbjRgxo=";
    };

    buildPhase = ''
      runHook preBuild
      pnpm build
      runHook postBuild
    '';

    installPhase = ''
      runHook preInstall
      cp -r build $out
      runHook postInstall
    '';
  });
in
buildGoModule {
  pname = "llama-gui";
  inherit version src;

  vendorHash = "sha256-zafxVNQsomtOwZ3RCyAeIgBg4Aj4Pf6yD3qZt6umd+Y=";

  env.CGO_ENABLED = "0";
  subPackages = [ "cmd/llama-gui" ];
  ldflags = [
    "-s"
    "-w"
    "-X main.version=${version}"
  ];

  preBuild = ''
    cp -r ${web} web/build
  '';

  # The tests spawn processes and bind ports; `just test` runs them.
  doCheck = false;

  passthru = { inherit web; };

  meta = {
    description = "Dashboard and control plane for llama.cpp";
    homepage = "https://github.com/Angelmmiguel/llama-gui";
    license = lib.licenses.asl20;
    mainProgram = "llama-gui";
    platforms = lib.platforms.linux;
  };
}
