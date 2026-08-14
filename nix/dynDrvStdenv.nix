# dynDrvStdenv — a stdenv that turns configurePhase+buildPhase into a
# builder-rpc-v0 derivation, with nixgg's shims live on PATH so every
# cc/c++/ar call becomes its own content-addressed Nix derivation
# (exactly like mkNixggBuild, just discovered by walking the resulting
# tree instead of by a single named `target`) — while leaving
# installPhase/fixupPhase/checkPhase/meta of the ORIGINAL package
# completely untouched, in a second, ordinary derivation.
#
# Usage, scoped to one package (does not affect anything else):
#
#   hello = pkgs.hello.override { stdenv = dynDrvStdenv; };
#
# Mechanism: override `mkDerivationFromStdenv`, the hook
# pkgs/stdenv/generic/default.nix documents as "parameterized with the
# final stdenv so we can tie the knot... convenient... so the stdenv
# adapters work better." adapters.nix uses the same seam nine times
# (propagateBuildInputs, addAttrsToDerivation, traceDrvLicenses, ...)
# via a private `withOldMkDerivation` helper; reimplemented here since
# that helper isn't exported.
#
# Why NOT just overrideCC (ccacheStdenv's approach): ccache only needs to
# swap the compiler BINARY — every phase still runs as stdenv wrote it.
# nixgg needs to swap what BUILDS the derivation itself
# (requiredSystemFeatures, __contentAddressed, phase boundaries), which
# is a property of the whole derivation, not of one input package.
#
# Why phase 1 is builder-rpc-v0 and not plain "call nix build at the end"
# (nixgg's native mode): a normal sandboxed stdenv build cannot invoke
# `nix build`/`nix-instantiate` at all — verified directly, twice.
# Against an alt-store: the nested build's OWN sandbox doesn't inherit
# the outer build's bind-mounts, so even a referenced input visible to
# the outer shell ("ls" succeeds) is invisible to the inner build
# ("No such file or directory"). Against the ambient store directly:
# "error: creating directory '/nix/var/nix/profiles': Permission
# denied" — a normal build sandbox is read-only for the Nix store,
# full stop. Only the narrow builder-rpc-v0 RPC allowlist
# (`nix derivation add`, `nix store add`, `nix store submit-output`)
# is reachable from inside a sandboxed build; this is exactly why
# mkNixggBuild.nix already commits to it instead of native mode.
#
# Why checkPhase moved to phase 2 (not run in phase 1 alongside
# configure/build, the way it does in a plain stdenv build): with
# shims live, every artifact the build just produced — including the
# thing a test suite would execute — is a drvref STUB (a small text
# file recording which drv will produce the real content; see
# internal/drvref), not yet resolved. A checkPhase that runs `ctest`
# or execs the just-linked binary would run it against that stub, not
# a real ELF. `nixgg assemble` (postBuild, below) resolves every stub
# into phase 1's single "out" output; phase 2 restores that ALREADY
# RESOLVED tree before its own checkPhase runs, so `doCheck = true`
# packages (zstd runs `ctest` there) see real binaries.
{
  lib,
  patchedNix,
  nixgg, # nixggBin: $out/bin/nixgg + $out/shims/{cc,c++,ar,...}
  bash,
  coreutils,
  gcc,
  gnumake,
  system, # target platform (e.g. "x86_64-linux") for the JSON drv
          # `nixgg assemble` builds. NOT builtins.currentSystem: that's
          # an impure builtin, and every OTHER attr in this file's
          # derivation is a pure Nix value known at eval time — using
          # it here would make `nix build` (no --impure) fail eval-time
          # with "attribute 'currentSystem' missing", exactly the way a
          # real flake package consumer invokes this. Confirmed
          # directly: building .#hello-dyndrv through the flake (a
          # plain `nix build`, no --impure) failed with precisely that
          # error before this parameter existed. mkNixggBuild.nix
          # already takes `system` the same way, for the same reason.
  nixpkgsPath, # store path of the nixpkgs tree stdenv0 came from, so
               # the private mkDerivationFromStdenv fallback (needed
               # only when the caller's stdenv never set an override —
               # true for a fresh pkgs.stdenv) reconstructs from the
               # SAME make-derivation.nix rather than guessing <nixpkgs>.
  config, # the REAL nixpkgs config (pkgs.config) — make-derivation.nix
          # reads config.doCheckByDefault / .structuredAttrsByDefault /
          # .stdenv.userHook etc.; {} silently gives wrong defaults
          # rather than erroring, which is not obviously the cause of
          # anything if you only see it fail three frames away.
}:

