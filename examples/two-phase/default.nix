# Two-phase mkNixggBuild — smoke test for the "phase-1 output as
# input to phase-2" pattern that LLVM's tblgen mid-build execution
# needs. If this works cleanly, the same shape scales to
# llvm-min-tblgen → llvm-config, protoc → user code, rustc stage0
# → stage1, etc.
#
# Structure:
#   phase1 (codegen/) → produces the `codegen` binary
#   phase2 (app/)     → cmake-style: buildInputs = [ phase1.result ],
#                       CODEGEN=${phase1.result}/bin/codegen path passed
#                       to the Makefile so `codegen hello > generated.h`
#                       runs mid-build. main.c #includes generated.h.
#
# The whole point: phase 2's builder mounts phase 1's output as a
# real store path (because Nix walks the drv graph), so exec-ing
# `codegen` mid-build works — no drvref-stub / self-realising
# gymnastics needed.
{
  mkNixggBuild,
  lib,
  codegenSrc,
  appSrc,
}:

let
  phase1 = mkNixggBuild {
    pname = "two-phase-codegen";
    version = "0";
    src = codegenSrc;
    target = "codegen";
    buildCommand = "make -j\"$NIX_BUILD_CORES\"";
  };

  phase2 = mkNixggBuild {
    pname = "two-phase-app";
    version = "0";
    src = appSrc;
    target = "app";
    # phase1.result is a `builtins.outputOf` node; putting it in
    # buildInputs threads it through Nix's dep graph as a required
    # store path. When phase2's sandbox starts, phase1's output is
    # mounted at that store path.
    buildInputs = [ phase1.result ];
    buildCommand = ''
      make -j"$NIX_BUILD_CORES" CODEGEN=${phase1.result}/bin/codegen
    '';
  };
in
{
  inherit (phase2) drv shell;
  inherit (phase2) result;
  # Expose phase1 too so we can `nix build .#two-phase-codegen`
  # independently for smoke tests.
  codegen = phase1.result;
}
