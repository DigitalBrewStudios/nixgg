#!/usr/bin/env bash
# Smoke test: every example still builds, and its artifact is where we
# say it is and actually runs.
#
# This exists because tests/drv-equivalence.sh structurally cannot catch
# a whole class of bug. That test compares drv HASHES between native and
# sandbox mode; it never realises an output and never reads one. So when
# link outputs moved to $out/bin, it reported a clean 149/149 while every
# native build failed to collect its artifact and two phase-chaining
# examples referenced tool paths that no longer existed.
#
# It also covers the four examples drv-equivalence leaves out. That
# omission is structural, not an oversight: the gate resolves each
# fixture's native source from a single flake INPUT, and llvm/two-phase
# have no single `src` — they are multi-phase, one source per phase.
#
# Cheap by default (EXAMPLES=quick, ~2 min). The expensive ones are
# opt-in because llvm alone is ~1500 TUs.
#
# Usage:
#   tests/smoke.sh                 # quick set
#   EXAMPLES=all tests/smoke.sh    # everything, including llvm (~1h+)
#   EXAMPLES="hello gcc" tests/smoke.sh
#   ALT_STORE=/tmp/my-store tests/smoke.sh

set -uo pipefail

here="$(cd "$(dirname "$0")" && pwd)"
nixgg_root="$(cd "$here/.." && pwd)"

ALT_STORE="${ALT_STORE:-/tmp/nixgg-smoke-store}"
PATCHED_NIX="${PATCHED_NIX:-$nixgg_root/.patched-nix}"
if [[ ! -x "$PATCHED_NIX/bin/nix" ]]; then
  echo "==> building patched nix (one-time)" >&2
  nix build --no-eval-cache "$nixgg_root#patched-nix" -o "$PATCHED_NIX" >&2 || exit 2
fi
mkdir -p "$ALT_STORE"

export NIX_REMOTE=""
export NIX_CONFIG="
experimental-features = nix-command flakes ca-derivations dynamic-derivations configurable-impure-env
extra-system-features = builder-rpc-v0
store = local?root=$ALT_STORE
"

# attr | expected path inside $out | how to prove it works
#
# The expected path is the point of this test: it encodes the FHS layout
# (link -> bin/, ar -> lib/) so a change to output placement that forgets
# a consumer fails here loudly instead of in a user's terminal.
#
# "-" as the run command means the artifact is a library: existing at the
# right path with non-zero size is all we assert.
QUICK=(
  "hello|bin/hello|%s"
  "two-phase|bin/app|%s"
  "fmt|lib/libfmt.a|-"
  "lua|bin/lua|%s -v"
  "gcc|lib/libiberty.a|-"
  "mosh|bin/mosh-server|%s --version"
)
SLOW=(
  "redis|bin/redis-server|%s --version"
  "ffmpeg|bin/ffmpeg_g|%s -version"
  "llvm|bin/llc|%s --version"
)

case "${EXAMPLES:-quick}" in
  quick) SET=("${QUICK[@]}") ;;
  all)   SET=("${QUICK[@]}" "${SLOW[@]}") ;;
  *)     SET=()
         for want in ${EXAMPLES}; do
           for e in "${QUICK[@]}" "${SLOW[@]}"; do
             [[ "${e%%|*}" == "$want" ]] && SET+=("$e")
           done
         done
         if [[ ${#SET[@]} -eq 0 ]]; then
           echo "no known examples in EXAMPLES='$EXAMPLES'" >&2; exit 2
         fi ;;
esac

fail=0
for entry in "${SET[@]}"; do
  IFS='|' read -r attr want run <<<"$entry"
  printf '\033[1;36m===== %s =====\033[0m\n' "$attr"

  log="/tmp/nixgg-smoke-$attr.log"
  out=$("$PATCHED_NIX/bin/nix" build --no-eval-cache --no-link \
        --print-out-paths "$nixgg_root#$attr" 2>"$log" | tail -1)
  if [[ -z "$out" ]]; then
    echo "  BUILD FAILED; see $log:" >&2
    tail -8 "$log" >&2
    fail=1; continue
  fi

  # The artifact must be at the documented FHS path.
  disk="$ALT_STORE$out/$want"
  if [[ ! -e "$disk" ]]; then
    printf '\033[1;31m  MISSING\033[0m %s\n' "\$out/$want" >&2
    echo "  what is actually there:" >&2
    ( cd "$ALT_STORE$out" && find . -maxdepth 2 | sed 's|^|      |' ) >&2
    fail=1; continue
  fi
  if [[ ! -s "$disk" ]]; then
    echo "  EMPTY: \$out/$want" >&2; fail=1; continue
  fi

  if [[ "$run" == "-" ]]; then
    printf '\033[1;32m  OK\033[0m       $out/%s (%s bytes)\n' \
      "$want" "$(stat -c%s "$disk")"
    continue
  fi

  # shellcheck disable=SC2059
  cmd=$(printf "$run" "$disk")
  if out_txt=$(eval "$cmd" 2>&1 | head -1); then
    printf '\033[1;32m  OK\033[0m       $out/%s -> %s\n' "$want" "$out_txt"
  else
    printf '\033[1;31m  RAN BUT FAILED\033[0m $out/%s -> %s\n' "$want" "$out_txt" >&2
    fail=1
  fi
done

echo
if [[ $fail -eq 0 ]]; then
  printf '\033[1;32mall examples build and run.\033[0m\n'
else
  printf '\033[1;31msome examples failed.\033[0m\n'
fi
exit $fail
