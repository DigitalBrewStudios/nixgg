# Shell-quote a list of flags for a bash `-c` script.
#
# MUST stay byte-identical to shellQuoteFlags in
# internal/expr/expr.go. Both sides consume the same Derivation.Flags
# slice — the Go one when a shim emits a JSON drv (sandbox mode), this
# one when builder.nix / linker.nix / archiver.nix render the same
# derivation (native mode). If the two disagree, the two modes produce
# different drv hashes for the same input and the whole
# tests/drv-equivalence.sh invariant is void.
#
# The escaping is the standard bash single-quote trick: a single quote
# cannot appear inside a single-quoted string, so close the quote, emit
# an escaped literal quote, and reopen:
#
#   it's   ->   'it'\''s'
#
# Before this helper existed, the three .nix builders did a bare
# `"'${f}'"`, which for a flag containing an apostrophe produced
# `'it's'` — an unbalanced quote that fails `bash -n` outright. Sandbox
# mode escaped correctly, so such a flag didn't merely change the hash,
# it broke native mode while sandbox mode kept working. No fixture in
# the pinned set happens to contain an apostrophe, so the integration
# test never caught it. `-D` defines carrying English text (version
# strings, banners, "can't"/"it's" in a message) reach this easily.
flags:
let
  quoteOne = f:
    "'" + builtins.replaceStrings [ "'" ] [ "'\\''" ] f + "'";
in
  builtins.concatStringsSep " " (map quoteOne flags)
