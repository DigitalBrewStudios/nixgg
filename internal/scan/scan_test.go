package scan

import (
	"reflect"
	"testing"
)

// TestExtractIncludeDirsExcludesForceInclude pins that `-include` is NOT
// treated as an include DIRECTORY.
//
// Regression origin: `-include` shared the pathFlags map with
// -I/-isystem/-iquote/-idirafter, so its value — a FILE — was collected
// as if it were a directory. Two consequences, both silent:
//
//  1. scan emitted a bogus `-I<path-to-a-file>`, which gcc reports only
//     as "warning: config.h: not a directory".
//  2. rewriteFlags, sharing the same map, DROPPED the `-include` from
//     the flags reaching the compiler.
//
// The header still got staged (scan's own -MM -MG lists it as a
// dependency), so the build SUCCEEDED — compiling with different
// preprocessor state than the caller asked for, exit code 0, no error.
// `-include config.h` is the standard autoconf/CMake way to inject
// HAVE_XXX defines, so this hit real projects.
func TestExtractIncludeDirsExcludesForceInclude(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  []string
	}{
		{
			name:  "force-include is not a dir",
			flags: []string{"-include", "config.h"},
			want:  nil,
		},
		{
			name:  "dirs collected, force-include skipped",
			flags: []string{"-I", "inc", "-include", "config.h", "-Isrc"},
			want:  []string{"inc", "src"},
		},
		{
			name:  "attached and separated -I both work",
			flags: []string{"-Ia", "-I", "b", "-isystem", "c", "-iquote", "d", "-idirafter", "e"},
			want:  []string{"a", "b", "c", "d", "e"},
		},
		{
			name:  "value that looks like a flag is still consumed",
			flags: []string{"-include", "-weird.h", "-Ilast"},
			want:  []string{"last"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractIncludeDirs(tc.flags); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractIncludeDirs(%q)\n got: %q\nwant: %q", tc.flags, got, tc.want)
			}
		})
	}
}

// TestExtractForceIncludes pins the companion extractor: `-include`
// values must be recovered so the flag can be re-emitted pointing at the
// staged copy of the header.
func TestExtractForceIncludes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		flags []string
		want  []string
	}{
		{"none", []string{"-O2", "-Iinc"}, nil},
		{"one", []string{"-include", "config.h"}, []string{"config.h"}},
		{
			"several, order preserved",
			[]string{"-include", "a.h", "-O2", "-include", "b.h"},
			[]string{"a.h", "b.h"},
		},
		{
			"not confused by -I dirs",
			[]string{"-I", "inc", "-include", "config.h", "-isystem", "sys"},
			[]string{"config.h"},
		},
		{
			// gcc has no attached `-include<file>` spelling, so a token
			// merely starting with -include is not a force-include.
			"attached spelling is not a thing",
			[]string{"-includeconfig.h"},
			nil,
		},
		{"dangling at end of argv", []string{"-O2", "-include"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractForceIncludes(tc.flags); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("extractForceIncludes(%q)\n got: %q\nwant: %q", tc.flags, got, tc.want)
			}
		})
	}
}

// TestResultCacheRoundTrip pins that every Result field survives the
// scan cache. The cache is consulted whenever all recorded deps' mtimes
// match, so a field that encodes but doesn't decode produces a bug
// visible ONLY on warm rebuilds — intermittent by construction, and the
// exact trap StagedIncludeFlags would have fallen into.
func TestResultCacheRoundTrip(t *testing.T) {
	want := &Result{
		ProjectRoot:  "/tmp/proj",
		StagedIFlags: []string{"-I.", "-Iinc"},
		StoreIFlags:  []string{"-I/nix/store/aaa-dep/include"},
		// Flat slice of alternating flag/value, as rewriteFlags appends it.
		StagedIncludeFlags: []string{"-include", "config.h", "-include", "sub/other.h"},
		Headers: []Header{
			{Abs: "/tmp/proj/inc/a.h", Rel: "inc/a.h"},
			{Abs: "/tmp/proj/config.h", Rel: "config.h"},
		},
	}
	body, err := encodeResult(want)
	if err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := decodeResult(body, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&got, want) {
		t.Errorf("round-trip lost data\n got: %#v\nwant: %#v", &got, want)
	}

	// Guard against a future field being added to Result without a
	// corresponding encode/decode line: count them and fail loudly.
	const knownFields = 5
	if n := reflect.TypeOf(Result{}).NumField(); n != knownFields {
		t.Errorf("Result has %d fields, round-trip test knows about %d — "+
			"add the new field to encodeResult/decodeResult and to `want` above, "+
			"then bump knownFields", n, knownFields)
	}
}
