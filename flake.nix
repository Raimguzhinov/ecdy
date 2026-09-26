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
          vendorHash = "sha256-RnLGn1LE4H5MYz+2Rvl3r5veXrUcasS73fQW1yNVNrQ=";
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

      devShells = forAllSystems (
        pkgs:
        let
          default = pkgs.mkShell {
            packages = with pkgs; [
              go
              gopls
              golangci-lint
              zsh
              tmux
              nodejs # npx-launched ACP agents (claude-agent-acp, codex-acp)
            ];
            # Third-party zsh plugins loaded by the coexistence PTY tests.
            ECDY_TEST_ZSH_SYNTAX_HIGHLIGHTING = "${pkgs.zsh-syntax-highlighting}/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh";
            ECDY_TEST_ZSH_AUTOSUGGESTIONS = "${pkgs.zsh-autosuggestions}/share/zsh-autosuggestions/zsh-autosuggestions.zsh";
            # A GOROOT inherited from the user's environment would pair this
            # shell's go binary with a different standard library.
            shellHook = ''
              unset GOROOT
            '';
          };
          zshVersions = import ./nix/zsh-versions.nix { inherit pkgs; };
        in
        {
          inherit default;
          # The PTY tests run against every supported zsh release:
          #   nix develop .#zsh-matrix -c go test ./shell/
          # Older releases are built from source on first use.
          zsh-matrix = default.overrideAttrs {
            ECDY_TEST_ZSH = pkgs.lib.concatMapStringsSep ":" (z: "${z}/bin/zsh") (
              builtins.attrValues zshVersions
            );
          };
        }
      );

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
