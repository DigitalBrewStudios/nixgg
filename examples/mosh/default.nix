# mosh — mobile shell. Autoconf + protoc + libtool + pkg-config.
#
# STATUS: partially wired. Configure now uses NIXGG_BYPASS=1 to
# skip shims (autoconf conftests need real .o files, which sandbox
# mode can't produce). The compiler probes pass, header checks
# pass, then configure fails at "Unable to find zlib" — the
# -L/-I flags stdenv usually injects via NIX_LDFLAGS /
# NIX_CFLAGS_COMPILE aren't set by mkNixggBuild.
#
# Fixing this cleanly requires either:
#   1. Threading buildInputs into mkNixggBuild and generating
#      the right NIX_*FLAGS, or
#   2. Running the whole configure step under stdenv's setup hooks
#      (which then need to not fight with NIXGG_BYPASS).
#
# Deferred. The mosh definition is retained as a documentation
# fixture — the shape a working autoconf-based dyn-drv build takes.
#
#
#   - autoconf `./configure` phase: fires conftests
#     ("conftest.c → conftest") which the compile shim recognises by
#     filename and realises synchronously. Without that, ./configure
#     would see the thunk symlink and fail its "does the compiler
#     work?" probe.
#
#   - protoc: mosh generates .pb.cc / .pb.h at build time. The
#     compile shim treats those as ordinary sources once produced.
#     protoc itself runs in the sandbox against extraToolchain.
#
#   - libtool + pkg-config: pull in ncurses / openssl / zlib /
#     protobuf.
#
#   - autogen.sh, if the tarball has no bundled configure: extract
#     it first.
#
# mosh has two link targets (mosh-client + mosh-server). We pick
# mosh-server as the submitted output — the other still runs
# through the link shim and gets its own drv, just no submit-output.
{
  mkNixggBuild,
  src,
  autoconf,
  automake,
  libtool,
  pkg-config,
  perl,
  protobuf,
  which,
  gnum4,
  gnugrep,
  gnused,
  gawk,
  file,
  # buildInputs (headers / libs referenced by mosh's link):
  ncurses,
  openssl,
  zlib,
}:

mkNixggBuild {
  pname = "mosh";
  version = "unstable";
  inherit src;
  target = "mosh-server";
  extraToolchain = [
    autoconf
    automake
    libtool
    pkg-config
    perl
    protobuf
    which
    gnum4
    gnugrep
    gnused
    gawk
    file
    ncurses
    openssl
    zlib
  ];
  buildCommand = ''
    # Configure with NIXGG_BYPASS=1 so autoconf's conftests exec
    # real compiler + real .o files. autogen.sh + configure both
    # end here; make picks up shim mode.
    [[ -x configure ]] || NIXGG_BYPASS=1 ./autogen.sh
    NIXGG_BYPASS=1 ./configure --disable-hardening

    # Build phase — shims fire, every cc/c++/ar becomes a drv.
    make
  '';
}
