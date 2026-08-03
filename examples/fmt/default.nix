# {fmt} — cmake-driven C++ header + library. Exercises three
# nixgg pieces the smaller examples don't:
#
#   - cmake+ninja driving the shims (not `make`);
#   - a *static library* (libfmt.a) as one of the outputs, so the
#     archive shim path matters (not just compile+link);
#   - realise-mode carveout for cmake's compiler probes
#     (CheckCXX*Compiler, CheckIncludeFile, …) — the compile shim's
#     filename heuristic auto-realises those synchronously, so
#     configure sees actual .o files, not thunks.
#
# extraToolchain adds cmake + ninja + pkg-config to the sandbox's
# PATH. mkNixggBuild wraps their /bin dirs in.
{
  mkNixggBuild,
  src,
  cmake,
  ninja,
  pkg-config,
}:

mkNixggBuild {
  pname = "fmt";
  version = "11.0.2";
  inherit src;
  # libfmt.a is the "big" output; a header-only variant exists too
  # but we want the archive path exercised.
  target = "libfmt.a";
  extraToolchain = [ cmake ninja pkg-config ];
  buildCommand = ''
    # Configure runs with NIXGG_BYPASS=1 so cmake's compiler probes
    # (CheckCXXCompiler, CheckIncludeFile, etc.) get real binaries.
    # Shims are still on PATH — they just exec-passthrough to the
    # real tool. cmake happily hard-codes the shim path into its
    # generated build files; once we unset NIXGG_BYPASS, subsequent
    # cmake --build invocations go through nixgg normally.
    NIXGG_BYPASS=1 cmake -S . -B build -G Ninja \
      -DCMAKE_BUILD_TYPE=Release \
      -DFMT_TEST=OFF -DFMT_DOC=OFF \
      -DBUILD_SHARED_LIBS=OFF

    cmake --build build --target fmt
  '';
}
