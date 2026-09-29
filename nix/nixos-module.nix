# NixOS module: installs ecdy system-wide and loads the plugin in every
# interactive zsh.
#
#   imports = [ ecdy.nixosModules.default ];
#   programs.ecdy.enable = true;
#
# The plugin is loaded from /etc/zshrc, before ~/.zshrc. Loading it before
# the plugins that wrap accept-line works (README, "Other plugins"); with
# Home Manager, prefer ecdy.homeManagerModules.default, which loads it at the
# end of ~/.zshrc as the README recommends.
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.ecdy;
in
{
  options.programs.ecdy = {
    enable = lib.mkEnableOption "ecdy, a layer on top of zsh that sends prompts to ACP agents";

    package = lib.mkOption {
      type = lib.types.package;
      default = self.packages.${pkgs.stdenv.hostPlatform.system}.default;
      defaultText = lib.literalExpression "ecdy.packages.\${system}.default";
      description = "The ecdy package to use.";
    };

    enableZshIntegration = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = ''
        Whether to load the plugin in interactive zsh (`eval "$(ecdy init zsh)"`).
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    environment.systemPackages = [ cfg.package ];

    programs.zsh.interactiveShellInit = lib.mkIf cfg.enableZshIntegration (
      lib.mkAfter ''
        eval "$(${lib.getExe cfg.package} init zsh)"
      ''
    );
  };
}
