{
  description = "Thin flake packaging for pancakes-harness";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-24.11";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = {
    self,
    nixpkgs,
    flake-utils,
  }:
    flake-utils.lib.eachDefaultSystem (
      system: let
        pkgs = import nixpkgs {
          inherit system;
        };
        lib = pkgs.lib;

        # The release version lives in the Go sources and installCheck compares the
        # built binary against it, so read it there: a second copy in this file
        # drifts silently and then fails the build for the wrong reason.
        readVersion = {
          file,
          pattern,
        }: let
          hits = builtins.filter (m: m != null) (
            map (line: builtins.match pattern line) (lib.splitString "\n" (builtins.readFile file))
          );
        in
          if hits == []
          then throw "readVersion: ${pattern} did not match any line of ${toString file}"
          else builtins.head (builtins.head hits);

        # Canonical Go toolchain for this project (matches go.mod: go 1.23)
        go = pkgs.go_1_23;

        mkBinary = {
          pname,
          subPackage,
          binaryName,
          versionFile,
          versionPattern,
        }: let
          version = readVersion {
            file = versionFile;
            pattern = versionPattern;
          };
        in
          pkgs.buildGoModule {
            inherit pname version go;
            src = ./.;
            vendorHash = null;
            subPackages = [subPackage];

            ldflags = [
              "-s"
              "-w"
            ];

            # buildGoModule names the binary after its subpackage directory;
            # install under the name the docs and `apps` promise.
            postInstall = lib.optionalString (builtins.baseNameOf subPackage != binaryName) ''
              mv "$out/bin/${builtins.baseNameOf subPackage}" "$out/bin/${binaryName}"
            '';

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
          versionFile = ./cmd/harness/main.go;
          versionPattern = ".*releaseVersion = \"([0-9][^\"]*)\".*";
        };

        demoCli = mkBinary {
          pname = "pancakes-harness-demo-cli";
          subPackage = "cmd/demo-cli";
          binaryName = "demo-cli";
          versionFile = ./cmd/demo-cli/main.go;
          versionPattern = ".*demo-cli ([0-9][0-9.]*).*";
        };

        dream = mkBinary {
          pname = "pancakes-harness-dream";
          subPackage = "cmd/dream";
          binaryName = "dream-daemon";
          versionFile = ./cmd/dream/main.go;
          versionPattern = ".*const version = \"([0-9][^\"]*)\".*";
        };

        # The test suite builds the sources once and runs go test over all packages.
        tests = let
          version = readVersion {
            file = ./cmd/harness/main.go;
            pattern = ".*releaseVersion = \"([0-9][^\"]*)\".*";
          };
        in
          pkgs.buildGoModule {
            pname = "pancakes-harness-tests";
            inherit version go;
            src = ./.;
            vendorHash = null;
            subPackages = ["cmd/harness"];

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
      in {
        packages = {
          default = harness;
          harness = harness;
          demo-cli = demoCli;
          dream = dream;
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
          dream = {
            type = "app";
            program = "${dream}/bin/dream-daemon";
            meta = dream.meta;
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
          dream = dream;
          tests = tests;
        };
      }
    );
}
