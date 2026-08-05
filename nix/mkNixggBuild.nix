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
  mkShell,
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

  # Shell prelude shared by the sandbox build (`preBuild`) and the
  # native-replay dev shell (`shellHook`).
  #
  # These two MUST produce identical NIX_CFLAGS_COMPILE / NIX_LDFLAGS.
  # `preBuild` is what the shims capture in sandbox mode; `shellHook` is
  # what they capture when tests/drv-equivalence.sh replays the same
  # build natively under `nix develop`. Any difference between them
  # shows up as a drv-hash divergence between the two modes — i.e. it
  # breaks the invariant the whole test suite exists to protect.
  #
  # It used to be two hand-synced copies of this text. They happened to
  # agree, but a one-sided edit would only ever be caught by the slow
  # integration test (which needs a builder-rpc-v0 nix and a remote
  # builder) — the same "kept identical by discipline" trap that
  # produced a real quoting bug between internal/expr's shellQuoteFlags
  # and nix/{builder,linker}.nix. One source, interpolated twice.
  #
  # Rationale for each scrub:
  #   - NIX_HARDENING_ENABLE: outer cc-wrapper marker. Inner drvs get
  #     their own wrapper with its own defaults; leaking this makes
  #     sandbox-produced drvs diverge from native (mkShellNoCC) ones.
  #   - CC/CXX/AR/LD/...: stdenv defaults them to cc/c++/ar. Under
  #     mkShellNoCC they aren't set, so the caller's Makefile picks its
  #     own default (usually `c++` via `?=`). Unsetting lets both modes
  #     converge on the same tool name.
  #   - -frandom-seed=... : per-invocation, so poisonous to CA-hash
  #     stability.
  #   - -rpath <...>/outputs/out/lib and -rpath /nonexistent/lib:
  #     bintools-wrapper always injects one off the derivation's $out,
  #     which is /nonexistent in the sandbox (see the out= trick below)
  #     and <workdir>/outputs/out under `nix develop`. Per-path, and not
  #     real linker information for a build whose actual output is a
  #     submitted drv.
  #
  # NOT scrubbed: NIX_CC_WRAPPER_TARGET_HOST_<triple>. Bypass-mode
  # configure steps exec-passthrough to the outer gcc-wrapper, which
  # needs that trigger to inject buildInputs' -isystem / -L. wrapperenv
  # gates propagation into the inner drv on non-empty flags, so
  # empty-buildInputs builds still hash identically to native.
  scrubWrapperEnv = ''
    export PATH="${nixgg}/bin:${nixgg}/shims:${patchedNix}/bin:$PATH"
    unset NIX_HARDENING_ENABLE
    unset CC CXX LD AR RANLIB NM STRIP OBJCOPY OBJDUMP READELF SIZE
    NIX_CFLAGS_COMPILE=$(printf '%s' "''${NIX_CFLAGS_COMPILE:-}" | sed -e 's| *-frandom-seed=[^ ]*||g')
    NIX_LDFLAGS=$(printf '%s' "''${NIX_LDFLAGS:-}" | sed -e 's| *-rpath [^ ]*/outputs/out/lib||g' -e 's| *-rpath /nonexistent/lib||g')
    export NIX_CFLAGS_COMPILE NIX_LDFLAGS
  '';

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

    # Populates $NIX_BUILD_CORES (used by build scripts via
    # `make -j"$NIX_BUILD_CORES"`). Actual per-drv build parallelism
    # still happens in Nix's outer pass — this only speeds up the
    # shim submission phase.
    enableParallelBuilding = true;

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

    # See scrubWrapperEnv above for what this does and why. Two points
    # specific to the sandbox side: the shims/ dir goes ahead of the
    # toolchain stdenv already put on PATH so cc/c++/ar dispatch through
    # us, and patchedNix's bin/ is what those shims exec for `nix
    # derivation add`. buildInputs' own contribution (-isystem / -L /
    # -rpath into real store paths) survives the scrub: stdenv's
    # setup-hooks add those AFTER the per-run noise, so filtering the
    # noise keeps the per-input flags.
    preBuild = scrubWrapperEnv;

    buildPhase = ''
      runHook preBuild
      ${buildCommand}
      runHook postBuild
    '';
  };

  # Devshell that mirrors `drv`'s stdenv env (same buildInputs +
  # setup-hooks + NIX_CFLAGS_COMPILE/NIX_LDFLAGS/PKG_CONFIG_PATH),
  # but as a plain mkShell so `nix develop` can enter it (a
  # text-hashed dyn-drv can't be an env-target). Used by
  # tests/drv-equivalence.sh to run the same buildCommand natively
  # under the same tool env and verify inner drvs hash-match.
  #
  # We replay preBuild's scrubbing in shellHook so what the caller
  # inherits matches what the sandbox's shims capture. Anything after
  # here (make, cmake, autogen.sh) is up to the caller.
  # Plain mkShell (not NoCC): mirrors the outer drv's stdenv env
  # including cc-wrapper's activation trigger. Bypass-mode configure
  # steps (autoconf, cmake probes) exec-passthrough to the outer
  # cc-wrapper, which needs the trigger to inject buildInputs
  # -isystem / -L. The setup-hook's -rpath <outputs>/out/lib and
  # -frandom-seed noise gets scrubbed below.
  shell = mkShell {
    name = "${drvName}-shell";
    inherit nativeBuildInputs buildInputs propagatedBuildInputs;
    # The exact command string the sandbox runs, passed through so
    # tests/drv-equivalence.sh can replay it natively without
    # duplicating build recipes. Consumers pull it out with
    # `nix eval --raw .#<attr>-shell.passthru.buildCommand`.
    passthru.buildCommand = buildCommand;
    shellHook = scrubWrapperEnv + ''
      export NIXGG_ROOT="${nixgg}"
      export NIXGG_COMPILER_ROOT="${gcc}"
      export NIXGG_BASH_ROOT="${bash}"
      export NIXGG_COREUTILS_ROOT="${coreutils}"
      export NIXGG_GNUMAKE_ROOT="${gnumake}"
      export NIXGG_REAL_CC="${gcc}/bin/g++"
      export NIXGG_NIX="${patchedNix}/bin/nix"
      export NIXGG_NIX_HELPERS="${nixHelpers}"
    '';
  };
in
{
  inherit drv shell;
  # Consumer entrypoint: `nix build … .result` walks the dyn-drv
  # chain and returns the final compiled artifact.
  result = builtins.outputOf drv.outPath "out";
}