{
  stdenv, # the base stdenv to wrap — e.g. pkgs.stdenv.
  extraPhase1Attrs ? (finalAttrs: old: old), # escape hatch: a
    # finalAttrs: old-attrs: new-attrs function, composed into phase
    # 1's derivation attrs AFTER argsOrFn's own attrs but BEFORE the
    # dontInstall/outputs/__contentAddressed/etc. overrides below —
    # see this file's top comment ("Why .overrideAttrs can't reach
    # phase 1") for why this exists: no nixpkgs-level .override or
    # .overrideAttrs applied by the CALLER can ever patch phase 1's
    # derivation attrs, because phase 1 is computed and closed over
    # inside the package function's own re-invocation, before any
    # .overrideAttrs the caller wrote gets a chance to run. This
    # parameter is the only way to reach phase 1 from outside — pass
    # it directly at the `dynDrvStdenv { ...; }  pkgs.stdenv` call
    # site, e.g.:
    #
    #   pkgs.foo.override {
    #     stdenv = dynDrvStdenv {
    #       extraPhase1Attrs = finalAttrs: old: old // {
    #         postPatch = (old.postPatch or "") + "sed -i ... Makefile\n";
    #       };
    #     } pkgs.stdenv;
    #   }
    #
    # `old` here is phase 1's own attrset as dynDrvStdenv built it —
    # NOT the original package's raw attrs — so appending to
    # `old.postPatch` (as above) preserves whatever the ORIGINAL
    # package.nix already put there, exactly like a normal
    # `.overrideAttrs (old: {...})` body would, just reaching a
    # derivation .overrideAttrs itself cannot touch.
  extraPhase2Attrs ? (finalAttrs: old: old), # same shape, for phase 2
    # (install/fixup/installCheck/dist). Phase 2 DOES receive ordinary
    # .overrideAttrs calls correctly (confirmed directly — only phase
    # 1 has the problem), so this is here for symmetry/discoverability
    # more than necessity; prefer plain .overrideAttrs on the returned
    # package for phase-2-only tweaks.
}:

