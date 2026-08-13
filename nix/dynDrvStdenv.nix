# dynDrvStdenv — a stdenv that turns configurePhase+buildPhase into a
# builder-rpc-v0 derivation while leaving installPhase/fixupPhase/
# checkPhase/meta of the ORIGINAL package completely untouched, in a
# second, ordinary derivation.
#
# CURRENT SCOPE: phase 1 runs as a real, unaccelerated native build
# (NIXGG_BYPASS stays set the whole time) — it proves the phase-split
# and submit-buildtree plumbing (real configurePhase, real autotools,
# real make, captured whole and handed to phase 2 as one drv) without
# yet routing individual cc/c++/ar calls through nixgg's shims. Per-TU
# acceleration needs stub resolution inside the sandbox first — see the
# postPatch/preBuild comments below for exactly what's missing and why.
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
{
  lib,
  patchedNix,
  bash,
  coreutils,
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

stdenv0:

let
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

  # Register the just-built tree as a real store path (nested drvs can
  # only reference store paths, never the ephemeral /build tree — see
  # sandbox.go's StoreAddScan, the same operation used here), then
  # register+submit a JSON drv that just copies it out. This is a
  # placeholder for nixgg's real per-TU JSON-drv path: it proves the
  # PHASE-1-TO-OUTER-OUTPUT plumbing works, without yet routing
  # individual cc/ar calls through it. Real acceleration needs
  # generalizing nixgg's existing single-target shim path to "whatever
  # buildPhase left behind" — separate work from this stdenv adapter.
  #
  # Captures the WHOLE $NIX_BUILD_TOP tree, not just $PWD — and records
  # $PWD's path relative to $NIX_BUILD_TOP in a marker file at the
  # tree's root, so phase2 can cd back to the exact same relative
  # offset. Needed for build systems that bake ABSOLUTE paths into
  # generated build files: cmake's Makefile has a `cmake_check_build_system`
  # prerequisite (run before `install` and other targets) that
  # re-invokes cmake against the ORIGINAL absolute source directory.
  # Confirmed directly against fmt (cmake): capturing only $PWD (the
  # cmake build dir, e.g. $NIX_BUILD_TOP/source/build) and restoring it
  # flattened into phase2's OWN $NIX_BUILD_TOP root dropped the sibling
  # source/ directory those absolute paths pointed at — `make install`
  # failed with "CMake Error: The source directory '/build/source' does
  # not exist." $NIX_BUILD_TOP itself (conventionally "/build") is the
  # same fixed sandbox path across every derivation, so replicating the
  # same relative offset under phase2's own $NIX_BUILD_TOP makes cmake's
  # baked absolute paths resolve identically — no cmake-specific
  # handling needed, this fixes the general "any build system that
  # embeds absolute build-tree paths" case.
  submitBuildTreeScript = drvName: ''
    export NIX_CONFIG='extra-experimental-features = nix-command ca-derivations dynamic-derivations'
    realpath --relative-to="$NIX_BUILD_TOP" "$PWD" > "$NIX_BUILD_TOP/.gg-cwd"
    # Stage a copy of $NIX_BUILD_TOP excluding sandbox-infrastructure
    # entries `nix store add --scan` cannot handle: `.nix-socket` is
    # the builder-rpc-v0 daemon's own unix socket (NIX_REMOTE points at
    # it — confirmed via NIX_REMOTE=unix:///build/.nix-socket in this
    # same sandbox), and `--scan` chokes on a socket with "file
    # '/.nix-socket' has an unsupported type" (confirmed directly
    # against fmt, which is large enough to still be building when
    # this ran). Can't just `rm` it from the live tree first: it's
    # still needed by the `nix store add`/`nix derivation add`/
    # `nix store submit-output` calls immediately below, which go
    # through that exact socket. `.gg-stage` (this staging dir itself)
    # is excluded too so store-add doesn't see itself mid-copy.
    mkdir -p "$NIX_BUILD_TOP/.gg-stage"
    for e in "$NIX_BUILD_TOP"/* "$NIX_BUILD_TOP"/.[!.]* "$NIX_BUILD_TOP"/..?*; do
      base=$(basename "$e")
      [ -e "$e" ] || continue
      case "$base" in .nix-socket|.gg-stage) continue ;; esac
      cp -a "$e" "$NIX_BUILD_TOP/.gg-stage/"
    done
    placeholder=$(nix eval --raw --expr 'builtins.placeholder "out"')
    treePath=$(nix store add --scan -n "${drvName}-tree" "$NIX_BUILD_TOP/.gg-stage")
    treeBase=$(basename "$treePath")
    printf '{"name":"%s","system":"%s","builder":"%s","args":["-c","export PATH=%s; mkdir -p $out && cp -a /nix/store/%s/. $out/"],"env":{"out":"%s"},"inputs":{"drvs":{},"srcs":["%s","%s","%s"]},"outputs":{"out":{"method":"nar","hashAlgo":"sha256"}},"version":4}' \
      "${drvName}" "${builtins.currentSystem}" "${bash}/bin/bash" "${coreutils}/bin" "$treeBase" "$placeholder" "${builtins.baseNameOf "${bash}"}" "${builtins.baseNameOf "${coreutils}"}" "$treeBase" \
      > /tmp/gg-inner-json
    drvPath=$(cat /tmp/gg-inner-json | nix derivation add)
    nix store submit-output "$drvPath" out
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

        withPhase1Attrs =
          finalAttrs:
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
            # Instead, stop phase1 after checkPhase using the SAME
            # dont*/do* toggles setup.sh's own runPhase already checks
            # (see runPhase's guards: dontInstall, dontFixup,
            # doInstallCheck, doDist) — this way every hook-injected
            # phase before checkPhase still runs in the real order,
            # whatever that order turns out to be for this package.
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

            # Bypass bracket: NIXGG_BYPASS must cover autoreconfHook too
            # (it injects itself via preConfigurePhases, which runs
            # BEFORE configurePhase — confirmed by reading setup.sh's
            # phase list), so it's set at postPatch, not preConfigure.
            #
            # Left set for the whole build (never unset before
            # buildPhase): per-TU shim acceleration inside phase 1 only
            # produces drvref STUB files at each shimmed output path
            # (see internal/drvref's docstring — a deliberate design
            # choice, resolution is deferred to `nixgg force`, which
            # itself requires a plain top-level `nix build` — the exact
            # thing already proven impossible inside this sandbox, see
            # this file's own top comment). Unbypassing here built
            # correctly compile-by-compile but then installPhase copied
            # those unresolved stubs into $out, and versionCheckHook
            # failed with "hello was not found, or is not an
            # executable" — confirmed directly. Resolving stubs inside a
            # builder-rpc-v0 sandbox is real, separate, not-yet-started
            # work (generalizing nixgg's single-target JSON-drv path to
            # "whatever buildPhase left behind" — see
            # submitBuildTreeScript's own docstring). Until that lands,
            # phase 1 stays a real, unaccelerated native build: it
            # proves the phase-split/submit-buildtree plumbing (real
            # configurePhase, real autotools, real make, captured and
            # handed to phase 2 as one drv) without silently shipping
            # broken artifacts.
            postPatch = (probeArgs.postPatch or "") + ''
              export NIXGG_BYPASS=1
            '';
            preBuild = ''
              export NIX_CONFIG="extra-experimental-features = nix-command ca-derivations dynamic-derivations"
              export PATH="${patchedNix}/bin:$PATH"
            '' + (probeArgs.preBuild or "");

            postBuild = (probeArgs.postBuild or "") + submitBuildTreeScript outerName;
          };

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
      # preInstall restores the WHOLE captured $NIX_BUILD_TOP tree (not
      # just the cmake-style build subdir) directly into THIS
      # derivation's own $NIX_BUILD_TOP, then cds to the same relative
      # offset recorded by submitBuildTreeScript's .gg-cwd marker — see
      # that script's own comment for why (cmake's baked absolute
      # source-dir path). dontUnpack + cd replaces the plain `cp -a
      # ${builtTree}/. .` this had before multi-output/cmake testing.
      mkDerivationSuper (
        finalAttrs:
        (lib.toFunction argsOrFn finalAttrs)
        // {
          phases = "installPhase fixupPhase installCheckPhase distPhase";
          dontUnpack = true;
          # See withPhase1Attrs's __structuredAttrs comment — same
          # reasoning applies to installFlags/postInstall below, which
          # are plain top-level attrs here too.
          __structuredAttrs = false;
          preInstall = ''
            cp -a ${builtTree}/. "$NIX_BUILD_TOP/"
            chmod -R u+w "$NIX_BUILD_TOP"
            cd "$NIX_BUILD_TOP/$(cat "$NIX_BUILD_TOP/.gg-cwd")"
            export DESTDIR="$NIX_BUILD_TOP/.gg-destdir"
          '' + (probeArgs.preInstall or "");
          installFlags = (probeArgs.installFlags or "");
          postInstall = ''
            mkdir -p "$out"
            cp -a "$DESTDIR/nonexistent/." "$out/"
          '' + (probeArgs.postInstall or "");
        }
      );
  }
)
