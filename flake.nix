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
          vendorHash = "sha256-+yCn/j7O6N1MFKiJ7JC2jhDe7shHmtQnssU525ExJyk=";

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
            # assets.go only exists so `assets/` can embed itself as the
            # binary's last-resort fallback; it is source, not a shipped
            # asset, and must not appear in the installed share tree.
            rm -f $out/share/wip/assets/assets.go
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
