{
  description = "ecdy — smart layer on top of zsh that sends prompts to ACP agents";

  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs =
    { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system: f nixpkgs.legacyPackages.${system});
      version = self.shortRev or self.dirtyShortRev or "dev";
    in
    {
      packages = forAllSystems (pkgs: {
        default = pkgs.buildGoModule {
          pname = "ecdy";
          inherit version;
          src = self;
          vendorHash = "sha256-7K17JaXFsjf163g5PXCb5ng2gYdotnZ2IDKk8KFjNj0=";
          subPackages = [ "cmd/ecdy" ];
          env.CGO_ENABLED = 0;
          ldflags = [
            "-s"
            "-w"
            "-X main.version=${version}"
          ];
          meta = {
            description = "Type in zsh; prompts go to your ACP agent";
            homepage = "https://github.com/Raimguzhinov/ecdy";
            license = pkgs.lib.licenses.asl20;
            mainProgram = "ecdy";
          };
        };
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            zsh
            tmux
            nodejs # npx-launched ACP agents (claude-agent-acp, codex-acp)
          ];
          # A GOROOT inherited from the user's environment would pair this
          # shell's go binary with a different standard library.
          shellHook = ''
            unset GOROOT
          '';
        };
      });

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
