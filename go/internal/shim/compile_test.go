package shim

import (
	"reflect"
	"strings"
	"testing"
)

// TestRewriteFlagsKeepsForceIncludes guards a bug that already shipped
// and was already fixed once, in 267722b.
//
// `-include <file>` names a file to textually include before the
// translation unit — it is not an include *directory*. Treating it as one
// meant the file was staged as a directory and the flag dropped, so the
// TU compiled without it: a silent miscompile, not a build failure.
//
// That commit's own message records why the integration test missed it:
// "no fixture uses -include, which is why the integration test never
// caught this." So the only thing standing between that bug and a
// reappearance is a unit test at this function, and there wasn't one —
// the fix's tests covered scan.go's extraction helpers and the cache
// round-trip, not rewriteFlags' own drop-then-append logic.
//
// Every case below asserts the full output slice rather than a
// Contains(), because the failure mode is positional: forceInc must land
// AFTER the staged -I flags, or a same-named header in an earlier
// directory wins.
//
// Note on what this can and cannot catch. Adding "-include" back to
// pathFlags is, on its own, a no-op: the pathFlags branch drops the flag
// and its argument exactly as the explicit case does, so output is
// unchanged and no test can see a difference. The original bug needed
// both halves — flag treated as a directory AND no forceInc replacement
// appended — and that combination does fail these tests. Verified by
// mutation, in both the one-sided and two-sided forms.
func TestRewriteFlagsKeepsForceIncludes(t *testing.T) {
	for _, tc := range []struct {
		name                            string
		caller, staged, store, forceInc []string
		want                            []string
	}{
		{
			name:     "caller -include is replaced by the staged one",
			caller:   []string{"-O2", "-include", "config.h", "-Wall"},
			staged:   []string{"-I", "."},
			forceInc: []string{"-include", "config.h"},
			want:     []string{"-O2", "-Wall", "-I", ".", "-include", "config.h"},
		},
		{
			name:     "forceInc lands after staged include dirs",
			caller:   []string{"-include", "pch.h"},
			staged:   []string{"-I", "inc", "-I", "gen"},
			forceInc: []string{"-include", "pch.h"},
			// If forceInc came first, a pch.h in inc/ or gen/ would not
			// be the one already resolved by the scanner.
			want: []string{"-I", "inc", "-I", "gen", "-include", "pch.h"},
		},
		{
			name:     "several -include flags all survive",
			caller:   []string{"-include", "a.h", "-O1", "-include", "b.h"},
			staged:   []string{"-I", "."},
			forceInc: []string{"-include", "a.h", "-include", "b.h"},
			want:     []string{"-O1", "-I", ".", "-include", "a.h", "-include", "b.h"},
		},
		{
			name:     "include dirs are still dropped, both spellings",
			caller:   []string{"-I", "/abs", "-I/abs2", "-isystem", "/sys", "-O2"},
			staged:   []string{"-I", "."},
			forceInc: nil,
			want:     []string{"-O2", "-I", "."},
		},
		{
			name:     "no -include at all is unchanged apart from appends",
			caller:   []string{"-O2", "-Wall"},
			staged:   []string{"-I", "."},
			store:    []string{"-I", "/nix/store/x/include"},
			forceInc: nil,
			want:     []string{"-O2", "-Wall", "-I", ".", "-I", "/nix/store/x/include"},
		},
		{
			name: "-include as the final token doesn't run off the end",
			// A malformed line: -include with no argument. Must not panic
			// and must not consume a token that isn't there.
			caller:   []string{"-O2", "-include"},
			staged:   nil,
			forceInc: nil,
			want:     []string{"-O2"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := rewriteFlags(tc.caller, tc.staged, tc.store, tc.forceInc)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("rewriteFlags mismatch\ncaller  : %q\nstaged  : %q\nforceInc: %q\ngot     : %q\nwant    : %q",
					tc.caller, tc.staged, tc.forceInc, got, tc.want)
			}
		})
	}
}

// TestRewriteFlagsDropsCallerIncludePaths pins that a caller's
// `-include` path never survives verbatim. The caller's spelling is
// relative to its own cwd, which does not exist inside the sandbox; only
// the staged replacement in forceInc is valid there.
func TestRewriteFlagsDropsCallerIncludePaths(t *testing.T) {
	got := rewriteFlags(
		[]string{"-include", "../../outside/config.h", "-O2"},
		[]string{"-I", "."},
		nil,
		[]string{"-include", "config.h"},
	)
	for _, g := range got {
		if strings.Contains(g, "outside") {
			t.Errorf("caller's -include path leaked into the sandbox flags: %q", got)
		}
	}
}
