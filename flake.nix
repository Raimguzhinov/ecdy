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
          vendorHash = "sha256-k/zTv3h4a99+W1a0piU4FEf+xHZKsRCvz73DMphadP8=";
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
              # Third-party tools of the compatibility PTY tests.
              atuin
              fzf
            ];
            # Third-party zsh plugins loaded by the coexistence PTY tests.
            ECDY_TEST_ZSH_SYNTAX_HIGHLIGHTING = "${pkgs.zsh-syntax-highlighting}/share/zsh-syntax-highlighting/zsh-syntax-highlighting.zsh";
            ECDY_TEST_ZSH_AUTOSUGGESTIONS = "${pkgs.zsh-autosuggestions}/share/zsh-autosuggestions/zsh-autosuggestions.zsh";
            ECDY_TEST_ZSH_VI_MODE = "${pkgs.zsh-vi-mode}/share/zsh-vi-mode/zsh-vi-mode.plugin.zsh";
            ECDY_TEST_FZF_TAB = "${pkgs.zsh-fzf-tab}/share/fzf-tab/fzf-tab.plugin.zsh";
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
          # Renders the README's demo GIF from docs/demo/*.tape:
          #   nix develop .#demo -c docs/demo/render.sh
          demo = default.overrideAttrs (old: {
            nativeBuildInputs = (old.nativeBuildInputs or [ ]) ++ [
              # vhs 0.12.0 prints "Creating demo.gif..." and writes
              # nothing; 0.11.0 works.
              (pkgs.vhs.overrideAttrs rec {
                version = "0.11.0";
                src = pkgs.fetchFromGitHub {
                  owner = "charmbracelet";
                  repo = "vhs";
                  rev = "v${version}";
                  hash = "sha256-VOiI+ddiax04QtCcDr6ze53kd/HHGbfQE3j/32iq4Ro=";
                };
                vendorHash = "sha256-cgKLYUATtn4hMdIOXZe9JWYNUOrX3S6BDfvS+rIWDfM=";
              })
              pkgs.ffmpeg
              pkgs.git
            ];
            # Only this font, so that the recording looks the same everywhere.
            FONTCONFIG_FILE = pkgs.makeFontsConf { fontDirectories = [ pkgs.nerd-fonts.jetbrains-mono ]; };
            ECDY_DEMO_FONT = "${pkgs.nerd-fonts.jetbrains-mono}/share/fonts/truetype/NerdFonts/JetBrainsMono";
          });
        }
      );

      overlays.default = final: _prev: {
        ecdy = self.packages.${final.stdenv.hostPlatform.system}.default;
      };

      nixosModules.default = import ./nix/nixos-module.nix self;
      homeManagerModules.default = import ./nix/home-manager-module.nix self;

      formatter = forAllSystems (pkgs: pkgs.nixfmt);
    };
}
