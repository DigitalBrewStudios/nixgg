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

// TestParseCompileArgsExplicitLanguage pins that `-x <lang>` overrides
// extension-based source detection.
//
// isSource matches on suffix, so a precompiled-header compile —
//
//	g++ -x c++-header -c pch.h -o pch.h.gch
//
// which is what CMake's target_precompile_headers and Qt's build emit —
// had no source by nixgg's reckoning, returned ok=false, and fell to
// Passthrough. The output was correct but the TU was never cached or
// distributed, and (before the diagnostics added earlier) said nothing.
func TestParseCompileArgsExplicitLanguage(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantSource string
		wantOutput string
		wantOK     bool
	}{
		{
			name:       "c++ precompiled header",
			args:       []string{"-x", "c++-header", "-c", "pch.h", "-o", "pch.h.gch"},
			wantSource: "pch.h", wantOutput: "pch.h.gch", wantOK: true,
		},
		{
			name:       "c precompiled header",
			args:       []string{"-x", "c-header", "-c", "pch.h", "-o", "pch.h.gch"},
			wantSource: "pch.h", wantOutput: "pch.h.gch", wantOK: true,
		},
		{
			// -x also legitimises an unusual extension for a normal
			// compile, which is the same rule the driver applies.
			name:       "explicit language with an odd extension",
			args:       []string{"-x", "c++", "-c", "gen.inc", "-o", "gen.o"},
			wantSource: "gen.inc", wantOutput: "gen.o", wantOK: true,
		},
		{
			// Without -x, a .h is not a source and never was.
			name:   "header with no -x is still not a source",
			args:   []string{"-c", "pch.h", "-o", "pch.h.gch"},
			wantOK: false,
		},
		{
			// The -x value itself must not be mistaken for the source.
			name:       "the language token is not the source",
			args:       []string{"-x", "c++-header", "-c", "real.h"},
			wantSource: "real.h", wantOutput: "real.h.gch", wantOK: true,
		},
		{
			// Two sources is still unmodellable, -x or not.
			name:   "two sources under -x still bails",
			args:   []string{"-x", "c++", "-c", "a.cc", "b.cc"},
			wantOK: false,
		},
		{
			name:   "-x with no value bails rather than indexing past the end",
			args:   []string{"-c", "a.cc", "-x"},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src, out, flags, ok := parseCompileArgs(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (args %q)", ok, tc.wantOK, tc.args)
			}
			if !ok {
				return
			}
			if src != tc.wantSource {
				t.Errorf("source = %q, want %q", src, tc.wantSource)
			}
			// Call the production helper rather than restating it: an
			// earlier version of this test reimplemented the rule here,
			// which made a mutation of the real logic invisible.
			if out == "" {
				out = defaultOutputName(src, flags)
			}
			if out != tc.wantOutput {
				t.Errorf("output = %q, want %q", out, tc.wantOutput)
			}
			// -x must survive into the sandbox flags: without it the
			// compiler would guess the language from the extension and
			// produce an object instead of a PCH.
			var sawX bool
			for i := 0; i+1 < len(flags); i++ {
				if flags[i] == "-x" {
					sawX = true
				}
			}
			if !sawX {
				t.Errorf("-x dropped from flags %q — the sandbox compile would "+
					"guess the language from the extension instead", flags)
			}
		})
	}
}

// TestPCHDefaultOutputName pins gcc's naming rule for a precompiled
// header, which differs from the object rule: .gch is appended to the
// source's FULL name, so pch.h becomes pch.h.gch, not pch.gch. Verified
// against gcc by compiling a header with no -o.
func TestPCHDefaultOutputName(t *testing.T) {
	if !isHeaderLang("c++-header") {
		t.Fatal("c++-header must be recognised as a header language")
	}
	for _, lang := range []string{"c-header", "c++-header",
		"objective-c-header", "objective-c++-header"} {
		if !isHeaderLang(lang) {
			t.Errorf("isHeaderLang(%q) = false, want true", lang)
		}
	}
	for _, lang := range []string{"c", "c++", "assembler", ""} {
		if isHeaderLang(lang) {
			t.Errorf("isHeaderLang(%q) = true, want false — a normal compile "+
				"must still get the .o naming rule", lang)
		}
	}
	if got := langOf([]string{"-O2", "-x", "c++-header", "-Wall"}); got != "c++-header" {
		t.Errorf("langOf = %q, want \"c++-header\"", got)
	}
	if got := langOf([]string{"-O2"}); got != "" {
		t.Errorf("langOf with no -x = %q, want \"\"", got)
	}
}

// TestDefaultOutputName covers the -o-omitted path directly. An earlier
// version of the PCH test restated this rule inline instead of calling
// the real function, so a mutation that gave precompiled headers the .o
// naming rule passed clean. Call the production code.
func TestDefaultOutputName(t *testing.T) {
	for _, tc := range []struct {
		source string
		flags  []string
		want   string
	}{
		{"a.cc", nil, "a.o"},
		{"src/b.c", nil, "b.o"},
		{"a.b.cc", nil, "a.b.o"},
		{"noext", nil, "noext.o"},
		// The header rule: extension kept, .gch appended.
		{"pch.h", []string{"-x", "c++-header"}, "pch.h.gch"},
		{"inc/pch.hpp", []string{"-x", "c++-header"}, "pch.hpp.gch"},
		{"pch.h", []string{"-x", "c-header"}, "pch.h.gch"},
		// A non-header -x keeps the object rule.
		{"gen.inc", []string{"-x", "c++"}, "gen.o"},
	} {
		if got := defaultOutputName(tc.source, tc.flags); got != tc.want {
			t.Errorf("defaultOutputName(%q, %q) = %q, want %q",
				tc.source, tc.flags, got, tc.want)
		}
	}
}

// TestOnlyDashXLegitimisesAnOddSource pins that the "any token can be the
// source" relaxation is gated on -x specifically, not on any two-argument
// flag. -Xlinker and -Xassembler go through the same parser branch, and
// their values say nothing about the source language.
//
// Without the guard, `cc -c -Xassembler --foo bar.unknown -o out.o` would
// treat bar.unknown as a source and try to model a TU nixgg cannot
// reason about.
func TestOnlyDashXLegitimisesAnOddSource(t *testing.T) {
	// -Xassembler present, no real source: must NOT adopt the odd token.
	if _, _, _, ok := parseCompileArgs([]string{
		"-c", "-Xassembler", "--noexecstack", "mystery.dat", "-o", "out.o",
	}); ok {
		t.Error("an -Xassembler value legitimised a non-source token as the " +
			"compile source; only -x names a language")
	}
	// -Xlinker likewise.
	if _, _, _, ok := parseCompileArgs([]string{
		"-c", "-Xlinker", "-z", "mystery.dat", "-o", "out.o",
	}); ok {
		t.Error("-Xlinker legitimised a non-source token as the compile source")
	}
	// And the real thing still works.
	if src, _, _, ok := parseCompileArgs([]string{
		"-x", "c++-header", "-c", "pch.h", "-o", "pch.h.gch",
	}); !ok || src != "pch.h" {
		t.Errorf("-x path broken: src=%q ok=%v", src, ok)
	}
}
