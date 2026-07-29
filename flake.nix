{
  description = "wip — dev shell and package for the wip Go CLI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };

        version = self.shortRev or self.dirtyShortRev or "dev";

        wip = pkgs.buildGoModule {
          pname = "wip";
          inherit version;
          src = ./.;
          vendorHash = null;

          env.CGO_ENABLED = 0;

          ldflags = [
            "-X main.version=${version}"
            "-X main.commit=${self.rev or self.dirtyRev or "unknown"}"
            "-X main.date=1970-01-01T00:00:00Z"
          ];

          subPackages = [ "cmd/wip" ];

          postInstall = ''
            mkdir -p $out/share/wip
            cp -r assets $out/share/wip/assets
          '';

          meta = {
            description = "wip — a personal, agent-friendly project-management CLI";
            license = pkgs.lib.licenses.mit;
            mainProgram = "wip";
          };
        };
      in {
        packages.default = wip;

        devShells.default = pkgs.mkShell {
          name = "wip";
          packages = with pkgs; [
            go
            golangci-lint
            gofumpt
            git-cliff
            gnumake
            pre-commit
          ];

          shellHook = ''
            echo "wip dev shell — run 'make check' to lint+test, 'make hooks' to install pre-commit."
          '';
        };
      });
}
