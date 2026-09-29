# Home Manager module: installs ecdy for the user, loads the plugin at the
# end of ~/.zshrc and writes ~/.config/ecdy/config.toml.
#
#   imports = [ ecdy.homeManagerModules.default ];
#   programs.ecdy = {
#     enable = true;
#     settings.default_agent = "claude";
#   };
self:
{
  config,
  lib,
  pkgs,
  ...
}:
let
  cfg = config.programs.ecdy;
  toml = pkgs.formats.toml { };
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
        Whether to load the plugin in interactive zsh (`eval "$(ecdy init zsh)"`)
        at the end of ~/.zshrc, after the plugins that wrap accept-line.
      '';
    };

    settings = lib.mkOption {
      inherit (toml) type;
      default = { };
      example = lib.literalExpression ''
        {
          default_agent = "codex";
          idle_timeout = "30m";
          context.commands = 20;
          agents.claude.command = [ "claude-agent-acp" ];
        }
      '';
      description = ''
        Written to {file}`$XDG_CONFIG_HOME/ecdy/config.toml`; see the README
        for the keys. Nothing is written when empty, and the built-in
        defaults apply.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    home.packages = [ cfg.package ];

    xdg.configFile."ecdy/config.toml" = lib.mkIf (cfg.settings != { }) {
      source = toml.generate "ecdy-config.toml" cfg.settings;
    };

    # The end of ~/.zshrc: after initExtra's 1500, so after the plugins.
    programs.zsh.initContent = lib.mkIf cfg.enableZshIntegration (
      lib.mkOrder 2000 ''
        eval "$(${lib.getExe cfg.package} init zsh)"
      ''
    );
  };
}
