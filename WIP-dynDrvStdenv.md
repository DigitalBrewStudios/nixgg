# dynDrvStdenv — work in progress

Context for resuming this thread in a fresh session. See
`nix/dynDrvStdenv.nix`'s own top comment and README.md's "Upgrade an
existing nixpkgs package" section for the user-facing design; this
doc is about *state*, not design.

## What this is

`dynDrvStdenv` lets you take an EXISTING nixpkgs package and run it
through nixgg's builder-rpc-v0 sandbox with real per-translation-unit
shim acceleration, via a one-line stdenv override:

```nix
pkgs.hello.override {
  stdenv = dynDrvStdenv { stdenv = pkgs.stdenv; };
}
```

It splits `stdenv.mkDerivation` into two real derivations: phase 1
(unpack..build, sandboxed, shims live) and phase 2 (check..dist,
ordinary derivation, seeded from phase 1's resolved tree).

## Current status: working, committed

Three examples build with **real per-TU acceleration** (not just
phase-split plumbing — actual cc/c++/ar calls become dynamic
derivations):

- `.#hello-dyndrv` — autotools, 85 shim invocations, `doCheck`
  postInstallCheck passes.
- `.#mosh-dyndrv` — autotools + `autoreconfHook`, 38 shim invocations.
- `.#zstd-dyndrv` — cmake, 4 outputs, custom `checkPhase` (real
  `ctest` passes), PLUS the "exec one of its own binaries mid-build"
  case (`contrib/gen_html`) fixed via phase-chaining +
  `extraPhase1Attrs`. 92 shim invocations.

Build any of them:

```sh
NIX_CONFIG='
experimental-features = nix-command flakes ca-derivations dynamic-derivations configurable-impure-env
extra-system-features = builder-rpc-v0
store = local?root=/tmp/nixgg-scratch-store
' ./.patched-nix/bin/nix build --no-eval-cache --no-link --print-out-paths .#hello-dyndrv
```

(Any fresh `/tmp/...` alt-store works; the ambient store also works
via `nix build .#hello-dyndrv` directly once the flake's `nixConfig`
is trusted.)

Relevant commits, newest first: `1ff3585` (comment trim — same
behavior), `ca99950` (the real work: per-TU acceleration +
`extraPhase1Attrs`), `a67452a`, `4cabf68`.

## Key files

- `nix/dynDrvStdenv.nix` — the whole mechanism. ~290 lines, comments
  trimmed to essentials; read the top comment first.
- `go/internal/assemble/` — walks a build tree for drvref stubs
  (`Walk`), stages it for `nix store add --scan` (`StageForScan`),
  builds the resolving JSON drv (`Build`).
- `go/internal/cli/assemble.go` — `nixgg assemble <root> <name>` CLI
  command, phase 1's `postBuild` hook calls this.
- `go/internal/scan/scan.go` — header scanner. Uses `gcc -M` (NOT
  `-MM` — see "Gotchas" below).
- `examples/zstd-dyndrv/default.nix` — the `extraPhase1Attrs`
  phase-chaining demo (gen_html).
- `flake.nix` — `dynDrvStdenv` package def (~line 254),
  `dynDrvExamples` (~line 296).

## Gotchas actually worth remembering

1. **`-M` not `-MM` in the header scanner.** `gcc -MM` omits headers
   gcc classifies as "system" (anything reached via a directory that
   shadows gcc's own system search path) — which is exactly gnulib's
   `-I`-shadowing trick for headers like `stddef.h`. `-MM` silently
   dropped `stddef.h` from one TU's dep list but not another's,
   producing a "gl_unreachable undeclared" error nowhere near the
   real cause. Fixed by switching to `-M` (still filters `/nix/store/`
   paths downstream, so this didn't bloat anything).

2. **Shims must go on PATH from `postPatch`, not `preBuild`.** cmake's
   own `configurePhase` bakes an ABSOLUTE compiler path into its
   generated Makefile. If the shim isn't on PATH yet at configure
   time, cmake bakes the real `gcc-wrapper` path and buildPhase never
   routes through the shim at all — build succeeds, zero stubs, **no
   error whatsoever**. This is the single easiest way to silently
   regress "acceleration" back to a no-op. `NIXGG_BYPASS` still needs
   to stay set through configure; only `preBuild` unsets it.

3. **`.overrideAttrs` can never patch phase 1.** nixpkgs'
   `.override`/`.overrideAttrs` reapplication contract always
   rebuilds the wrapped package from its *original* attrs first —
   phase 1 is closed over inside that rebuild, before any
   `.overrideAttrs` the caller wrote gets a chance to run. Confirmed
   directly: both orderings produced a byte-identical phase-1
   derivation hash to the fully unpatched build. The only way to
   patch phase 1 from outside is `extraPhase1Attrs`, passed at the
   `dynDrvStdenv { ...; }` call site itself.

4. **`checkPhase` needs a real, always-run phase to restore the tree
   before it, not a `preCheck` hook.** `preCheck`/`preInstall` hooks
   are attached to `checkPhase`/`installPhase`, which `runPhase`'s own
   guard skips ENTIRELY when `doCheck`/`dontInstall` says so — the
   common case for most packages. A custom phase name (`ggRestorePhase`
   in phase 2) has no such gate, so it always runs regardless.

5. **`StageForScan`'s staging dir must live INSIDE the tree it's
   staging, at a fixed excluded name (`.gg-stage`), not under
   `os.MkdirTemp("", ...)`.** `$TMPDIR` inside a builder-rpc-v0
   sandbox resolves under `$NIX_BUILD_TOP` (the tree itself), so an
   externally-supplied dest can become a descendant of root — copying
   root into itself recurses until "file name too long".

6. **`nix store add --scan` chokes on `.nix-socket`** — builder-rpc-v0's
   own live unix socket, present in `$NIX_BUILD_TOP`. Can't delete it
   before scanning either — the RPC calls that follow go through it.
   `StageForScan` excludes it by name.

## What's NOT done / open threads

- **Task #47 — resolved, but as a SEPARATE mechanism, not a change to
  this file.** "One dynamic derivation per stdenv phase" turned out to
  need no dynamic-derivation machinery at all for the configure/build
  boundary specifically (no per-TU discovery happens there, so no
  RPC/sandbox/shims needed) — see `nix/configureCacheStdenv.nix` and
  `WIP-configureCacheStdenv.md`, its own sibling doc.
- `extraPhase2Attrs` exists for symmetry with `extraPhase1Attrs` but
  is basically unnecessary — phase 2 IS reachable via a plain
  `.overrideAttrs` on the returned package. Not deeply tested; if it
  turns out redundant, could be dropped.
- Only tested against hello/mosh/zstd. Untested: packages with
  multiple mid-build-exec hazards, packages needing native (non-cmake,
  non-autotools) build systems, cross-compilation.
- Test coverage now exists: `tests/smoke.sh` runs hello-dyndrv,
  mosh-dyndrv, zstd-dyndrv alongside the mkNixggBuild examples (quick
  set by default, no opt-in needed) — see "Test suite" below.
- Session cwd note: this repo (`~/nixgg`, branch `master`) is where
  all of this work happened. A SEPARATE checkout at
  `~/incremental/nixgg` (branch `nixgg-go`) has unrelated pending
  work (`example/util.cc` edit, untracked `spdlog/`/`zstd/` clones)
  — don't confuse the two.

## Two bugs found and fixed by writing the test suite

Both only bite **multi-output** packages (zstd-dyndrv is the only
current example with `outputs != ["out"]`) — hello-dyndrv/mosh-dyndrv
were never affected, since they're single-output already.

1. **Multi-output collapse.** Phase 1 must declare exactly one
   derivation output (`outputs = ["out"]"`, required by the
   `.drv`-suffixed submit-output naming convention), but that also
   meant `$bin`/`$dev`/`$man` etc. never existed as env vars before
   configurePhase ran — and `multiple-outputs.sh`'s
   `_overrideFirst outputBin "bin" "out"` chain silently collapses to
   `$out` whenever the preferred var is unset. cmake baked `$out`-only
   install paths into `cmake_install.cmake`; phase 2's real install
   (with the real multi-output env) then wrote into those same
   `/nonexistent`-baked paths, and the old `postInstall` only ever
   copied into `$out`. Result: zstd-dyndrv's `bin` output existed but
   was **empty** — `nix run .#zstd-dyndrv` failed with "No such file
   or directory" even though the build succeeded.

   Fix: give every real non-`out` output its own placeholder path
   (`/nonexistent-bin`, `/nonexistent-man`, ...) as a plain env var in
   phase 1 (not a declared derivation output — phase 1 stays
   single-output), so `multiple-outputs.sh` picks each one up instead
   of collapsing. Phase 2's `postInstall` now splits the restored tree
   back apart per real output (`restoreOutputsScript` in
   `dynDrvStdenv.nix`).

2. **Dangling self-rpath.** cc-wrapper's `ld-wrapper.sh` bakes a
   self-rpath into every linked binary/shared-lib AT LINK TIME (phase
   1's buildPhase) — using whatever placeholder path was live then
   (e.g. `/nonexistent/lib` for zstd's `libzstd.so`). That placeholder
   survives verbatim into phase 2; fixupPhase's own patchelf-based
   rpath-shrinking (correctly) drops any rpath entry pointing
   somewhere nonexistent. Result: `bin/zstd` couldn't find
   `libzstd.so.1` at runtime — "cannot open shared object file" — even
   on an otherwise-correct build.

   Fix: a new `preFixup` step (`elfRpathFixupScript`) walks every file
   under every real output and `patchelf --set-rpath`s each
   placeholder substring to the corresponding real output path,
   BEFORE fixupPhase's own rpath-shrink runs. Longest-placeholder-first
   substitution order matters (`/nonexistent` is a literal prefix of
   `/nonexistent-bin`).

Both fixes are in `nix/dynDrvStdenv.nix`; verified directly via `nix
run .#zstd-dyndrv -- --version` (prints the real CLI banner) and
zstd's own `ctest`-based checkPhase still passing post-fix.

## Test suite

`tests/smoke.sh` now covers all three dynDrv examples
(hello-dyndrv/mosh-dyndrv/zstd-dyndrv), always, as part of the default
`EXAMPLES=quick` run — no separate opt-in needed:

```sh
tests/smoke.sh                                    # QUICK + all 3 dyndrv examples
EXAMPLES="zstd-dyndrv" tests/smoke.sh              # just one
```

Key difference from the QUICK/SLOW mkNixggBuild fixtures: dyndrv
examples are verified via `nix run`/`nix shell` ONLY, never a direct
exec of the predicted `$ALT_STORE` disk path. Their baked
RPATH/RUNPATH entries point at real (non-fixed-output) sibling store
paths that exist only inside `$ALT_STORE`, not at the real filesystem
root — the dynamic loader always resolves an absolute RPATH against
the real root, so direct exec fails with "cannot open shared object
file" even on a perfectly correct build. Only `nix run`'s private
mount namespace (bind-mounts `$ALT_STORE` onto `/nix/store`) resolves
it. `meta.mainProgram` presence is also NOT checked for dyndrv
entries — that's a property of whatever upstream package got wrapped,
not something `dynDrvStdenv` controls (confirmed: upstream nixpkgs'
own `mosh` package sets no `mainProgram` at all).

Also fixed while writing this: `check_example`'s build-output capture
used to `tail -1` a single line, silently picking the WRONG realized
output for any multi-output default build (`nix build
--print-out-paths` prints one line per output, order not guaranteed)
— now scans all printed lines for the one that actually has the
wanted path.

No CI workflow exists yet (no `.github/workflows/`, no `.yml`/`.yaml`
anywhere in the repo) — `tests/smoke.sh` and `tests/drv-equivalence.sh`
are locally-run only for now.

## How to verify a build actually got accelerated

Don't just check exit code — a cmake gotcha (#2 above) can make a
build succeed with zero real acceleration. Check the shim count in
phase 1's log:

```sh
nix log /nix/store/<hash>-gg-build-<name>.drv.drv \
  | grep -c '\[nixgg\]   compile\|\[nixgg\]   archive\|\[nixgg\]   link'
```

Should be nonzero (and roughly equal to the real TU count). Also
check `nixgg assemble`'s own stub count in the same log
(`grep '\[nixgg assemble\]'`).
