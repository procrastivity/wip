{
  description = "wip — dev shell for the wip-plumbing bash CLI";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = import nixpkgs { inherit system; };
      in {
        devShells.default = pkgs.mkShell {
          name = "wip";
          packages = with pkgs; [
            bash
            coreutils
            curl
            git
            gnumake
            jq
            yq-go
            shellcheck
            shfmt
            pre-commit
          ];

          shellHook = ''
            echo "wip dev shell — run 'make check' to lint+test, 'make hooks' to install pre-commit."
          '';
        };
      });
}