let
  stdenv0 = stdenv;

  # NOTE: make-derivation.nix's calling convention is nixpkgs-revision-
  # dependent — this pinned nixpkgs curries `lib: config: stdenv: {...}`
  # (three separate positional args), not the older `{lib, config}:
  # stdenv: {...}` shape. Verified directly: importing it and applying
  # an attrset `{inherit lib; config = {};}` as the first argument
  # silently binds that whole attrset to `lib`, and every subsequent
  # application lands one argument short — the symptom was "expected a
  # set but found a function" deep inside make-derivation.nix's OWN
  # internals (buildPlatform.system), nowhere near the real mistake.
  # Call it the way it actually wants to be called for THIS pin.
  defaultMkDerivationFromStdenv =
    stdenv:
    (import "${nixpkgsPath}/pkgs/stdenv/generic/make-derivation.nix" lib config stdenv).mkDerivation;

  # Shims must land on PATH AFTER cc-wrapper's own setup-hook prepend —
  # same ordering mkNixggBuild.nix already depends on. NIXGG_* vars
  # match mkNixggBuild.nix's toolchainEnv exactly (that file is the
  # single source of truth for what the shims need; drift between the
  # two would be exactly the "kept identical by discipline" trap that
  # already produced one real bug this session).
  #
  # NIXGG_SANDBOX_TARGET is set to a basename no real build output can
  # match (see shim/link.go's matchesTarget: exact path, abs path, or
  # basename equality) rather than left unset. link.go's maybeSubmit
  # defaults to submitting whatever it links as the outer drv's "out"
  # when NIXGG_SANDBOX_TARGET is empty — correct for mkNixggBuild.nix's
  # single-target builds, wrong here: `nixgg assemble` (postBuild,
  # below) owns the ONE "out" submission for the whole tree. Leaving
  # TARGET unset made a package's own final link self-submit, and
  # assemble's later submit-output then failed with "Attempted to
  # submit duplicate output 'out'" — confirmed directly while building
  # this the first time.
  #
  # Takes knownStorePathsJSON as a PARAMETER rather than computing it
  # once at the top of this file (mkNixggBuild.nix's toolchainEnv is a
  # plain string precisely because IT is given explicit buildInputs):
  # dynDrvStdenv wraps an arbitrary EXISTING package, so the set of
  # real store paths the shim needs to recognize (mosh's protobuf,
  # ncurses, zlib, openssl, ...) is different for every package and
  # only known from probeArgs.buildInputs/propagatedBuildInputs, read
  # per-call inside withPhase1Attrs below.
  ggShimsOnPath = knownStorePathsJSON: ''
    export PATH="${nixgg}/shims:${patchedNix}/bin:$PATH"
    export NIXGG_ROOT="${nixgg}"
    export NIXGG_COMPILER_ROOT="${gcc}"
    export NIXGG_BASH_ROOT="${bash}"
    export NIXGG_COREUTILS_ROOT="${coreutils}"
    export NIXGG_GNUMAKE_ROOT="${gnumake}"
    export NIXGG_REAL_CC="${gcc}/bin/g++"
    export NIXGG_NIX="${patchedNix}/bin/nix"
    export NIXGG_NIX_HELPERS="${nixgg}"
    export NIXGG_SANDBOX=1
    export NIXGG_STORE="auto"
    export NIXGG_SYSTEM="${system}"
    export NIXGG_SANDBOX_TARGET="/nonexistent/nixgg-phase1-no-per-artifact-submit"
    export NIXGG_KNOWN_STORE_PATHS=${lib.escapeShellArg knownStorePathsJSON}
    export NIX_CONFIG="extra-experimental-features = nix-command ca-derivations dynamic-derivations"
  '';

  # postBuild: record $PWD's offset relative to $NIX_BUILD_TOP (phase2
  # needs to `cd` back there before `make install` — cmake's own
  # `mkdir build && cd build` convention means $PWD at this point is
  # NOT $NIX_BUILD_TOP, and autotools' baked absolute-prefix Makefile
  # rules must be invoked from the same directory they were generated
  # in), then walk $NIX_BUILD_TOP for every drvref stub the shims left
  # (one per intercepted cc/c++/ar call), build one assembly drv that
  # restores the tree and resolves each stub against its real,
  # dyn-drv-resolved artifact, and submit it as this derivation's "out"
  # — see go/internal/cli/assemble.go and go/internal/assemble/ for the
  # actual walk/build logic. Walks $NIX_BUILD_TOP (not $PWD): the WHOLE
  # tree needs capturing regardless of which subdirectory the build
  # itself is working in, for the same cmake_check_build_system /
  # absolute-source-dir reason recorded in this file's git history.
  submitBuildTreeScript = drvName: ''
    realpath --relative-to="$NIX_BUILD_TOP" "$PWD" > "$NIX_BUILD_TOP/.gg-cwd"
    ${nixgg}/bin/nixgg assemble "$NIX_BUILD_TOP" "${drvName}"
  '';
in

