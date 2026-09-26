# zsh releases the PTY tests run against: the oldest supported one, the
# most widespread one and the newest one in nixpkgs. Older releases are
# built from the upstream tarballs with nixpkgs' zsh recipe.
{ pkgs }:
let
  release =
    version: hash:
    # gcc >= 14 turns implicit declarations and pointer mismatches into
    # errors, which makes the configure tests of zsh <= 5.9 misdetect the
    # system (termcap tables, signal handling); build them with gcc 13, as
    # distributions did when these releases were current.
    (pkgs.zsh.override { stdenv = pkgs.gcc13Stdenv; }).overrideAttrs (old: {
      inherit version;
      src = pkgs.fetchurl {
        url = "mirror://sourceforge/zsh/zsh-${version}.tar.xz";
        inherit hash;
      };
      patches = [ ];
      # The tests need no docs, and texinfo 7 rejects their texi2html.conf.
      postInstall =
        builtins.replaceStrings
          [ "make install.info install.html" ]
          [ "mkdir -p $out/share/zsh/htmldoc $info" ]
          old.postInstall;
      doCheck = false;
    });
in
{
  "5.8.1" = release "5.8.1" "sha256-tpc1ILrOYAtHeSACabHl155fUFrElSBYwRrVu/DdmRk=";
  "5.9" = release "5.9" "sha256-m40ezt1bXoH78ZGOh2dSp92UjgXBoNuhCrhjhC1FrNU=";
  latest = pkgs.zsh;
}
