// Package storedeps scans compile flags and wrapper env values for
// /nix/store/<name-hash> roots. Those roots must appear in the
// derivation's inputs so Nix will actually make them available inside
// the sandbox — a bare -I/nix/store/... flag isn't enough by itself.
package storedeps

import (
	"regexp"
	"sort"
	"strings"
)

// Matches a /nix/store root: /nix/store/<32-char nix32 hash>-<name>.
//
// Both character classes mirror what Nix itself accepts, not a loose
// negation:
//   - hash: Nix's base32 alphabet omits e/o/t/u (see nix32Chars in
//     internal/expr and src/libutil/base-nix-32.hh upstream). [a-z0-9]
//     is wider than that, so it also matches deliberately-invalid
//     lookalikes — e.g. an all-'e' blanked placeholder meant to dodge
//     reference scanning — and forwards them to `nix derivation add`,
//     which then rejects them with "illegal base-32 character".
//   - name: copied from nameRegexStr in
//     src/libstore/include/nix/store/path-regex.hh upstream (minus its
//     leading "not '.'/'..'" lookahead, irrelevant when scanning
//     mid-string). Excluding just space/slash/colon/quote, as before,
//     is wider than that: punctuation Nix would reject (commas,
//     parens, semicolons, ...) that happens to follow a real store
//     path on the same argv token — e.g. `-Wl,-rpath,<path>,` — would
//     get swept into the "name" and produce a string that's just as
//     invalid, only on the name side instead of the hash side.
var storePathRE = regexp.MustCompile(`/nix/store/[0-9a-df-np-sv-z]{32}-[0-9a-zA-Z+._?=-]+`)

// From returns a sorted, deduplicated list of store roots referenced by
// any of the flag strings or by any value in the wrapper env JSON.
//
// wrapperEnvJSON is a compact JSON object (from wrapperenv.JSON). We
// don't parse it as JSON; we just regex-match store paths in the raw
// text. That's fine because the wrapper env values are opaque strings —
// we just need every store path they name to be an input to the drv.
func From(flags []string, wrapperEnvJSON string) []string {
	set := map[string]bool{}
	for _, f := range flags {
		for _, m := range storePathRE.FindAllString(f, -1) {
			set[m] = true
		}
	}
	for _, m := range storePathRE.FindAllString(wrapperEnvJSON, -1) {
		set[m] = true
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
