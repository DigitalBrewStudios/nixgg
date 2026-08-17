# dynDrvConfigureCacheStdenv early-cutoff test fixture, driven by
# tests/dyndrv-configure-cache-cutoff.sh. Same shape as
# tests/configure-cache-cutoff-fixture.nix (constructs the package
# DIRECTLY, not via pkgs.foo.override — nixpkgs' own
# .override/.overrideAttrs reapplication always re-invokes the wrapped
# function with its ORIGINAL args first, so a src substitution applied
# that way never reaches group A at all), parameterized to drive three
# scenarios: baseline, an edit to a file the filter excludes, an edit
# to a file it includes.
{
  flakeDir, # path to the nixgg checkout, passed by the driver script
  edit ? null, # null | "excluded" | "included"
}:
let
  flake = builtins.getFlake (toString flakeDir);
  nixpkgsFlake = flake.inputs.nix-15793.inputs.nixpkgs;
  pkgs = nixpkgsFlake.legacyPackages.${builtins.currentSystem};
  dynDrvConfigureCacheStdenv = flake.outputs.packages.${builtins.currentSystem}.dynDrvConfigureCacheStdenv;
  configureSrcFilterPresets = flake.outputs.packages.${builtins.currentSystem}.configureSrcFilterPresets;

  realSrc = pkgs.hello.src;
  excludedEditPath = "src/hello.c";
  excludedEditLine = "/* excluded-file cutoff test */\n";
  includedEditPath = "configure.ac";
  includedEditLine = "dnl included-file cutoff test\n";

  editedSrc =
    let
      path = if edit == "excluded" then excludedEditPath else includedEditPath;
      line = if edit == "excluded" then excludedEditLine else includedEditLine;
    in
    pkgs.runCommand "hello-src-edited" { nativeBuildInputs = [ pkgs.gnutar pkgs.gzip ]; } ''
      mkdir -p "$out"
      if [ -d ${realSrc} ]; then
        cp -a ${realSrc}/. "$out"
      else
        tar xf ${realSrc} -C "$out" --strip-components=1
      fi
      chmod -R u+w "$out"
      printf '%s' ${builtins.toJSON line} >> "$out"/${path}
    '';

  src = if edit == null then realSrc else editedSrc;
in
(dynDrvConfigureCacheStdenv {
  stdenv = pkgs.stdenv;
  configureSrcFilter = {
    includePatterns = configureSrcFilterPresets.autotools;
    existenceStubs = [ "src/hello.c" ];
  };
}).mkDerivation {
  pname = "hello";
  version = "2.12.3";
  inherit src;
}
