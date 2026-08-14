# dynDrvStdenv applied to zstd, demonstrating the phase-chaining
# pattern for packages that exec one of their own binaries mid-build.
#
# Real zstd (via dynDrvStdenv alone, unpatched) builds and shims
# correctly for every actual TU — every lib/*.c and programs/*.c
# compile drv is real. But zstd's own cmake graph, when
# ZSTD_BUILD_CONTRIB is on (nixpkgs' own default for a native build),
# also execs `contrib/gen_html` — A BINARY IT JUST BUILT — mid-build
# (`add_custom_target(zstd_manual.html ALL ${GENHTML_BINARY} ...
# DEPENDS gen_html)`) to render zstd_manual.html from zstd.h's
# doc-comments. In sandbox mode the shim's link step for gen_html
# leaves a drvref STUB in place of a real executable — nothing inside
# a builder-rpc-v0 sandbox resolves dyn-drv outputs synchronously (see
# dynDrvStdenv.nix's own top comment on why nested nix/nix-instantiate
# doesn't work there either) — so `./gen_html` fails with "Permission
# denied": it's a small text file, not an ELF.
#
# WHY A PLAIN .overrideAttrs PATCH CANNOT FIX THIS (confirmed
# directly, cost real investigation time): nixpkgs' own
# `.overrideAttrs`-then-`.override` (and `.override`-then-
# `.overrideAttrs`) reapplication contract ALWAYS re-invokes the
# wrapped package's function body with the ORIGINAL, unpatched attrs
# first — `.override`'s own implementation is `newArgs: makeOverridable
# f (origArgs // newArgs)` (lib/customisation.nix), which calls `f`
# (zstd's package function) fresh; `.overrideAttrs`'s cooperating
# `override` is `(f args).overrideAttrs fdrv` — same thing, the
# attrs-patch is reapplied to the ALREADY-COMPUTED result afterward.
# dynDrvStdenv's phase1 is a `let`-bound value closed over inside that
# first, unpatched `f args` call, so no attrs-level override —
# regardless of which order `.override`/`.overrideAttrs` are written
# in the calling code — can ever reach it. Verified empirically: both
# orderings produced a byte-IDENTICAL phase1 derivation hash to the
# fully unpatched build; the patch text was verifiably absent from the
# real built drv's own postPatch env var (`nix derivation show`), even
# though it WAS present on the outer/phase2 attrs in isolation.
#
# The fix that actually works: dynDrvStdenv's `extraPhase1Attrs`
# parameter (see nix/dynDrvStdenv.nix's own docstring) is spliced into
# phase1's attrs BEFORE phase1 is computed, at the `dynDrvStdenv {
# ...; }` call site itself — not via any nixpkgs override mechanism.
# This is the one and only way to patch a wrapped package's real
# configure/build behavior from outside.
#
# Two-phase structure, same shape as examples/two-phase.nix:
#   phase A (mkNixggBuild) -> builds contrib/gen_html/gen_html.cpp
#            standalone (one TU, no cmake, no zstd-specific deps at
#            all — it only #includes <iostream>/<fstream>/<sstream>/
#            <vector>) into a real, resolved binary.
#   phase B (dynDrvStdenv, extraPhase1Attrs) -> zstd's REAL cmake
#            build, patched so contrib/gen_html/CMakeLists.txt calls
#            phase A's ALREADY-BUILT binary directly instead of
#            compiling+execing its own. Nix's dep graph mounts phase
#            A's real output at a real store path before phase B's
#            phase1 derivation is even instantiated.
{
  pkgs,
  mkNixggBuild,
  dynDrvStdenv,
}:

let
  # Standalone build of the one helper binary zstd's own cmake graph
  # wants to exec mid-build. gen_html.cpp is entirely self-contained
  # (iostream/fstream/sstream/vector only) — no zstd headers, no cmake
  # involvement, so this is a plain single-TU mkNixggBuild call, the
  # same shape as examples/two-phase/codegen.
  genHtml = mkNixggBuild {
    pname = "zstd-gen-html";
    version = "0";
    src = pkgs.zstd.src;
    target = "gen_html";
    buildCommand = ''
      cd contrib/gen_html
      g++ -O2 -c gen_html.cpp -o gen_html.o
      g++ gen_html.o -o gen_html
    '';
  };
in
pkgs.zstd.override {
  stdenv = dynDrvStdenv {
    stdenv = pkgs.stdenv;
    extraPhase1Attrs = finalAttrs: old: old // {
      # Removes gen_html's own add_executable + its DEPENDS edge, and
      # points GENHTML_BINARY at phase A's real, already-resolved
      # binary instead. Touches exactly the three lines that named the
      # problem — every other TU (lib/*.c, programs/*.c,
      # contrib/pzstd/*, the test suite) still goes through
      # dynDrvStdenv's real shim acceleration unmodified.
      postPatch =
        old.postPatch
        + ''
          substituteInPlace build/cmake/contrib/gen_html/CMakeLists.txt \
            --replace-fail \
              'add_executable(gen_html ''${GENHTML_DIR}/gen_html.cpp)' \
              "" \
            --replace-fail \
              'DEPENDS gen_html COMMENT "Update zstd manual")' \
              'COMMENT "Update zstd manual")' \
            --replace-fail \
              'set(GENHTML_BINARY ''${PROJECT_BINARY_DIR}/gen_html''${CMAKE_EXECUTABLE_SUFFIX})' \
              'set(GENHTML_BINARY ${genHtml.package}/bin/gen_html)'
        '';
    };
  };
}
