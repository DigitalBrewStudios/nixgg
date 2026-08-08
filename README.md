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
extracted lua source tree, and vice versa.

That holds by construction rather than by discipline: the build
command is rendered once, in Go, and sandbox mode bakes it into a
JSON drv while native mode passes the same text through a thunk for
`nix/resolve-script.nix` to fill in the few values only Nix knows at
eval time. The two modes cannot disagree about flag quoting or
argument order because there is only one place that decides either.

Two tests, covering different failure modes:

- [tests/drv-equivalence.sh](tests/drv-equivalence.sh) — the invariant.
  149 drvs across five fixtures: `hello` (3), `lua` (37), `fmt` (3),
  `mosh` (38), `gcc` (68), every one matching byte-for-byte between the
  two modes. ~25 min; `ONLY=hello` is a 35-second smoke of the same
  machinery.
- [tests/smoke.sh](tests/smoke.sh) — every example builds, its artifact
  is at the FHS path it should be, and it runs. ~2 min;
  `EXAMPLES=all` adds redis, ffmpeg and llvm.

The second exists because the first structurally cannot catch a whole
class of bug: it compares drv *hashes* and never realises an output, so
it stayed green at 149/149 while a change to output placement left
native mode unable to collect any artifact at all.

### Invoking sandbox mode explicitly

The `nix build .#hello` above assumes you are inside `nix develop`,
which supplies the patched Nix and the experimental features. To run it
from an arbitrary shell, spell out all four:

```sh
nix build .#patched-nix -o .patched-nix     # one-time; substituted from cache

./.patched-nix/bin/nix build .#hello -Lv \
  --store 'local?root=/tmp/incremental' \
  --extra-experimental-features "ca-derivations dynamic-derivations" \
  --extra-system-features builder-rpc-v0
```

Every part of that is load-bearing:

- **`./.patched-nix/bin/nix`** — it must be the patched Nix, not
  whatever is on `PATH`. Your system Nix will get surprisingly far (it
  evaluates the flake and starts the outer derivation) and then fail
  inside the build with

  ```
  error: Submit outputs for a currently running derivation
         not supported by store 'local'
  ```

  because `nix store submit-output` does not exist in it.
- **`--store 'local?root=…'`** — an alternative store. Sandbox mode
  registers derivations from inside a running build, which a normal
  daemon store refuses.
- **`ca-derivations dynamic-derivations`** — content-addressed outputs
  and `builtins.outputOf`. Note the **plural** in `ca-derivations`;
  Nix treats an unknown feature name as a warning, not an error, so a
  typo here fails later and confusingly.
- **`--extra-system-features builder-rpc-v0`** — `mkNixggBuild` sets
  `requiredSystemFeatures = [ "builder-rpc-v0" ]`, so without this the
  derivation is simply unbuildable on this machine.
- **`-Lv`** is optional, but sandbox mode does its interesting work
  inside a build, so without it you see none of the `[nixgg]` lines.

One wrinkle worth knowing: `result` symlinks to a `/nix/store/…` path
that does not exist on your real filesystem, because the artifact lives
under the alt-store root. Read it there instead:

```sh
/tmp/incremental/nix/store/…-bin-hello/bin/hello
```

## Use it in your own project

nixgg is a flake input. Pull in `mkNixggBuild` and call it with your
own source, target, and build command — same function every example
in this repo uses.

Two things a consuming flake needs beyond the call itself: the
experimental features that dynamic derivations require, and a Nix that
can serve `builder-rpc-v0`. Both are shown below.

```nix
# flake.nix
{
  inputs.nixgg.url = "github:tomberek/nixgg";

  # mkNixggBuild's output is a `builtins.outputOf` node, and its outer
  # derivation asks for the builder-rpc-v0 system feature. Without
  # these, `nix build` fails at eval with "experimental Nix feature
  # 'dynamic-derivations' is disabled". Nix prompts you to trust these
  # the first time; after that it Just Works.
  nixConfig = {
    extra-experimental-features = [
      "ca-derivations"
      "dynamic-derivations"
      "configurable-impure-env"
    ];
    extra-system-features = [ "builder-rpc-v0" ];
  };

  outputs = { self, nixpkgs, nixgg }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
      mkNixggBuild = nixgg.packages.${system}.mkNixggBuild;
    in
    {
      packages.${system}.default = (mkNixggBuild {
        pname = "myproject";
        version = "0.1.0";
        src = ./.;
        target = "myproject";      # basename the link shim submits as `out`
        nativeBuildInputs = [ pkgs.pkg-config ];
        buildInputs = [ pkgs.zlib ];
        buildCommand = ''
          make -j"$NIX_BUILD_CORES"
        '';
      }).result;
    };
}
```

You also need a Nix that implements `builder-rpc-v0` and `nix store
submit-output` — that work lives on NixOS/nix master and is not in a
released Nix yet. This flake exposes the build it pins:

```sh
# One-time: get a capable nix (substituted from cache.nixos.org).
nix build github:tomberek/nixgg#patched-nix -o ./.patched-nix

# Build your project with it.
./.patched-nix/bin/nix build .
```

Stock Nix is fine for nixgg's **native** mode (thunks on disk, one
`nix build` at the end); the patched Nix is only needed to consume a
`mkNixggBuild` result, which is sandbox mode.

See `examples/*/default.nix` for real-world call sites (lua, {fmt},
mosh, redis, ffmpeg, and a 3-phase LLVM build). If your build execs one
of its own binaries mid-build — codegen and bootstrap tools do this —
read `examples/llvm/default.nix`: that needs two or more chained
`mkNixggBuild` calls, since a not-yet-realised output can't be run.

`mkNixggBuild`'s parameters:

| param | required | meaning |
|---|---|---|
| `pname` | yes | naming only |
| `version` | no (default `"0"`) | naming only |
| `src` | yes | the source tree |
| `target` | yes | path of the final artifact; its basename is matched against the link/archive shim's `-o` to decide what gets submitted as the derivation's output |
| `buildCommand` | yes | shell run inside the sandbox once shims are on `PATH` — typically `make`/`cmake --build`/`ninja` |
| `nativeBuildInputs` | no | build-time tools (compilers, generators, `pkg-config`) |
| `buildInputs` | no | libraries the build links against |
| `propagatedBuildInputs` | no | passed through to the underlying `stdenv.mkDerivation` |

Every `mkNixggBuild` call also returns a `.shell` — a plain `mkShell`
mirroring the sandbox's exact `stdenv` environment. `nix develop` into
it when you need to reproduce a sandbox build by hand; it's what
`tests/drv-equivalence.sh` uses to run the native side under the same
tool env.

## Architecture

Every shim writes a derivation. Nix does the rest. See
[ARCHITECTURE.md](ARCHITECTURE.md) for the design and the shim
mechanics. See [dyn-drv/NOTES.md](dyn-drv/NOTES.md) for the
sandbox / dynamic-derivation exploration notes.

## Requirements

- Nix ≥ 2.36 for sandbox mode (needs `builder-rpc-v0` + `nix store
  submit-output`, both merged into NixOS/nix master via
  [#15793][pr-15793]). Native mode works with older Nix.
- The flake pins its own Nix build; `nix develop` bootstraps it, or
  `nix build .#patched-nix` if you would rather invoke it directly —
  see [Invoking sandbox mode explicitly](#invoking-sandbox-mode-explicitly)
  for the full flag set and what each flag is for.

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
