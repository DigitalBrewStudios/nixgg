# Link CA derivation.
#
# `inputs` is a native Nix list of { drv, name }. Each `drv` is either
# a derivation (thunk mode: not-yet-built, produced by `import
# .../foo.nix`) or a `pureStorePath` result (realise mode: already in
# the store). Either way, `${item.drv}/${item.name}` interpolates to
# the linker CLI.
{
  compilerRoot  ? (import ./toolchain.nix).compilerRoot,
  bashRoot      ? (import ./toolchain.nix).bashRoot,
  coreutilsRoot ? (import ./toolchain.nix).coreutilsRoot,
  toolBasename,
  outName,
  inputs,
  flagsJSON,
  storeDepsJSON ? "[]",
  wrapperEnvJSON ? "{}",
}:
let
  pureStorePath = import ./pure-store-path.nix;
  bash        = pureStorePath bashRoot;
  coreutils   = pureStorePath coreutilsRoot;
  compiler    = pureStorePath compilerRoot;
  flags       = builtins.fromJSON flagsJSON;
  storeDeps   = map pureStorePath (builtins.fromJSON storeDepsJSON);
  wrapperEnv  = builtins.fromJSON wrapperEnvJSON;

  # Split `-l<name>` out of flags so they can be emitted AFTER
  # objects/archives on the link line. Classic single-pass ld
  # resolves libraries against objects mentioned *before* them; if
  # `-lm` appears before ffmpeg's libavutil.a we get "undefined
  # reference to sqrt". Preserve intra-group order so `-Wl,--as-needed`
  # and its ilk still apply.
  isLFlag = f: builtins.substring 0 2 f == "-l" && builtins.stringLength f > 2;
  lflags     = builtins.filter isLFlag flags;
  nonLflags  = builtins.filter (f: !(isLFlag f)) flags;
  quotedNonL = builtins.concatStringsSep " " (map (f: "'${f}'") nonLflags);
  quotedL    = builtins.concatStringsSep " " (map (f: "'${f}'") lflags);
  # When there are no -l flags, keep the historical layout (no
  # trailing empty slot in the shell command) so pre-existing drv
  # hashes stay stable.
  tailArgs = if lflags == [] then "" else " " + quotedL;

  objArgs = builtins.concatStringsSep " " (map
    (i: "'${i.drv}/${i.name}'")
    inputs);
in
derivation ({
  name = "bin-${outName}";
  system = builtins.currentSystem;

  __contentAddressed = true;
  outputHashMode = "nar";
  outputHashAlgo = "sha256";

  builder = "${bash}/bin/bash";
  args = [
    "-c"
    ''
      set -euo pipefail
      export PATH="${coreutils}/bin:${compiler}/bin"
      mkdir -p "$out"
      "${toolBasename}" ${quotedNonL} ${objArgs}${tailArgs} -o "$out/${outName}"
    ''
  ];

  _storeDeps = builtins.concatStringsSep ":" storeDeps;
} // wrapperEnv)
