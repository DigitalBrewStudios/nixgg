# configureCacheStdenv — work in progress

Context for resuming this thread in a fresh session. See
`nix/configureCacheStdenv.nix`'s own top comment for the mechanism;
this doc is about *state*. Sibling doc: `WIP-dynDrvStdenv.md` (the
build/install-boundary split this generalizes from).

## What this is

`configureCacheStdenv` splits `stdenv.mkDerivation` at the
CONFIGURE/BUILD boundary (unlike `dynDrvStdenv`'s build/install split),
via the same one-line stdenv override:

```nix
pkgs.hello.override {
  stdenv = configureCacheStdenv { stdenv = pkgs.stdenv; };
}
```

"Group A" runs unpack..configure only, named `<pname>-configure-<version>`
(distinct from group B, which keeps the package's real, unmodified
name — it's the actual deliverable). "Group B" always runs its own
real unpack+patch against the REAL `src` (cheap — tar extraction +
patch application), then overlays group A's snapshot on top and runs
build..dist. Changing a group-B-only attr (e.g. `installFlags`) never
rebuilds or rehashes group A.

## Why this is simpler than dynDrvStdenv

Task #47 asked whether dynDrvStdenv's per-TU idea generalizes to
"one dynamic derivation per stdenv phase". It doesn't need to, at
least not at this boundary: dynDrvStdenv's phase 1 needs
builder-rpc-v0 + RPC submit-output because it discovers an *unknown*
set of compiler sub-derivations at build time (`$out` is literally
unset inside that sandbox). A configure/build split discovers nothing
unknown — we already know at eval time exactly what configurePhase
does. So group A is a **plain, ordinary derivation** (no sandbox, no
shims, no RPC, no `requiredSystemFeatures`) — it does use
`__contentAddressed`, but only for the early-cutoff win (see below),
never as a mechanism for building an unknown sub-graph the way
dynDrvStdenv's phase1 does. Group B references group A via a normal
Nix string interpolation — not `builtins.outputOf`. Precedented by
`dyn-drv/dyn-one-layer.nix`'s `inner` derivation, which is exactly this
shape already checked into the repo.

## Current status: working, with real early-cutoff

Four examples build and cache correctly:

- `.#hello-cache` — autotools, single-output, no `configureSrcFilter`
  (whole-tree snapshot).
- `.#zstd-cache` — cmake, 4 outputs (out/bin/dev/man), no
  `configureSrcFilter`. No `extraPhase1Attrs` gen_html workaround
  needed (unlike zstd-dyndrv) — group A has no sandbox/shims, so
  zstd's own cmake graph exec-ing `contrib/gen_html` mid-build is just
  an ordinary native exec.
- `.#hello-cache-filtered` — autotools, WITH `configureSrcFilter` set
  (the `autotools` preset from `nix/configureSrcFilterPresets.nix`
  plus `existenceStubs = [ "src/hello.c" ]`). Proves the early-cutoff
  win for autotools — see below.
- `.#fmt-cache-filtered` — cmake, WITH `configureSrcFilter` set (the
  `cmake` preset plus fmt-specific patterns — see Gotcha #7). Proves
  the same win for cmake, on a package where it's actually possible —
  see Gotcha #7 for why `zstd` itself can't take this path.

Verified directly:
- `nix run`/build on all four produces real output.
- `zstd-cache`'s `bin/zstd` gets a **correct RPATH baked natively** by
  the linker — no patchelf/ELF-patching step needed at this boundary
  at all (unlike dynDrvStdenv's `elfRpathFixupScript`), since no
  binaries exist yet when group B's tree-restore + path-rewrite runs
  (which happens before buildPhase, not just before installPhase).
- **Group-B-only attr changes never rebuild group A**: added a
  group-B-only `installFlags` via `extraGroupBAttrs` (temporarily, for
  verification only — not committed), rebuilt, and confirmed via `nix
  show-derivation` that group A's `.drv` hash was byte-identical and
  it was NOT rebuilt — only group B's derivation rebuilt.
- **Real early-cutoff on SOURCE edits, not just attr changes**, for
  BOTH filtered examples (autotools and cmake) — see "How early-cutoff
  really works" below for the mechanism and Gotchas #5/#7 for what it
  took to get each build system's preset right:
  - `hello-cache-filtered`: edited `src/hello.c` (excluded by the
    `autotools` preset) via a controlled `overrideAttrs`-substituted
    `src` — group A's `ggtree` output resolved to the exact same store
    path before and after. Negative control: editing `configure.ac`
    (included) produced a different `ggtree` path.
  - `fmt-cache-filtered`: same test, `LICENSE` (excluded) vs
    `CMakeLists.txt` (included) — same result, ggtree path unchanged
    for the excluded edit, changed for the included one.

Build any of them:

```sh
NIX_CONFIG='
experimental-features = nix-command flakes ca-derivations dynamic-derivations configurable-impure-env
extra-system-features = builder-rpc-v0
store = local?root=/tmp/nixgg-scratch-store
' ./.patched-nix/bin/nix build --no-eval-cache --no-link --print-out-paths .#hello-cache
```

(`ca-derivations`/`dynamic-derivations`/`builder-rpc-v0` are in the
NIX_CONFIG above only because the flake's other examples need them —
configureCacheStdenv itself needs none of them.)

## How early-cutoff really works

The naive design (whole-tree snapshot, no source filtering) only
caches group-B-only ATTR changes (`installFlags` etc) — any edit to
ANY source file still invalidates group A, because
`postConfigure`'s snapshot literally `cp -a`s the whole tree,
patched sources included. Getting a real win on source edits needs
TWO things together:

1. **`configureSrcFilter`** (opt-in, `nix/configureSrcFilter.nix` +
   `nix/configureSrcFilterPresets.nix`): shrinks group A's own `src`
   input to only the files `configurePhase` reads, via a small
   separate CA derivation that copies a `find`-glob subset out of the
   real `src` — NOT `lib.fileset` (considered and rejected: it needs a
   real eval-time `Path`, and arbitrary `pkgs.foo.src` is usually an
   unrealized fetcher derivation; converting it would mean fetching
   the whole source during evaluation). An edit outside the filtered
   set never changes this filter derivation's OUTPUT content, so CA
   early-cutoff (see #2) means group A's `src` input doesn't change
   either, and group A never even re-runs.
2. **CA on group A's own output** (`__contentAddressed = true;
   outputHashMode = "nar";` on group A itself): the same early-cutoff
   mechanism, one level up — even if group A's input DID change (a
   real configure-relevant edit, or just build noise) but its actual
   output content is byte-identical, group A's output PATH stays the
   same, so group B's input doesn't change either.

Filters are explicit per-package opt-in ONLY, default `null` (no
filtering — today's always-safe whole-tree behavior). An
under-inclusive pattern list is a SILENT correctness bug (stale cached
group A output, no error), not a build failure — see the Gotchas
below for how deep this rabbit hole went for `hello` alone.

## Key files

- `nix/configureCacheStdenv.nix` — the whole mechanism, ~230 lines
  (simplified after an initial version had separate filtered/
  unfiltered code paths — see "Simplification" below). Read the top
  comment first.
- `nix/configureSrcFilter.nix` — the filter-derivation builder
  (shell `find`/glob copy, CA output, plus `existenceStubs` for
  AC_CONFIG_SRCDIR-style presence-only checks).
- `nix/configureSrcFilterPresets.nix` — starting-point pattern lists
  (`autotools`, `cmake`). Explicitly caveated NOT guaranteed correct —
  see Gotchas #5 and #7.
- `flake.nix` — `configureCacheStdenv` package def (~line 318),
  `configureCacheExamples` (~line 323, includes
  `hello-cache-filtered` and `fmt-cache-filtered`), and both
  `configureCacheStdenv` and `configureSrcFilterPresets` exposed as
  top-level flake outputs (same convention as `dynDrvStdenv`) so
  downstream flakes can use them without vendoring.

## Simplification: group B always unpacks for itself

The first working version had group B's restore branch on whether
`configureSrcFilter` was set: unfiltered used `dontUnpack=true` (group
A's snapshot was already the complete tree) and needed a separate
`.gg-sourceroot` file to recreate the right directory depth before
overlaying; filtered ran a real `unpackPhase`/`patchPhase` first. Two
code paths, only one exercised by each example — exactly the kind of
asymmetry that produced Gotcha #6 (a real regression only caught by
testing the *other* branch).

Simplified to ONE path: group B **always** runs its own real
`unpackPhase`/`patchPhase` against the real `src`, unconditionally,
then overlays group A's `ggtree` on top via `ggRestorePhase`. This is
cheap (tar extraction + patch application, not a build) and means
group B's own `sourceRoot` always matches reality — no
`.gg-sourceroot` bookkeeping, no `dontUnpack`/`phases` branching, no
"unfiltered" special case to keep in sync with the filtered one.
Re-verified after simplifying: all three examples still build/run,
and the group-B-only-attr-doesn't-rebuild-group-A property still
holds (checked directly via `nix show-derivation` — group A's `.drv`
hash was identical with/without a group-B-only `installFlags`).

## Gotchas actually worth remembering

1. **`__structuredAttrs` must be forced off in BOTH groups**, same as
   dynDrvStdenv. Under structured attrs, make-derivation.nix only
   honors env-nested attrs, not bare top-level ones — `dontBuild`,
   `doCheck`, `outputs`, `dontUnpack`, `dontConfigure` are all bare
   top-level attrs here. Confirmed directly: `hello` sets
   `__structuredAttrs = true`, and without forcing it off, `doCheck =
   false` in group A was silently ignored — checkPhase ran anyway and
   failed on a missing `makeinfo` (see Gotcha #2).

2. **The path-rewrite (`sed -i`) must preserve file mtimes.** Group A's
   configure baked its OWN real output paths into Makefiles/
   `cmake_install.cmake`/`.pc`/`.la` files. Group B has different real
   output paths for the same outputs, so every occurrence needs
   rewriting once, right after the tree restore. A naive `sed -i`
   gives the touched file a fresh mtime — and `hello`'s own `make
   check` compares `doc/Makefile`'s mtime against the checked-in
   `doc/hello.info` to decide whether to regenerate it via `makeinfo`
   (not on PATH in this sandbox — confirmed directly, this broke the
   very first build attempt with "makeinfo: command not found").
   Fixed by `touch -r`-capturing and restoring each file's mtime around
   the `sed -i` call. This is the same class of hazard nixpkgs' own
   `configurePhase` already guards against for libtool's `ltmain.sh`
   (setup.sh's `CONFIGURE_MTIME_REFERENCE` dance) — just hit at a
   different boundary.

3. **Group A keeps the package's REAL `outputs`, plus one extra
   `ggtree`.** Unlike dynDrvStdenv's phase 1 (forced to
   `outputs=["out"]` because of its `.drv`-suffixed submit-output
   naming requirement), group A has no such requirement — it's an
   ordinary derivation. Keeping real outputs gets
   `multiple-outputs.sh`'s own `_overrideFirst outputBin "bin" "out"`
   chain working with real, self-consistent paths for free — no
   placeholder-substitution trick needed anywhere in this file (a nice
   simplification vs. dynDrvStdenv's `outputPlaceholder`/
   `restoreOutputsScript`). The real outputs stay structurally
   required-but-empty (`installPhase` never runs in group A); `ggtree`
   carries the whole configured tree instead, same `.gg-cwd`-offset
   trick as dynDrvStdenv (needed because cmake's `mkdir build && cd
   build` means `$PWD` isn't `$NIX_BUILD_TOP`).

4. **No patchelf/ELF step needed at all, unlike dynDrvStdenv.** Because
   the split happens before `buildPhase`, group B's own linker reads
   the corrected install-path text at LINK time and bakes a correct
   RPATH natively. Confirmed directly on `zstd-cache`: `bin/zstd`'s
   RPATH points at group B's real `out` path with zero post-hoc
   patching.

5. **`configureSrcFilter` is a genuine rabbit hole — hello ALONE hit
   five distinct classes of missing-file bug** before working, each
   silent/confusing rather than an obvious "file not found":
   - **`AC_CONFIG_SRCDIR`'s presence check.** Autoconf bakes a `test
     -r "$srcdir/$ac_unique_file"` existence check into every generated
     `configure` — hello's is `src/hello.c`. Read for EXISTENCE only,
     never content — this is what `existenceStubs` is for (an empty
     placeholder, safe because nothing reads its content). Getting this
     wrong the OTHER way (forgetting to exclude the stub from group A's
     snapshot before it overlays onto group B) silently clobbers the
     REAL file with an empty one — group B's build then "succeeds" up
     to link time, which fails with "undefined reference to main"
     because `hello.o` was compiled from an empty `.c` file. This is
     the single most dangerous failure mode: it looks like a normal
     build error, not a caching bug.
   - **`*.in` templates are genuine CONTENT, not existence-only.**
     `config.status` substitutes `@VAR@` placeholders FROM `Makefile.in`
     INTO `Makefile` — excluding `Makefile.in` fails with "cannot find
     input file: 'Makefile.in'".
   - **Automake's non-recursive-Makefile `*.mk` fragments are real
     prerequisites of `Makefile.in`'s own regen rule** in the generated
     Makefile (hello has `lib/local.mk`/`doc/local.mk`/`lib/gnulib.mk`).
     Missing (not just stale) prerequisites make `make` unconditionally
     re-invoke `automake` regardless of any mtime — this fails outright
     since `automake` isn't on PATH in this sandbox, and looks
     identical to a genuine "you forgot a file" mistake rather than a
     make dependency-graph subtlety.
   - **GNU gettext's `po/` directory is a dense, package-specific web**
     (`Makevars`, `POTFILES.in`, `LINGUAS`, `*.header`, `*.sin`,
     `Rules-quot`, ...) — no small glob captures it. The `autotools`
     preset treats `po/` as all-or-nothing rather than trying to
     enumerate gettext's boilerplate filenames.
   - **Group A's own absolute build directory can be baked as literal
     TEXT**, separately from the store-path rewrite `pathRewriteScript`
     already handled. When `configureSrcFilter` is active, group A
     unpacks a DIFFERENTLY-NAMED derivation (the filter's own output,
     e.g. `hello-configure-src`) than group B's real unpack
     (`hello-2.12.3`) — automake's generated Makefile invokes
     `build-aux/missing` via group A's absolute `/build/<name>` path,
     which doesn't exist in group B's build directory at all. Fixed by
     a SEPARATE rewrite pass (`.gg-buildroot`) that runs before the
     store-path one.

   **Bottom line for anyone adding a new preset or wrapping a new
   package**: presets are STARTING POINTS, not guarantees. Verify by
   building, not by inspection — every one of the five bugs above
   looked like an unrelated, ordinary build failure until traced back
   to a missing file.

6. **The old unfiltered code path needed its OWN directory-depth fix**
   — this was the exact asymmetry the later simplification (see
   "Simplification" above) eliminated. Group A's snapshot used to
   `cp -a` from bare `$NIX_BUILD_TOP`; changed to snapshot from
   `$NIX_BUILD_TOP/$sourceRoot` instead (needed so the filtered case's
   differently-named sourceRoot didn't matter — Gotcha #5's last
   bullet). But the OLD unfiltered restore never ran its own
   `unpackPhase` (it used `dontUnpack=true`, since group A's ggtree
   was already the complete tree) — so nothing recreated the
   `sourceRoot` directory to restore INTO, and the overlay landed one
   directory level too shallow. Confirmed directly: broke `zstd-cache`
   (cmake's own baked absolute build-dir paths, e.g. dependency-file
   directories under `build/lib/common/`, silently didn't exist —
   "fatal error: opening dependency file ...: No such file or
   directory"). Worked around at the time with a `.gg-sourceroot`
   file the unfiltered branch would `mkdir -p`+`cd` into before the
   overlay — but the REAL fix, applied later, was removing the
   asymmetry entirely: group B now always runs its own unpack+patch,
   so there's no "unfiltered case" left to special-case.

7. **cmake's own configure-time `file(GLOB ...)` calls can make
   filtering structurally impossible for a given package.** Tried
   `zstd` first for the `cmake` preset — its `CMakeLists.txt`
   `file(GLOB CommonSources lib/common/*.c)`-style calls (source lists
   for the library, tests, and every contrib tool) mean configure
   itself needs the ENTIRE `lib/`, `programs/`, `tests/`, `contrib/`
   tree present just to enumerate what it'll build later — there's no
   smaller filter that helps, because "the files configure reads" is
   effectively "everything." `fmt` uses explicit source lists instead
   (`set(FMT_SOURCES src/format.cc)`), so filtering it down actually
   means something. Getting `fmt` working also needed patterns beyond
   the generic `cmake` preset: `src/*.cc` and `include/fmt/*.h`
   (the real library sources/headers `add_library()` needs at
   configure time), `README.md`/`ChangeLog.md` (baked directly into
   the same `add_library()` call), `support/cmake/*.in` (pkg-config
   and cmake-config templates `configure_file()` reads), and `test/`
   wholesale (BUILD_TESTING defaults on, and `test/CMakeLists.txt`
   enumerates a dozen individual test targets, each needing its own
   source present — same "not worth chasing individually" tradeoff as
   autotools' `po/`). This is the general lesson: whether
   `configureSrcFilter` can help AT ALL for a given cmake package
   depends on whether that package's own `CMakeLists.txt` globs its
   sources or lists them explicitly — the tool doesn't have visibility
   into that on its own, and there's no way to tell without trying.

## What's NOT done / open threads

- Scoped to exactly 2 groups (configure | build..dist) for v1, per the
  user's explicit priority ("configurePhase... that's the most
  expensive and is the phase that matters"). NOT a general N-way
  phase splitter — task #47's original framing suggested that, but
  it's deliberately deferred.

  Investigated whether a build/install split (caching buildPhase
  itself, so e.g. an `installFlags`-only change skips recompilation
  too) could reuse this same plain-CA-derivation pattern. It can't,
  cleanly: unlike the configure/build boundary, real ELF binaries
  exist by the time build finishes, and they have RPATH baked in at
  LINK time pointing at that group's own output paths — text
  substitution (`pathRewriteScript`'s `sed`) can't touch ELF dynamic
  sections, so a build/install split would need real `patchelf`-based
  rewriting, the same class of mechanism `dynDrvStdenv`'s
  `elfRpathFixupScript` already solves — but not reusable verbatim,
  since that script's glue is tied to dynDrvStdenv's specific
  placeholder-output scheme, not a general "N real outputs, two
  groups" shape. Confirmed via nixpkgs' own `patchelf` setup-hook
  (`fixupPhase`'s `fixupOutputHooks`): it only *shrinks* RPATH
  (drops entries with no `DT_NEEDED` in the dir), it never adds or
  corrects one — so a naive restore-without-ELF-rewrite would silently
  produce a binary missing a real runtime dependency, not a build
  failure. Net: a build/install split is much closer in complexity to
  `dynDrvStdenv`'s than to this file's — plain derivations plus a new
  ELF-rewrite pass, not a copy-paste of either existing mechanism.
  Worth it only if buildPhase-level caching turns out to matter enough
  in practice to justify that cost; not started.
- `extraGroupBAttrs` is basically unnecessary for the same reason
  dynDrvStdenv's `extraPhase2Attrs` is — group B IS reachable via a
  plain `.overrideAttrs` on the returned package. Kept for symmetry.
- The `cmake` preset in `configureSrcFilterPresets.nix` is now
  verified — against `fmt`, not `zstd` (see Gotcha #7 for why zstd
  can't use it at all). Any OTHER cmake package's own configure-time
  file reads are still unknown until tried; the preset is a starting
  point, not a guarantee, same as always.
- Only tested against hello/zstd/fmt (same three build-system shapes
  dynDrvStdenv used for its first pass, plus fmt). Untested: packages whose
  configure step itself execs a just-built tool (the "exec one of its
  own binaries mid-build" case dynDrvStdenv's zstd-dyndrv example had
  to work around) — a configure-time equivalent might exist in some
  package but hasn't been hit yet.
- Test coverage exists at two levels: `tests/smoke.sh`'s `CONFIGCACHE`
  set (hello-cache/zstd-cache/hello-cache-filtered/fmt-cache-filtered)
  checks "does it still build and run", same `nix run`-only
  verification pattern as `DYNDRV`. `tests/configure-cache-cutoff.sh`
  checks the actual caching mechanism: for `hello` and `fmt`, it
  constructs the package three ways (baseline, an edit to a file
  `configureSrcFilter` excludes, an edit to a file it includes) and
  asserts group A's `ggtree` output path is unchanged for the excluded
  edit and changed for the included one — automating exactly the
  manual `nix show-derivation`/build-path comparisons this doc
  describes above. Verified the negative-case detection works too:
  temporarily pointing the "excluded" edit at a file the filter
  actually includes made the script fail with the expected message,
  not silently pass.
