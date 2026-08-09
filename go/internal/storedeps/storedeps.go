// Package storedeps finds which of the build's known store-path inputs
// are referenced by compile flags or wrapper env values. Those roots
// must appear in the derivation's inputs so Nix will actually make
// them available inside the sandbox — a bare -I/nix/store/... flag
// isn't enough by itself.
package storedeps

import (
	"sort"
	"strings"
)

// From returns the sorted subset of knownPaths that appear as a
// substring of any flag string or of wrapperEnvJSON.
//
// wrapperEnvJSON is a compact JSON object (from wrapperenv.JSON). We
// don't parse it as JSON; a substring search is fine because the
// wrapper env values are opaque strings — we just need every known
// store path they mention to be an input to the drv.
//
// This matches mkNixggBuild.nix's exported/known-paths list verbatim
// rather than pattern-matching arbitrary "/nix/store/..." shaped text.
// A previous regex-based version tried to reconstruct Nix's store-path
// grammar (32-char nix32 hash + name) directly in Go, and repeatedly
// diverged from the real grammar at the edges — matching hash-lookalike
// text that isn't valid nix32 (e.g. a deliberately-blanked "eeee..."
// placeholder meant to dodge reference scanning), and over-greedily
// swallowing trailing punctuation Nix's name grammar wouldn't accept
// (e.g. the "=2" in "-DVERSION=/nix/store/<hash>-foo-1.0=2"). Both
// produced strings `nix derivation add` then rejected outright. Nix's
// own reference scanner (RefScanSink) avoids this class of bug by
// never guessing at path shape — it substring-matches against a
// pre-known set of hashes. Matching against the known-paths manifest
// does the same.
func From(flags []string, wrapperEnvJSON string, knownPaths []string) []string {
	set := map[string]bool{}
	for _, p := range knownPaths {
		if p == "" {
			continue
		}
		for _, f := range flags {
			if strings.Contains(f, p) {
				set[p] = true
				break
			}
		}
		if !set[p] && strings.Contains(wrapperEnvJSON, p) {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}

// AsJSONArray formats a slice of paths as a compact JSON array of
// strings. We do this manually — store paths have no characters
// requiring JSON escaping (only [a-z0-9-_./]+), so a plain quote is
// safe and cheap.
func AsJSONArray(paths []string) string {
	if len(paths) == 0 {
		return "[]"
	}
	var b strings.Builder
	b.WriteByte('[')
	for i, p := range paths {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(p)
		b.WriteByte('"')
	}
	b.WriteByte(']')
	return b.String()
}
