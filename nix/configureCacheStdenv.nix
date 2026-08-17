# configureCacheStdenv — splits stdenv.mkDerivation at the
# configure/build boundary: group A runs unpack..configure and
# snapshots its tree; group B always runs its own unpack+patch
# against the REAL src (cheap: tar extraction + patch application),
# then overlays group A's snapshot on top and runs build..dist. A
# group-B-only attr change (installFlags, etc.) never invalidates
# group A.
#
# With `configureSrcFilter` set, group A's `src` is shrunk to just
# the files configure reads, via a small CA filter-derivation — see
# configureSrcFilter.nix. Combined with CA on group A's own output
# (below), an edit outside the filtered set never even reruns
# group A's configure step.
#
# Unlike nix/dynDrvStdenv.nix (build/install split, needs
# builder-rpc-v0 + shims for real per-TU acceleration), this split
# discovers nothing unknown at build time — group A is a plain,
# ordinary derivation, no sandbox, no RPC.
#
# Usage:
#   hello = pkgs.hello.override {
#     stdenv = configureCacheStdenv { stdenv = pkgs.stdenv; };
#   };
{
  lib,
  nixpkgsPath, # nixpkgs tree stdenv0 came from, for the
               # mkDerivationFromStdenv fallback.
  config, # real nixpkgs config — make-derivation.nix reads it.
  stdenvNoCC, # for the configureSrcFilter derivation (copies files only).
}:

{
  stdenv,
  # .overrideAttrs on the RETURNED package can't reach group A
  # (nixpkgs' .override always re-invokes with the original attrs
  # first) — use these at the configureCacheStdenv call site instead.
  extraGroupAAttrs ? (finalAttrs: old: old),
  extraGroupBAttrs ? (finalAttrs: old: old), # group B IS reachable via .overrideAttrs; kept for symmetry.
  # Opt-in only, default null (no filtering — always safe). An
  # attrset `{ includePatterns; existenceStubs ? []; }` — see
  # configureSrcFilter.nix. An under-inclusive pattern list silently
  # produces a STALE build, not an error — verify by building.
  configureSrcFilter ? null,
}:

