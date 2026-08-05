# Per-TU CA derivation.
#
# All toolchain paths are passed in as already-realised store paths, so
# this file does NOT depend on <nixpkgs> and does no fetching — that
# keeps parallel invocations from racing on nixpkgs input evaluation.
#
# This helper no longer assembles the compile command. The Go driver
# emits the full shell body (internal/expr Derivation.scriptTemplate)
# with @NIXGG_*@ markers standing in for the store paths that are this
# file's arguments, and all we do is substitute them. Sandbox mode bakes
# the same body — from the same Go code — straight into its JSON drv, so
# the two modes cannot drift in flag quoting or argument order the way
# they did when both sides re-derived the command.
{
  # Toolchain roots — default to the pinned versions in toolchain.nix.
  # Callers can override individually if they need to (unusual).
  compilerRoot  ? (import ./toolchain.nix).compilerRoot,
  bashRoot      ? (import ./toolchain.nix).bashRoot,
  coreutilsRoot ? (import ./toolchain.nix).coreutilsRoot,
  srcTree,                 # path expression → Nix imports the tree at eval time
  source,                  # relative path inside srcTree, e.g. "main.cc"
  outName,                 # object filename, e.g. "main.o"
  scriptTemplate,          # bash body from Go, with @<tag>_*@ markers
  markerTag,               # the tag those markers use; see resolve-script.nix
  storeDepsJSON ? "[]",    # JSON string list of extra /nix/store/... deps
                           # (e.g. protobuf/include roots) that the flags
                           # or wrapper env vars reference and must be
                           # present in the sandbox.
  wrapperEnvJSON ? "{}",   # JSON object of NIX_CFLAGS_COMPILE etc. that
                           # the Nix gcc-wrapper consults.
}:
let
  pureStorePath = import ./pure-store-path.nix;
  bash        = pureStorePath bashRoot;
  coreutils   = pureStorePath coreutilsRoot;
  compiler    = pureStorePath compilerRoot;
  # srcTree comes in as a path expression (a `../srcs/<tu-id>` literal
  # emitted by the driver). Nix imports the referenced directory at
  # eval time and interpolates to `/nix/store/<hash>-<basename>` here.
  src         = srcTree;
  storeDeps   = map pureStorePath (builtins.fromJSON storeDepsJSON);
  wrapperEnv  = builtins.fromJSON wrapperEnvJSON;
  script      = import ./resolve-script.nix {
    inherit scriptTemplate markerTag coreutils compiler;
    inputs = [ ];
  };
in
derivation ({
  name = "tu-${outName}";
  system = builtins.currentSystem;

  __contentAddressed = true;
  outputHashMode = "nar";
  outputHashAlgo = "sha256";

  builder = "${bash}/bin/bash";
  args = [ "-c" script ];

  inherit src source outName;
  _storeDeps = builtins.concatStringsSep ":" storeDeps;
} // wrapperEnv)
