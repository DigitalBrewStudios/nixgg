# mosh — mobile shell. Real-world autoconf + protoc + libtool +
# pkg-config example.
#
# Configure runs with NIXGG_BYPASS=1 (see nixgg/nix/mkNixggBuild.nix)
# — autoconf conftests need real .o files, which sandbox mode
# doesn't provide. Once configure completes, `make` runs with
# shims on and every cc/c++/ar becomes a nixgg drv.
#
# buildInputs (ncurses/openssl/zlib/protobuf) are translated by
# mkNixggBuild into NIX_CFLAGS_COMPILE / NIX_LDFLAGS (-isystem
# / -L / -rpath) so the pinned gcc-wrapper finds their headers
# and libs. Same convention nixpkgs's stdenv uses.
#
# mosh has two link targets (mosh-client + mosh-server). We pick
# mosh-server as the submitted output — the other still runs
# through the link shim and gets its own drv, no submit-output.
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
  # ncurses / openssl / zlib each split their outputs into .dev
  # (headers) and default (libs). Autoconf's link probes need both;
  # the flake pipes them as lists.
  ncurses,       # [ ncurses.dev ncurses ]
  openssl,       # [ openssl.dev openssl.out ]
  zlib,          # [ zlib.dev zlib.out ]
  protobuf-lib,  # protobuf (single output)
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
  ];
  buildInputs = ncurses ++ openssl ++ zlib ++ [ protobuf-lib ];
  buildCommand = ''
    [[ -x configure ]] || NIXGG_BYPASS=1 ./autogen.sh
    NIXGG_BYPASS=1 ./configure --disable-hardening
    make
  '';
}
