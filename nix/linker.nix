# Link CA derivation.
#
# `inputs` is a native Nix list of { drv, name }. Each `drv` is either
# a derivation (thunk mode: not-yet-built, produced by `import
# .../foo.nix`) or a `pureStorePath` result (realise mode: already in
# the store). Either way, `${item.drv}/${item.name}` interpolates to
# the linker CLI.
#
# The link command itself comes from Go — see resolve-script.nix. That
# includes the `-l`-after-inputs ordering that ffmpeg needed: this file
# used to reimplement that split, and sandbox mode implemented it
# separately, which is precisely the kind of duplication that produced
# divergent drv hashes.
{
  compilerRoot  ? (import ./toolchain.nix).compilerRoot,
  bashRoot      ? (import ./toolchain.nix).bashRoot,
  coreutilsRoot ? (import ./toolchain.nix).coreutilsRoot,
  outName,
  inputs,
  scriptTemplate,
  markerTag,
  storeDepsJSON ? "[]",
  wrapperEnvJSON ? "{}",
}:
let
  pureStorePath = import ./pure-store-path.nix;
  bash        = pureStorePath bashRoot;
  coreutils   = pureStorePath coreutilsRoot;
  compiler    = pureStorePath compilerRoot;
  storeDeps   = map pureStorePath (builtins.fromJSON storeDepsJSON);
  wrapperEnv  = builtins.fromJSON wrapperEnvJSON;
  script      = import ./resolve-script.nix {
    inherit scriptTemplate markerTag coreutils compiler inputs;
  };
in
derivation ({
  name = "bin-${outName}";
  system = builtins.currentSystem;

  __contentAddressed = true;
  outputHashMode = "nar";
  outputHashAlgo = "sha256";

  builder = "${bash}/bin/bash";
  args = [ "-c" script ];

  _storeDeps = builtins.concatStringsSep ":" storeDeps;
} // wrapperEnv)
