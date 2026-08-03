# Lua 5.4 through nixgg. Plain Makefile, ~30 TUs, one archive
# (liblua.a), two link targets in upstream (lua, luac) — we build the
# `lua` binary here; luac is a follow-up (mkNixggBuild currently
# submits one outer output).
#
# `src` is passed in from the flake's `lua-src` input (a plain
# tarball fetcher). Nothing here knows or cares where it came from.
{
  mkNixggBuild,
  src,
}:

mkNixggBuild {
  pname = "lua";
  version = "5.4.7";
  inherit src;
  target = "lua";
  # `make linux` from src/ is upstream's Linux recipe: sets
  # SYSCFLAGS + SYSLIBS then recurses into `make all`. `all` builds
  # every .o, ar's them into liblua.a, and links `lua` + `luac`.
  #
  # mkNixggBuild will pick the `lua` link (matches NIXGG_SANDBOX_TARGET)
  # as the submitted output. The luac link fires too but its
  # submit-output call is idempotently ignored.
  buildCommand = ''
    cd src
    make linux CC=cc
  '';
}
