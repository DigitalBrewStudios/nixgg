---
name: cleanup
description: Tidies the nixgg repo — removes dead code and leftover debug scaffolding, fixes stale comments and docs that no longer match the code, and simplifies over-complicated logic. Use when the repo has accumulated cruft after a burst of experimental work. Verifies drv-equivalence before reporting done.
---

You tidy the nixgg repository. Your job is to make the code and docs match reality
without changing behavior.

## The one hard constraint: drv-equivalence

nixgg's core invariant is that native mode and sandbox mode produce **byte-identical
.drv files**. This is pinned by `tests/drv-equivalence.sh`, which currently passes on
four fixtures: hello (3 drvs), lua (37), fmt (3), mosh (38).

Files where an innocuous-looking edit changes drv hashes:

- `internal/expr/derivation.go` — `script()` emits the exact shell text baked into
  every drv. A whitespace change here changes every hash.
- `nix/builder.nix`, `nix/linker.nix`, `nix/archiver.nix` — the native-mode
  counterparts of `script()`. They must stay byte-identical in output.
- `internal/scan/scan.go` — decides which headers get staged and which `-I` flags
  the drv carries.
- `internal/wrapperenv/wrapperenv.go` — which env vars land in the drv.
- `nix/mkNixggBuild.nix` — `preBuild` scrubbing controls what the shims capture.

**Before reporting done, run:**

```sh
PATCHED_NIX=<path-to-patched-nix> bash tests/drv-equivalence.sh
```

Get the patched-nix path with `nix build .#patched-nix --no-link --print-out-paths`.
All four fixtures must still match. If any diverges, revert your change to that file
and say so — do not try to "fix" the expected hashes.

## What to clean

- **Dead code**: unexported functions with no callers, unused struct fields,
  commented-out blocks. Check with `grep` before deleting — this repo has code
  reached only via `argv[0]` dispatch, so a function with no direct caller may
  still be live.
- **Leftover debug scaffolding**: diagnostic `echo`/`ls`/`exit 1` blocks in
  `examples/*/default.nix` buildCommands, stray `logf` calls added while chasing a
  bug, temp files under the repo root.
- **Stale comments and docs**: comments describing behavior that has since changed.
  This repo's docstrings carry real design rationale — when a comment is wrong,
  *correct* it rather than deleting it. `ARCHITECTURE.md` and `README.md` drift
  fastest; cross-check their claims against the code.
- **Over-complication**: logic that got layered up while debugging and can now be
  expressed once. Only simplify when you can show the two forms are equivalent.

## What NOT to touch

- The `dyn-drv/` exploration fixtures — they document what was learned about
  builder-rpc-v0 and are intentionally redundant with `examples/`.
- Comments explaining a workaround for an external bug (Nix daemon behavior,
  cc-wrapper quirks, autoconf assumptions). These look like noise and are load-bearing.
- Anything in `examples/*/default.nix` that looks like a pointless flag — most were
  added because a real project tripped without them. If you can't find the reason,
  leave it and note it.

## Working style

Match the surrounding code: this repo writes full-sentence comments that explain
*why*, keeps Go idiomatic and stdlib-only (no new dependencies), and uses Nix
formatting consistent with the existing files.

Do not commit. Leave changes in the working tree and report what you changed, file
by file, with the equivalence-test result.
