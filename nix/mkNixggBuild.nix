# mkNixggBuild — wrap a user build command in a builder-rpc-v0
# derivation whose output is a *derivation file* (.drv). The
# consumer picks up the compiled artifact via
# `builtins.outputOf drv.outPath "out"`.
#
# Built on stdenv.mkDerivation. Stdenv handles buildInputs →
# NIX_LDFLAGS / NIX_CFLAGS_COMPILE / PKG_CONFIG_PATH, setup hooks
# (autoreconfHook, cmake, etc.), propagatedBuildInputs (which pulls
# in a package's transitive dev-time deps automatically), gcc-
# wrapper activation, and much more that we'd otherwise reinvent.
#
# The one deviation: builder-rpc-v0 mode intentionally UNSETS $out
# (the builder is supposed to `submit-output` a store path instead
# of writing to $out). Stdenv assumes $out exists via _assignFirst,
# so we set out="/nonexistent" at the drv level — same trick
# nix-ninja's mkMesonPackage uses. Any accidental write to $out
# fails visibly with "no such directory".
#
# See nixgg/dyn-drv/NOTES.md for the underlying mechanism.
{
  lib,
  stdenv,
  bash,
  coreutils,
  gnumake,
  gcc,
  nixgg,        # store path with bin/nixgg + shims/
  nixHelpers,   # nixgg-nix helper package (unused in sandbox mode,
                # kept for parity with native)
  patchedNix,   # nix with builder-rpc-v0 + submit-output
  system,       # target platform (e.g. "x86_64-linux")
}:

{
  pname,
  version ? "0",
  src,
  # The command that produces the final artifact. Runs during the
  # buildPhase; stdenv's setup has already handled configure hooks,
  # PATH, buildInputs → NIX_LDFLAGS etc.
  buildCommand,
  # Path (relative or absolute) of the final artifact — the shim
  # matches this against `-o <output>` to decide when to submit.
  target,
  # Passed straight through to stdenv.mkDerivation.
  nativeBuildInputs ? [ ],
  buildInputs ? [ ],
  propagatedBuildInputs ? [ ],
}:

let
  targetBase = baseNameOf target;
  # The outer derivation's name must equal the .drv basename the shim
  # submits — Nix enforces submit-output's path-name matches
  # outputPathName(outerName, "out"). Prefix depends on which shim
  # produces the target: `bin-<target>` for link-produced binaries,
  # `ar-<target>` for archive-produced static libs.
  targetPrefix =
    if lib.hasSuffix ".a" targetBase then "ar-"
    else "bin-";
  drvName = "${targetPrefix}${targetBase}.drv";

  drv = stdenv.mkDerivation {
    name = drvName;
    inherit src;
    inherit nativeBuildInputs buildInputs propagatedBuildInputs;

    # builder-rpc-v0 wants $out unset. /nonexistent keeps stdenv's
    # _assignFirst happy while making any actual write visibly fail.
    out = "/nonexistent";

    # We set our own build phase; skip cd-into-src (we do that
    # ourselves after copying to a writable location), install, fixup.
    dontUnpack = false;    # stdenv unpacks src/ into $NIX_BUILD_TOP
    dontConfigure = true;  # user's buildCommand does what it needs
    dontInstall = true;
    dontFixup = true;

    # nix-command + ca + dyn-drv for the inner nix invocations our
    # shims make (nix derivation add / nix store add / submit-output).
    NIX_CONFIG = ''
      extra-experimental-features = nix-command ca-derivations dynamic-derivations
    '';

    requiredSystemFeatures = [ "builder-rpc-v0" ];

    __contentAddressed = true;
    outputHashMode = "text";
    outputHashAlgo = "sha256";

    # NIXGG_* the shims read.
    NIXGG_ROOT           = "${nixgg}";
    NIXGG_COMPILER_ROOT  = "${gcc}";
    NIXGG_BASH_ROOT      = "${bash}";
    NIXGG_COREUTILS_ROOT = "${coreutils}";
    NIXGG_GNUMAKE_ROOT   = "${gnumake}";
    NIXGG_REAL_CC        = "${gcc}/bin/g++";
    NIXGG_NIX            = "${patchedNix}/bin/nix";
    NIXGG_NIX_HELPERS    = "${nixHelpers}";
    NIXGG_STORE          = "auto";
    NIXGG_SANDBOX        = "1";
    NIXGG_SANDBOX_TARGET = target;

    # Prepend nixgg's shims/ before the toolchain so cc/c++/ar
    # dispatch through us. stdenv's setup put its own gcc-wrapper's
    # bin/ on PATH earlier; we go in front.
    #
    # patchedNix's bin/ is what our shims exec for `nix derivation
    # add` etc.
    #
    # Strip stdenv's outer-sandbox noise from the wrapper env: the
    # cc-wrapper's activation hook injects `-frandom-seed=<per-run>`
    # (breaks CA-hash stability) and `-rpath /nonexistent/lib`
    # (points at mkNixggBuild's fake $out). Also drop NIX_HARDENING_ENABLE
    # — inner drvs get their own gcc-wrapper with its own defaults.
    # buildInputs's contribution (-isystem / -L / -rpath into real
    # store paths) stays: those were added AFTER these noise flags
    # by stdenv's setup-hooks, so filtering the noise while keeping
    # per-input flags is the goal.
    preBuild = ''
      export PATH="${nixgg}/bin:${nixgg}/shims:${patchedNix}/bin:$PATH"

      # Scrub stdenv's outer-sandbox noise from the wrapper env before
      # the shims capture it. Rationale:
      #   - NIX_HARDENING_ENABLE / NIX_CC_WRAPPER_TARGET_HOST_<triple>:
      #     outer cc-wrapper markers — inner drvs get their own wrapper
      #     with its own defaults; leaking these makes sandbox-produced
      #     drvs diverge from native (mkShellNoCC) drvs.
      #   - CC/CXX/AR/LD: stdenv defaults them to c++/cc/ar. Under
      #     mkShellNoCC these aren't set, and the caller's Makefile
      #     picks its own default (usually `c++` via `?=`). Unsetting
      #     lets both modes converge.
      #   - -frandom-seed=... and -rpath /nonexistent/lib in
      #     NIX_CFLAGS_COMPILE / NIX_LDFLAGS: baked in by cc-wrapper's
      #     setup-hook off the outer drv's $out=/nonexistent trick.
      #     Per-run and per-path, so poisonous to CA-hash stability.
      unset NIX_HARDENING_ENABLE
      unset CC CXX LD AR RANLIB NM STRIP OBJCOPY OBJDUMP READELF SIZE
      for v in ''${!NIX_CC_WRAPPER_TARGET_HOST_@}; do unset "$v"; done
      NIX_CFLAGS_COMPILE=$(printf '%s' "''${NIX_CFLAGS_COMPILE:-}" | sed -e 's| *-frandom-seed=[^ ]*||g')
      NIX_LDFLAGS=$(printf '%s' "''${NIX_LDFLAGS:-}" | sed -e 's| *-rpath /nonexistent/lib||g')
      export NIX_CFLAGS_COMPILE NIX_LDFLAGS
    '';

    buildPhase = ''
      runHook preBuild
      ${buildCommand}
      runHook postBuild
    '';
  };
in
{
  inherit drv;
  # Consumer entrypoint: `nix build … .result` walks the dyn-drv
  # chain and returns the final compiled artifact.
  result = builtins.outputOf drv.outPath "out";
}