stdenv0.override (
  old:
  {
    mkDerivationFromStdenv =
      stdenvSelf:
      let
        mkDerivationSuper = (old.mkDerivationFromStdenv or defaultMkDerivationFromStdenv) stdenvSelf;
      in
      argsOrFn:
      let
        # `argsOrFn` may be a plain attrset OR a `finalAttrs: {...}`
        # function (nixpkgs' modern package style — hello's real
        # package.nix is exactly this, using finalAttrs.version and
        # finalAttrs.meta.mainProgram). makeDerivationExtensible
        # resolves that via a genuine fixed point: `args = rattrs (args
        # // { finalPackage; overrideAttrs; })` — args refers to
        # itself. Precompute a plain attrset here (as the first version
        # of this file did, via `lib.toFunction argsOrFn {}`) and every
        # finalAttrs.* reference inside the package sees an EMPTY
        # attrset instead of the real fixed point — hello's own
        # postInstallCheck reads finalAttrs.meta.mainProgram and dies
        # with "attribute 'meta' missing", three frames from the actual
        # mistake.
        #
        # So: never collapse argsOrFn to a plain set. Read pname/version
        # by invoking the function once with a THROWAWAY {} (safe only
        # because they're static string literals in every real-world
        # package, never finalAttrs-dependent themselves — verified
        # against hello's package.nix) purely to name phase1's
        # derivation; then build phase1 and phase2 by wrapping argsOrFn
        # in ANOTHER finalAttrs-function layer that merges in overrides,
        # exactly the shape `overrideAttrs`'s own `extends'` uses. The
        # real fixed point — finalPackage, overrideAttrs, meta, passthru
        # — is left entirely to mkDerivationSuper.
        probeArgs = lib.toFunction argsOrFn { };
        drvName = if probeArgs ? name then probeArgs.name else "${probeArgs.pname}-${probeArgs.version}";
        outerName = "gg-build-${drvName}";

        # The store paths the shim's storedeps matcher needs to
        # recognize in -I/-L flags and NIX_CFLAGS_COMPILE/NIX_LDFLAGS
        # text — same computation mkNixggBuild.nix's knownStorePathInputs
        # does, just sourced from the WRAPPED PACKAGE's own
        # buildInputs/propagatedBuildInputs instead of an explicit
        # param (dynDrvStdenv has no such param — it wraps whatever
        # package.nix already declared). Missing this meant mosh's
        # `#include <zlib.h>` compiled fine in a real (unshimmed)
        # build, where cc-wrapper's own -isystem flag just works, but
        # failed inside the shim's sandbox drv with "zlib.h: No such
        # file or directory": storedeps.From had no known-store-paths
        # manifest to match zlib's -I flag against, so it never made
        # it into inputs.srcs for the compile drv — confirmed directly.
        knownStorePathsJSON = builtins.toJSON (
          map toString (
            builtins.concatMap (p: p.all or [ p ]) (
              (probeArgs.buildInputs or [ ]) ++ (probeArgs.propagatedBuildInputs or [ ])
              ++ [ bash coreutils gcc nixgg patchedNix ]
            )
          )
        );

        withPhase1Attrs =
          finalAttrs:
          let
            base =
              (lib.toFunction argsOrFn finalAttrs)
              // {
                name = "${outerName}.drv"; # submit-output requires this to
                                            # match outputPathName(outerName, "out")
                # Deliberately NOT setting `phases` here. genericBuild's own
                # default construction (setup.sh) splices in whatever
                # pre*Phases setup hooks append at RUNTIME — autoreconfHook's
                # setup hook does `appendToVar preConfigurePhases
                # autoreconfPhase`, cmake's does the equivalent for its own
                # phase. Those arrays don't exist yet at Nix eval time (this
                # attrset), only inside the build shell after setup hooks
                # have sourced — so a hand-written `phases` string here can
                # only ever hardcode the phases every package always has,
                # silently dropping any hook-injected one. Confirmed
                # directly against mosh (autoreconfHook): a hardcoded
                # "unpackPhase patchPhase configurePhase buildPhase" skipped
                # autoreconfPhase entirely, so configurePhase found no
                # configure script ("no configure script, doing nothing"),
                # buildPhase found no Makefile, and the failure only
                # surfaced two phases later in phase2's installPhase as a
                # generic "cannot stat .../nonexistent/." — nowhere near the
                # actual missing phase.
                #
                # Instead, stop phase1 after buildPhase using the SAME
                # dont*/do* toggles setup.sh's own runPhase already checks
                # (see runPhase's guards: dontInstall, dontFixup,
                # doInstallCheck, doDist) — this way every hook-injected
                # phase before buildPhase still runs in the real order,
                # whatever that order turns out to be for this package.
                # doCheck is forced off HERE too (checkPhase moves to
                # phase2 — see this file's top comment for why: a
                # checkPhase that execs the just-built binary would run it
                # against an unresolved drvref stub, not a real ELF, if it
                # ran here before assemble resolves anything).
                doCheck = false;
                dontInstall = true;
                dontFixup = true;
                doInstallCheck = false;
                doDist = false;
                # Phase1 only ever produces ONE tree (no install step splits
                # it by output) — force single-output regardless of what
                # the real package declares. Needed for multi-output
                # packages like fmt (outputs = ["out" "dev"]): inheriting
                # that via the // merge below made phase1 itself carry two
                # outputs, and Nix rejects a two-output derivation named
                # "*.drv" ("derivation names are allowed to end in '.drv'
                # only if they produce a single derivation file") —
                # confirmed directly, this is what building fmt hit before
                # this line was added. Phase2 keeps the package's real
                # `outputs` (via the unmodified `// {}` merge there), so
                # output-splitting setup hooks (moveToOutput etc.) still run
                # correctly against phase2's own fixupPhase.
                outputs = [ "out" ];
                out = "/nonexistent";
                # Force off regardless of what the wrapped package requests
                # (hello's own package.nix sets __structuredAttrs = true).
                # Under structured attrs, make-derivation.nix only turns
                # attrs nested inside `env = {...}` into real derivation
                # env vars / $out overrides — bare top-level attrs like the
                # `out = "/nonexistent"` above are silently NOT honored.
                # Confirmed directly: with __structuredAttrs inherited as
                # true, phase1's configurePhase baked its own real
                # auto-assigned store path into the Makefile's install
                # prefix instead of /nonexistent, and phase2 (built from
                # phase1's copied tree) then tried installing files at
                # THAT path instead of its own $out — "hello was not
                # found, or is not an executable" was the resulting
                # symptom, one more layer removed from the actual cause.
                __structuredAttrs = false;
                requiredSystemFeatures = (probeArgs.requiredSystemFeatures or [ ]) ++ [ "builder-rpc-v0" ];
                __contentAddressed = true;
                outputHashMode = "text";
                outputHashAlgo = "sha256";
                nativeBuildInputs = (probeArgs.nativeBuildInputs or [ ]) ++ [ patchedNix ];

                # Bypass bracket: NIXGG_BYPASS gates ACCELERATION per shim
                # invocation (bypassed() short-circuits to a real passthrough
                # exec — see go/internal/shim/passthrough.go), not whether
                # the shims are physically on PATH. Both postPatch AND
                # preConfigure put the shims on PATH: cmake's OWN
                # configurePhase resolves CMAKE_C_COMPILER/CMAKE_CXX_COMPILER
                # to whatever absolute path is on PATH at configure time and
                # bakes that ABSOLUTE PATH into every subsequent Makefile
                # rule — unlike autotools' bare `CC = gcc`, re-resolved via
                # $PATH at build time, cmake's baked path is never
                # re-resolved. Putting the shims on PATH only in preBuild (a
                # version of this file's first working attempt) meant cmake
                # baked the REAL gcc-wrapper path at configure time, and
                # buildPhase's own generated Makefile invoked that real path
                # directly — nothing ever routed through the shim, and
                # `nixgg assemble` later found ZERO stubs even though the
                # build succeeded. Confirmed directly building zstd (cmake):
                # 0 stubs, real unaccelerated compiles throughout, no error
                # at all — the exact "builds clean but isn't accelerated"
                # failure mode that's easy to miss because nothing looks
                # broken. Putting shims on PATH from postPatch, gated
                # per-call by NIXGG_BYPASS instead, means cmake bakes the
                # SHIM's own path, and once NIXGG_BYPASS is unset in
                # preBuild those same baked invocations start hitting the
                # (now-unbypassed) shim — no cmake-specific handling needed.
                #
                # NIXGG_BYPASS itself must still cover autoreconfHook, cmake's
                # configurePhase, and any other configure-time probe (it
                # injects itself via preConfigurePhases, which runs BEFORE
                # configurePhase — confirmed by reading setup.sh's phase
                # list), so it's set at postPatch, not preConfigure.
                # Configure-time compiler probes (autoconf's AC_CHECK_HEADER
                # etc.) run real gcc invocations to test feature
                # availability, not to produce a project artifact — routing
                # THOSE through unbypassed shim logic would misclassify probe
                # output as a real build target. Real per-TU acceleration
                # only needs to start at buildPhase, hence unset there, not
                # earlier.
                postPatch = (probeArgs.postPatch or "") + ''
                  export NIXGG_BYPASS=1
                  ${ggShimsOnPath knownStorePathsJSON}
                '';
                preBuild = ''
                  unset NIXGG_BYPASS
                '' + (probeArgs.preBuild or "");

                postBuild = (probeArgs.postBuild or "") + submitBuildTreeScript outerName;
              };
          in
          # extraPhase1Attrs runs LAST, composed over dynDrvStdenv's own
          # base attrs — a caller's fix sees (and can build on top of)
          # every override above, exactly the way a normal
          # `.overrideAttrs (old: {...})` body sees the base package's
          # attrs. See this parameter's own docstring at the top of the
          # file for why it exists at all (nixpkgs' .overrideAttrs
          # cannot reach phase 1 by itself).
          extraPhase1Attrs finalAttrs base;

        # Phase 1: unpack, patch, configure, build. Real nixpkgs phase
        # functions, real dependency resolution, real setup hooks
        # (autoreconfHook, cmake, ...) — only the compiler on PATH and
        # the phase list are different. This is an internal
        # implementation detail nobody overrides from outside, so it
        # does not need its own finalPackage/overrideAttrs wiring —
        # mkDerivationSuper still provides them, they are just never
        # consumed.
        phase1 = mkDerivationSuper withPhase1Attrs;

        builtTree = builtins.outputOf phase1.outPath "out";
      in
      # Phase 2: install, fixup, installCheck, dist. The REAL package's
      # unmodified installPhase/fixupPhase/checkPhase/meta — seeded from
      # phase1's tree instead of a fresh unpack. Wrapping argsOrFn the
      # same way (rather than realArgs // {...}) means finalAttrs.meta,
      # finalAttrs.version etc. still resolve correctly for THIS
      # derivation — the one actually returned to the caller, where it
      # matters most (hello's postInstallCheck runs here).
      #
      # DESTDIR staging: phase1's real configurePhase baked ITS OWN
      # $out (deliberately the literal "/nonexistent", see withPhase1Attrs)
      # into the generated Makefile's prefix/bindir/etc — autotools
      # substitutes --prefix at configure time, not install time. Since
      # phase2 skips configure (reusing phase1's tree verbatim) but has
      # a DIFFERENT real $out, a plain `make install` here would write
      # to /nonexistent again. Confirmed directly: without DESTDIR,
      # install logs showed "installing ... as /nonexistent/share/...",
      # and the outer build failed at installCheckPhase with "hello was
      # not found" — the files were real, just at the wrong (phase1's)
      # path instead of this derivation's own $out.
      #
      # DESTDIR is the standard escape hatch for exactly this "configure
      # once, install to a different root" scenario (autotools and
      # CMake both honor it; setup.sh's own generic installPhase passes
      # installFlags straight to `make install` with no DESTDIR of its
      # own). Since the baked prefix is always the literal /nonexistent
      # for every package under this stdenv, relocating
      # "$stagedir/nonexistent/." into "$out/" is deterministic
      # regardless of which package this is.
      #
      # DESTDIR is exported in preInstall (a real bash var), NOT baked
      # into installFlags as a literal "DESTDIR=$NIX_BUILD_TOP/..."
      # string: installFlags becomes a `make install <flags>` ARGUMENT,
      # where GNU make parses $N as ITS OWN single-letter variable
      # reference and silently swallows the following character —
      # "$NIX_BUILD_TOP" became the literal path "IX_BUILD_TOP/...".
      # Confirmed directly: that exact mangled path showed up in the
      # install log, and the postInstall cp then failed with "cannot
      # stat '/build/.gg-destdir/nonexistent/.': No such file or
      # directory" (the real files were one path segment away, under
      # the correctly-bash-expanded $NIX_BUILD_TOP). A bare `DESTDIR`
      # token in installFlags (env-style, no `=`) would dodge make's
      # parsing but isn't valid make syntax either; exporting the
      # variable is simpler and avoids relying on make/env quoting
      # rules at all.
      #
      # ggRestorePhase (below) restores the WHOLE captured
      # $NIX_BUILD_TOP tree (not just the cmake-style build subdir)
      # directly into THIS derivation's own $NIX_BUILD_TOP, then cds to
      # the same relative offset recorded by submitBuildTreeScript's
      # .gg-cwd marker — see that script's own comment for why (cmake's
      # baked absolute source-dir path). dontUnpack replaces the plain
      # `cp -a ${builtTree}/. .` this had before multi-output/cmake
      # testing.
      #
      # checkPhase runs BEFORE installPhase (matching setup.sh's own
      # default order: "buildPhase checkPhase ... installPhase"), and
      # with the package's REAL doCheck (never forced off here — only
      # phase1 forces its own off unconditionally, see
      # withPhase1Attrs) — see this file's top comment for why
      # checkPhase moved here at all: by the time ggRestorePhase below
      # has run, $NIX_BUILD_TOP holds fully resolved artifacts (nixgg
      # assemble already substituted every stub), so a checkPhase that
      # execs a freshly linked binary (zstd's own checkPhase runs
      # `ctest` against exactly that) sees a real ELF, not a stub text
      # file.
      #
      # The tree-restore is its OWN custom phase (ggRestorePhase, ahead
      # of checkPhase in the list) rather than a preCheck/preInstall
      # hook: those hooks are attached to checkPhase/installPhase,
      # which runPhase's own guard SKIPS ENTIRELY when doCheck/
      # dontInstall says so (see setup.sh's runPhase: "if curPhase =
      # checkPhase && -z doCheck; then return"). doCheck defaults to
      # false for most packages, so a preCheck-only restore would
      # simply never run for them — installPhase would then find
      # nothing to install. A custom phase name has no such gate;
      # setup.sh's runPhase only special-cases the six well-known
      # phase names, so "ggRestorePhase" always runs regardless of
      # doCheck/dontInstall.
      mkDerivationSuper (
        finalAttrs:
        let
          base =
            (lib.toFunction argsOrFn finalAttrs)
            // {
              phases = "ggRestorePhase checkPhase installPhase fixupPhase installCheckPhase distPhase";
              dontUnpack = true;
              # See withPhase1Attrs's __structuredAttrs comment — same
              # reasoning applies to installFlags/postInstall below, which
              # are plain top-level attrs here too.
              __structuredAttrs = false;
              ggRestorePhase = ''
                runHook preGgRestore
                cp -a ${builtTree}/. "$NIX_BUILD_TOP/"
                chmod -R u+w "$NIX_BUILD_TOP"
                cd "$NIX_BUILD_TOP/$(cat "$NIX_BUILD_TOP/.gg-cwd")"
                export DESTDIR="$NIX_BUILD_TOP/.gg-destdir"
                runHook postGgRestore
              '';
              installFlags = (probeArgs.installFlags or "");
              postInstall = ''
                mkdir -p "$out"
                cp -a "$DESTDIR/nonexistent/." "$out/"
              '' + (probeArgs.postInstall or "");
            };
        in
        # extraPhase2Attrs — see extraPhase1Attrs's docstring; unlike
        # phase1, phase2 IS reachable via a plain .overrideAttrs on the
        # returned package (confirmed directly), so this exists mostly
        # for symmetry with extraPhase1Attrs rather than necessity.
        extraPhase2Attrs finalAttrs base
      );
  }
)
