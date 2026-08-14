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

- **Task #47** (tracked in this session's task list, not yet started):
  investigate a coarser "one dynamic derivation per stdenv phase"
  design as an alternative to per-TU — may be useful where full TU
  tracking is overkill.
- `extraPhase2Attrs` exists for symmetry with `extraPhase1Attrs` but
  is basically unnecessary — phase 2 IS reachable via a plain
  `.overrideAttrs` on the returned package. Not deeply tested; if it
  turns out redundant, could be dropped.
- Only tested against hello/mosh/zstd. Untested: packages with
  multiple mid-build-exec hazards, packages needing native (non-cmake,
  non-autotools) build systems, cross-compilation.
- No test suite for `dynDrvStdenv.nix` itself (unlike `mkNixggBuild`,
  which has `tests/drv-equivalence.sh`) — every verification this
  session was a manual `nix build` + log inspection. Worth automating
  if this gets more use.
- Session cwd note: this repo (`~/nixgg`, branch `master`) is where
  all of this work happened. A SEPARATE checkout at
  `~/incremental/nixgg` (branch `nixgg-go`) has unrelated pending
  work (`example/util.cc` edit, untracked `spdlog/`/`zstd/` clones)
  — don't confuse the two.

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
