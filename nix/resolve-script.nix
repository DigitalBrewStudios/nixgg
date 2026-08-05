# Resolve a Go-authored script template into the final bash body.
#
# The Go driver (internal/expr Derivation.scriptTemplate) emits the whole
# shell command — PATH, flag quoting, the `-l`-after-inputs split, argv
# order — leaving markers only where the value cannot be known until Nix
# evaluates the thunk:
#
#   @<tag>_COREUTILS@   this thunk's coreutilsRoot argument
#   @<tag>_COMPILER@    this thunk's compilerRoot argument (or the `ar`
#                       provider, for archiver.nix)
#   @<tag>_INPUT<i>@    inputs[i], which in native mode may be an
#                       unrealised sibling derivation whose CA output
#                       placeholder does not exist until Nix instantiates
#                       it
#
# `tag` is not fixed. Go picks the first of NIXGG, NIXGG1, NIXGG2, … that
# does not already occur in the script body and passes it here, so a flag
# whose own text spells a marker (`-DAT=@NIXGG_COMPILER@` — contrived, but
# it would otherwise be substituted silently) cannot collide. See
# markerTag in internal/expr/derivation.go.
#
# Why substitution rather than letting each helper build its own command:
# sandbox mode bakes the Go-rendered body directly into a JSON drv, so
# any layout logic duplicated here is logic that can silently disagree
# with it — and a disagreement means the two modes produce different drv
# hashes for the same compile. Two such divergences shipped before this
# split (`'` quoting, `-l` ordering). Now there is one implementation.
#
# String context survives builtins.replaceStrings, which is what makes
# this safe: substituting a derivation into the script still records the
# dependency edge in inputDrvs, exactly as `"${drv}/name"` would.
{
  scriptTemplate,
  markerTag ? "NIXGG",
  coreutils,
  compiler,
  # List of { drv, name }; `drv` is either a derivation (unrealised
  # sibling) or a pureStorePath result (already in the store).
  inputs ? [ ],
}:
let
  # These spellings must match coreutilsMarker / compilerMarker /
  # inputMarker in internal/expr/derivation.go.
  # TestNativeTemplateResolvesToSandboxScript runs this file against Go's
  # own output and fails if the two sides disagree.
  inputMarkers = builtins.genList
    (i: "@${markerTag}_INPUT${toString i}@")
    (builtins.length inputs);
  inputValues = map (i: "${i.drv}/${i.name}") inputs;
in
builtins.replaceStrings
  ([ "@${markerTag}_COREUTILS@" "@${markerTag}_COMPILER@" ] ++ inputMarkers)
  ([ "${coreutils}" "${compiler}" ] ++ inputValues)
  scriptTemplate
