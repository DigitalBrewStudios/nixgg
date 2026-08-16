# Starting-point configureSrcFilter include-pattern lists. NOT
# guaranteed correct for any specific package — an under-inclusive
# pattern silently produces a stale build, not an error, so verify by
# building. `find -path` glob depth is literal ("*/x" matches one
# level, not any depth) — deeply-nested packages may need more
# patterns or a coarser include.
#
# These are plain includePatterns lists, not full configureSrcFilter
# values — pair with a package-specific `existenceStubs` at the call
# site for autoconf's AC_CONFIG_SRCDIR (one author-chosen file, tested
# for existence only, e.g. `existenceStubs = [ "src/hello.c" ];` for
# hello):
#
#   configureSrcFilter = {
#     includePatterns = configureSrcFilterPresets.autotools;
#     existenceStubs = [ "src/hello.c" ];
#   };
{
  # Autotools: configure + everything autoreconf/automake read to
  # regenerate it (Makefile.am/configure.ac, aclocal/m4 macros,
  # build-aux scripts), every *.in template config.status substitutes
  # into a generated file, *.mk fragments (Automake's non-recursive
  # Makefile convention — real prerequisites of Makefile.in's own
  # regen rule; a MISSING one makes `make` unconditionally reinvoke
  # automake), and po/ wholesale (gettext's translation subsystem is
  # a dense web of package-specific files with no small glob that
  # captures it — all-or-nothing is the pragmatic choice here).
  autotools = [
    "configure"
    "configure.ac"
    "configure.in"
    "Makefile.am"
    "*/Makefile.am"
    "*/*/Makefile.am"
    "Makefile.in"
    "*/Makefile.in"
    "*/*/Makefile.in"
    "*.in"
    "*/*.in"
    "*/*/*.in"
    "*.mk"
    "*/*.mk"
    "*/*/*.mk"
    "aclocal.m4"
    "config.h.in"
    "*.m4"
    "*/*.m4"
    "build-aux"
    "build-aux/*"
    "m4"
    "m4/*"
    "po"
    "po/*"
  ];

  # CMake: CMakeLists.txt at any nesting level cmake's own
  # add_subdirectory() graph might use, plus *.cmake modules.
  cmake = [
    "CMakeLists.txt"
    "*/CMakeLists.txt"
    "*/*/CMakeLists.txt"
    "*/*/*/CMakeLists.txt"
    "*.cmake"
    "*/*.cmake"
    "cmake"
    "cmake/*"
  ];
}
