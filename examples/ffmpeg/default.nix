# ffmpeg — the stress test for shim submission throughput.
# ~1200 translation units under a plain autoconf-like `./configure`
# (custom, not GNU autotools) + recursive make. No optional
# codec/library flags are on: this is the "core ffmpeg only" build,
# which still exercises the full TU count and gives us a clean
# fork+exec-per-drv timing without the noise of a dozen external
# dependency setups.
#
# Configure phase runs under NIXGG_BYPASS=1 like mosh's: ffmpeg's
# configure script compiles many probe programs that need runnable
# binaries; the shim's drvref stub doesn't help there. Once
# configure completes, `make -j"$NIX_BUILD_CORES"` recurses and
# every cc invocation becomes a nixgg drv.
#
# CURRENT STATUS: shim throughput scales — ~1200 compile drvs
# submit cleanly (~46s wall on 32 cores, 3-4x parallel speedup vs
# `-j1`). A subset of TUs (parseutils.o and a few relatives) fail
# to compile inside the inner drv with "gmtime_r implicit
# declaration" — the drv env is missing something the outer
# configure baked into config.h (`HAVE_GMTIME_R=1` there vs.
# missing `<time.h>` feature-test-macro plumbing in the drv's
# NIX_CFLAGS_COMPILE). Follow-up work; unrelated to the shim
# submission or the -L/-l resolution.
#
# The link shim's -L/-l resolution is generic — it picks up
# `-L libavcodec -lavcodec` combos and treats
# `libavcodec/libavcodec.a` as a real input if it's a nixgg
# drvref stub. Verified on the smaller ffmpeg binaries.
{
  mkNixggBuild,
  src,
  pkg-config,
  perl,
  nasm,
  yasm,
  gnumake,
  which,
}:

mkNixggBuild {
  pname = "ffmpeg";
  version = "7.1.2";
  inherit src;
  # Stop at ffmpeg_g (unstripped). ffmpeg's Makefile normally does
  # `strip ffmpeg_g -o ffmpeg` after the link — but the link shim
  # left a drvref stub at ffmpeg_g (not a real ELF), so strip
  # would fail with "file format not recognized". Submitting the
  # unstripped variant sidesteps that; a downstream step could
  # realize+strip if needed, but for a build-throughput benchmark
  # the unstripped binary is what we want anyway.
  target = "ffmpeg_g";
  nativeBuildInputs = [ pkg-config perl nasm yasm gnumake which ];
  buildInputs = [ ];
  buildCommand = ''
    # ffmpeg configure has no autoconf; it's a bespoke shell script.
    # NIXGG_BYPASS so probe compilations hit the outer cc-wrapper
    # directly (drvref stubs are opaque to configure's `./conftest`
    # exec-then-check pattern).
    #
    # --disable-doc: avoids texinfo / makeinfo dependency.
    # --disable-htmlpages / manpages / podpages: same.
    # --disable-x86asm: skip nasm probing. Enable if you actually
    #   want the accelerated codepaths — for a shim-throughput test
    #   the pure-C path exercises more TUs per drv equally.
    # --cc="cc": explicit; ffmpeg's configure defaults to whatever's
    #   in $CC (which our preBuild unsets to let the argv0-based
    #   default win).
    NIXGG_BYPASS=1 ./configure \
      --cc=cc \
      --disable-doc \
      --disable-htmlpages \
      --disable-manpages \
      --disable-podpages \
      --disable-x86asm

    make -j"$NIX_BUILD_CORES" ffmpeg_g
  '';
}
