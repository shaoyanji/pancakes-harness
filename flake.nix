{
  description = "Thin flake packaging for pancakes-harness";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs =
    { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (
      system:
      let
        pkgs = import nixpkgs {
          inherit system;
        };
        lib = pkgs.lib;
        version = "0.2.4";

        # Canonical Go toolchain for this project (matches go.mod: go 1.23)
        go = pkgs.go_1_23;

        mkBinary =
          {
            pname,
            subPackage,
            binaryName,
          }:
          pkgs.buildGoModule {
            inherit pname version go;
            src = ./.;
            vendorHash = null;
            subPackages = [ subPackage ];

            ldflags = [
              "-s"
              "-w"
            ];

            doInstallCheck = true;
            installCheckPhase = ''
              $out/bin/${binaryName} -version | grep -F "${binaryName} ${version}"
              $out/bin/${binaryName} -h >/dev/null
            '';

            meta = {
              description = "Local-first context and egress kernel";
              mainProgram = binaryName;
              platforms = lib.platforms.unix;
            };
          };

        harness = mkBinary {
          pname = "pancakes-harness";
          subPackage = "cmd/harness";
          binaryName = "harness";
        };

        demoCli = mkBinary {
          pname = "pancakes-harness-demo-cli";
          subPackage = "cmd/demo-cli";
          binaryName = "demo-cli";
        };

        tests = pkgs.buildGoModule {
          pname = "pancakes-harness-tests";
          inherit version go;
          src = ./.;
          vendorHash = null;
          subPackages = [ "cmd/harness" ];

          doCheck = true;
          checkPhase = ''
            runHook preCheck
            export HOME="$TMPDIR"
            export GOCACHE="$TMPDIR/go-cache"
            go test -p 1 ./...
            runHook postCheck
          '';

          buildPhase = ''
            runHook preBuild
            runHook postBuild
          '';

          installPhase = ''
            runHook preInstall
            mkdir -p "$out"
            runHook postInstall
          '';
        };
      in
      {
        packages = {
          default = harness;
          harness = harness;
          demo-cli = demoCli;
        };

        apps = {
          harness = {
            type = "app";
            program = "${harness}/bin/harness";
            meta = harness.meta;
          };
          demo-cli = {
            type = "app";
            program = "${demoCli}/bin/demo-cli";
            meta = demoCli.meta;
          };
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            nixfmt-rfc-style
          ];

          shellHook = ''
            export GOROOT=
            export PATH="${go}/bin:$PATH"
          '';
        };

        checks = {
          harness = harness;
          demo-cli = demoCli;
          tests = tests;
        };
      }
    );
}