let
  stdenv0 = stdenv;

  # This pinned nixpkgs' make-derivation.nix curries `lib: config:
  # stdenv: {...}`.
  defaultMkDerivationFromStdenv =
    stdenv:
    (import "${nixpkgsPath}/pkgs/stdenv/generic/make-derivation.nix" lib config stdenv).mkDerivation;

  mkConfigureSrcFilter = import ./configureSrcFilter.nix { inherit lib stdenvNoCC; };
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
        # Never collapse argsOrFn to a plain set — that destroys
        # makeDerivationExtensible's fixed point. probeArgs is a
        # throwaway {} application, used only for static info that
        # doesn't depend on finalAttrs.
        probeArgs = lib.toFunction argsOrFn { };
        realOutputs = probeArgs.outputs or [ "out" ];
        existenceStubs = if configureSrcFilter == null then [ ] else configureSrcFilter.existenceStubs or [ ];

        # Snapshots group A's configured tree into "ggtree" for group
        # B to overlay. existenceStubs are dropped first: they're
        # empty placeholders that exist only so group A's OWN
        # configure succeeds (e.g. AC_CONFIG_SRCDIR's presence check)
        # and would otherwise clobber the real file group B's own
        # unpack already produced. .gg-cwd/.gg-buildroot record group
        # A's cwd offset and absolute build directory, for group B's
        # restore/rewrite below.
        snapshotScript = ''
          mkdir -p ${lib.concatMapStrings (o: "\"$" + o + "\" ") realOutputs} "$ggtree"
          cp -a "$NIX_BUILD_TOP/$sourceRoot/." "$ggtree/tree"
          ${lib.concatMapStrings (
            p: "rm -f \"$ggtree\"/tree/${lib.escapeShellArg p}\n"
          ) existenceStubs}
          realpath --relative-to="$NIX_BUILD_TOP/$sourceRoot" "$PWD" > "$ggtree/.gg-cwd"
          printf '%s' "$NIX_BUILD_TOP/$sourceRoot" > "$ggtree/.gg-buildroot"
        '';

        withGroupAAttrs =
          finalAttrs:
          let
            # Real finalAttrs fixed point, not probeArgs — some
            # packages' src is self-referential through finalAttrs
            # (e.g. hello's mirror URL uses finalAttrs.version).
            orig = lib.toFunction argsOrFn finalAttrs;

            groupASrc =
              if configureSrcFilter == null then
                orig.src
              else
                mkConfigureSrcFilter {
                  name = "${orig.pname or orig.name}-configure-src";
                  src = orig.src;
                  includePatterns = configureSrcFilter.includePatterns;
                  existenceStubs = configureSrcFilter.existenceStubs or [ ];
                };

            base =
              orig
              // {
                # Distinct name from group B (which keeps the real
                # name — it's the actual deliverable), so build/log
                # output shows which derivation is which.
                name =
                  if orig ? name then "${orig.name}-configure" else "${orig.pname}-configure-${orig.version}";
                # `phases` stays unset — setup hooks (autoreconfHook,
                # cmake, ...) splice pre*Phases in at runtime; a
                # hardcoded phases string would drop those. Native
                # runPhase toggles (dontBuild etc.) truncate the run.
                #
                # __structuredAttrs must be off: under it,
                # make-derivation.nix only honors env-nested attrs,
                # not these bare top-level toggles/outputs (confirmed:
                # hello sets it true, and doCheck=false was silently
                # ignored without this forcing).
                __structuredAttrs = false;
                dontBuild = true;
                dontInstall = true;
                dontFixup = true;
                doCheck = false;
                doInstallCheck = false;
                doDist = false;
                src = groupASrc;
                # Real outputs (not forced single-output like
                # dynDrvStdenv's phase1 — group A has no ".drv"-suffix
                # naming requirement) plus "ggtree" for the snapshot.
                # Gets multiple-outputs.sh's own routing working with
                # real, self-consistent paths for free.
                outputs = realOutputs ++ [ "ggtree" ];
                # CA + early-cutoff: if group A re-runs (its input
                # changed) but its output content doesn't, its output
                # PATH stays the same, so group B's input doesn't
                # change either. "nar" not "text" — outputs are
                # directory trees. No conflict with
                # __structuredAttrs=false (CA attrs are bare top-level
                # too, unconditionally emitted by make-derivation.nix).
                __contentAddressed = true;
                outputHashMode = "nar";
                outputHashAlgo = "sha256";
                # installPhase never runs (dontInstall above); create
                # the real outputs + ggtree here instead, right after
                # configure. A normal hook is fine — configurePhase is
                # never itself skipped, so postConfigure always runs.
                postConfigure = (orig.postConfigure or "") + snapshotScript;
              };
          in
          extraGroupAAttrs finalAttrs base;

        # Group A: unpack, patch, configure — real nixpkgs phases,
        # unmodified. No sandbox, no shims, no requiredSystemFeatures.
        groupA = mkDerivationSuper withGroupAAttrs;

        # Group A's configure baked ITS OWN real output paths into
        # Makefiles/cmake_install.cmake/.pc/.la files. Group B has
        # DIFFERENT real output paths for the same outputs — rewrite
        # every occurrence right after the tree restore, before
        # buildPhase, so group B's own linker bakes a correct RPATH
        # natively (no patchelf step needed at this boundary at all).
        pathRewriteScript =
          let
            sedExprs = lib.concatMapStrings (
              o: " -e \"s|" + toString groupA.${o} + "|$" + o + "|g\""
            ) realOutputs;
          in
          ''
            while IFS= read -r -d "" gg_f; do
              # preserve mtime: some Makefiles compare mtimes to decide
              # whether to regenerate a checked-in file (e.g. via
              # makeinfo, not on PATH here) — a bare sed -i would
              # trigger that.
              gg_ref="$(mktemp)"
              touch -r "$gg_f" "$gg_ref"
              sed -i${sedExprs} "$gg_f"
              touch -r "$gg_ref" "$gg_f"
              rm -f "$gg_ref"
            done < <(grep -rlZI -F "/nix/store/" "$NIX_BUILD_TOP" 2>/dev/null)
          '';
      in
      mkDerivationSuper (
        finalAttrs:
        let
          base =
            (lib.toFunction argsOrFn finalAttrs)
            // {
              # Group B always runs its own real unpack+patch against
              # the REAL src (cheap), THEN overlays group A's snapshot
              # on top via ggRestorePhase — this is what makes the
              # same restore logic work whether configureSrcFilter is
              # set or not: group B's own sourceRoot always matches
              # the real source, so the overlay lands at the right
              # depth with no special-casing needed.
              phases = "unpackPhase patchPhase ggRestorePhase buildPhase checkPhase installPhase fixupPhase installCheckPhase distPhase";
              __structuredAttrs = false; # same reason as group A — dontConfigure is a bare toggle
              dontConfigure = true;
              # Custom phase name, not a postConfigure/preBuild hook:
              # dontConfigure means runPhase skips configurePhase (and
              # any hook attached to it) entirely.
              ggRestorePhase = ''
                runHook preGgRestore
                cp -a ${groupA.ggtree}/tree/. .
                chmod -R u+w .
                # Group A's absolute build directory (always different
                # from group B's own — each derivation gets its own
                # /build/<hash>) can be baked as literal text by tools
                # that compute srcdir/builddir absolutely rather than
                # relatively (e.g. automake's generated Makefile
                # invoking build-aux/missing). Rewrite it before the
                # store-path rewrite below, since both can appear in
                # the same generated file.
                gg_oldroot="$(cat ${groupA.ggtree}/.gg-buildroot)"
                gg_newroot="$PWD"
                if [ "$gg_oldroot" != "$gg_newroot" ]; then
                  grep -rlZI -F "$gg_oldroot" . 2>/dev/null | while IFS= read -r -d "" gg_f; do
                    gg_ref="$(mktemp)"
                    touch -r "$gg_f" "$gg_ref"
                    sed -i "s|$gg_oldroot|$gg_newroot|g" "$gg_f"
                    touch -r "$gg_ref" "$gg_f"
                    rm -f "$gg_ref"
                  done
                fi
                cd "$(cat ${groupA.ggtree}/.gg-cwd)"
                ${pathRewriteScript}
                runHook postGgRestore
              '';
            };
        in
        extraGroupBAttrs finalAttrs base
      );
  }
)
