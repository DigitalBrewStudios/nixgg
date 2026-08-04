# nixgg

> A [gg][gg]-style build accelerator built on Nix content-addressed
> derivations.

Intercept every `-c` compile, `ar` archive, and link with a shim.
Turn each invocation into a content-addressed Nix derivation. Nix
decides what's cached and what needs building — nixgg just
constructs the expressions.

Two modes for producing derivations, same drv-hashes either way:

- **Native** — shims write `.nix` thunk files on disk, one `nix build`
  at the end. Works with any recent Nix daemon.
- **Sandbox / dyn-drv** — shims call `nix derivation add` inside a
  `builder-rpc-v0` sandbox and submit the final drv as the outer
  derivation's output. `nix build .#hello` Just Works.

## Try it

```sh
# 1. Enter a shim-enabled shell.
nix develop

# 2. Native: your normal build system, drvs materialised on demand.
cd example
make          # compiles + auto-force-links via NIXGG_AUTOFORCE=1
./hello

# 3. Sandbox: same source, whole graph as dynamic Nix derivations.
nix build .#hello
./result/hello

# 4. Real projects, sandbox mode, out-of-tree sources pinned in flake.lock.
nix build .#lua         # lua 5.4.7 — 32 TUs, 1 archive, 1 link
nix build .#fmt         # {fmt} 11.0.2 — cmake + ninja + libfmt.a
nix build .#mosh        # mosh unstable — autoconf + protobuf + openssl/ncurses/zlib
```

Both modes produce byte-identical `.drv` files. `nix build .#lua`
gets an instant cache hit from an earlier native build in an
extracted lua source tree, and vice versa. The equivalence is pinned
by [tests/drv-equivalence.sh](tests/drv-equivalence.sh), which
currently covers all four fixtures: `hello` (3 drvs), `lua` (37),
`fmt` (3), `mosh` (38) — every drv matches byte-for-byte between the
two modes.

## Architecture

Every shim writes a derivation. Nix does the rest. See
[ARCHITECTURE.md](ARCHITECTURE.md) for the design and the shim
mechanics. See [dyn-drv/NOTES.md](dyn-drv/NOTES.md) for the
sandbox / dynamic-derivation exploration notes.

The equivalence between native and sandbox modes is pinned by
[tests/drv-equivalence.sh](tests/drv-equivalence.sh) — a regression
test that builds the same source both ways and asserts the resulting
drv-store-paths match byte-for-byte.

## Requirements

- Nix ≥ 2.36 for sandbox mode (needs `builder-rpc-v0` + `nix store
  submit-output`, both merged into NixOS/nix master via
  [#15793][pr-15793]). Native mode works with older Nix.
- The flake pins its own Nix build; `nix develop` bootstraps it.

[pr-15793]: https://github.com/NixOS/nix/pull/15793

## Prior art

Inspired by **[gg][gg]** (Stanford SNR, [ATC '19: *From Laptop to
Lambda*][gg-paper]). gg models every build step as a
content-addressed thunk that a scheduler can dispatch to a cluster
of workers. nixgg keeps the model but drops the scheduler in favor
of the one nixOS already ships — the Nix store, its evaluator, and
its remote-build machinery. Every gg thunk becomes a Nix derivation;
every gg fingerprint becomes a Nix output path.

Related work in the same shape:

- **[nix-ninja](https://github.com/pdtpartners/nix-ninja)** —
  emits dynamic derivations from Ninja build graphs. Similar
  sandbox mechanism (builder-rpc-v0), rust implementation, targets
  the meson/cmake→ninja pipeline.
- **[sandstone](https://github.com/obsidiansystems/sandstone)** —
  Haskell-module-per-derivation via `recursive-nix`.
- **[NixOS/nix#15793](https://github.com/NixOS/nix/pull/15793)** —
  the upstream PR that added `builder-rpc-v0` + `nix store
  submit-output`. Now merged into master.

## License

MIT. See [LICENSE](LICENSE).

[gg]: https://github.com/StanfordSNR/gg
[gg-paper]: https://www.usenix.org/conference/atc19/presentation/fouladi
