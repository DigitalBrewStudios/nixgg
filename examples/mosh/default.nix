# mosh — mobile shell. The hardest of the three examples:
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
    # First-run: some mosh checkouts ship without a generated
    # configure. autogen.sh regenerates from configure.ac.
    [[ -x configure ]] || ./autogen.sh
    ./configure --disable-hardening
    make
  '';
}
