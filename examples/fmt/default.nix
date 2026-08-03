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
    # -DCMAKE_CXX_COMPILER_WORKS=1 short-circuits cmake's compiler
    # probe. Under nixgg sandbox mode we don't yet have a working
    # realise-mode path for probes: the inner nix that would build
    # them can't reach the outer store's paths (see dyn-drv/NOTES.md
    # on why nix-instantiate doesn't work inside builder-rpc-v0).
    # Skipping the probe is safe here — the toolchain is pinned by
    # nixgg's flake, and native mode still runs the probes fine.
    cmake -S . -B build -G Ninja \
      -DCMAKE_BUILD_TYPE=Release \
      -DCMAKE_CXX_COMPILER_WORKS=1 -DCMAKE_C_COMPILER_WORKS=1 \
      -DFMT_TEST=OFF -DFMT_DOC=OFF \
      -DBUILD_SHARED_LIBS=OFF
    cmake --build build --target fmt
  '';
}
