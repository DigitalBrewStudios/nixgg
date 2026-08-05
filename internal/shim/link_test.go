package shim

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestParseLinkArgs pins the link-line parser: which tokens are inputs
// (objects/archives), which stay as flags, and which are dropped.
func TestParseLinkArgs(t *testing.T) {
	for _, tc := range []struct {
		name       string
		args       []string
		wantOut    string
		wantInputs []string
		wantFlags  []string
		wantOK     bool
	}{
		{
			name:       "separated -o",
			args:       []string{"a.o", "b.o", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o", "b.o"},
			wantOK:     true,
		},
		{
			name:       "attached -o",
			args:       []string{"a.o", "-oprog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o"},
			wantOK:     true,
		},
		{
			name:       "archives are inputs too",
			args:       []string{"main.o", "libfoo.a", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"main.o", "libfoo.a"},
			wantOK:     true,
		},
		{
			name:       "-l flags stay flags when unresolvable",
			args:       []string{"a.o", "-lm", "-ldl", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o"},
			wantFlags:  []string{"-lm", "-ldl"},
			wantOK:     true,
		},
		{
			// -M* families are dep-file generation; meaningless in a link
			// thunk and they target paths outside the sandbox.
			name:       "dep-file flags dropped",
			args:       []string{"a.o", "-MD", "-MF", "dep.d", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o"},
			wantOK:     true,
		},
		{
			// CMake 4 emits this so ninja can track link deps; it makes ld
			// WRITE to a build-tree-relative path that doesn't exist in the
			// link drv's sandbox ("cannot open dependency file .../link.d").
			name:       "-Wl,--dependency-file= dropped (attached)",
			args:       []string{"a.o", "-Wl,--dependency-file=x/link.d", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o"},
			wantOK:     true,
		},
		{
			name:       "-Wl,--dependency-file dropped (separated)",
			args:       []string{"a.o", "-Wl,--dependency-file", "x/link.d", "-o", "prog"},
			wantOut:    "prog",
			wantInputs: []string{"a.o"},
			wantOK:     true,
		},
		{
			name:   "no inputs is not a link we model",
			args:   []string{"-o", "prog"},
			wantOK: false,
		},
		{
			name:   "no -o is not a link we model",
			args:   []string{"a.o"},
			wantOK: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, inputs, flags, ok := parseLinkArgs(tc.args)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			if out != tc.wantOut {
				t.Errorf("output = %q, want %q", out, tc.wantOut)
			}
			if !reflect.DeepEqual(inputs, tc.wantInputs) {
				t.Errorf("inputs = %q, want %q", inputs, tc.wantInputs)
			}
			if tc.wantFlags != nil && !reflect.DeepEqual(flags, tc.wantFlags) {
				t.Errorf("flags = %q, want %q", flags, tc.wantFlags)
			}
			for _, f := range flags {
				if strings.Contains(f, "dependency-file") {
					t.Errorf("dependency-file survived into flags: %q", flags)
				}
				if strings.HasPrefix(f, "-M") {
					t.Errorf("dep-file flag survived into flags: %q", flags)
				}
			}
		})
	}
}

// TestResolveLibFlagOnlyClaimsOurArtifacts pins that `-l<name>` is
// promoted to an explicit input ONLY when the matching lib<name>.a in a
// -L dir is something nixgg produced. A vendored or system .a that we
// did not build must stay a `-l` flag so the linker resolves it normally.
//
// resolveLibFlag recognizes two markers: a symlink (native mode's thunk
// pointer) and a regular file starting with the drvref magic header
// (sandbox mode, since builder-rpc-v0 doesn't materialise .drv files
// into the sandbox so a symlink would dangle).
func TestResolveLibFlagOnlyClaimsOurArtifacts(t *testing.T) {
	dir := t.TempDir()

	// A drvref stub: what the archive shim writes in sandbox mode.
	stub := filepath.Join(dir, "libours.a")
	body := "#!nixgg-drvref\n/nix/store/" + strings.Repeat("a", 32) + "-ar-libours.a.drv\n"
	if err := os.WriteFile(stub, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// A plain archive we did not produce.
	foreign := filepath.Join(dir, "libforeign.a")
	if err := os.WriteFile(foreign, []byte("!<arch>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := resolveLibFlag("ours", []string{dir}); got != stub {
		t.Errorf("drvref stub not claimed: got %q, want %q", got, stub)
	}
	if got := resolveLibFlag("foreign", []string{dir}); got != "" {
		t.Errorf("foreign archive wrongly claimed as ours: %q — it would be "+
			"referenced as a drv input that does not exist", got)
	}
	if got := resolveLibFlag("absent", []string{dir}); got != "" {
		t.Errorf("nonexistent lib claimed: %q", got)
	}
}

// TestIsLinkInput pins which link-line tokens are files the linker
// consumes. Getting this wrong is silently wrong, not loudly wrong: an
// unrecognized token is filed under `flags` by parseLinkArgs and baked
// into the drv as a bare relative path that does not exist in the
// sandbox. Recognizing a token is what routes it through
// classify.Target, whose Regular/Absent verdicts trigger Passthrough.
func TestIsLinkInput(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want bool
	}{
		{"main.o", true},
		{"libfoo.a", true},
		{"mod.xo", true},   // redis's PIC objects for test modules
		{"thing.lo", true}, // libtool
		{"MAIN.O", true},   // ext match is case-insensitive
		{"sub/dir/main.o", true},

		// Shared libraries, plain and versioned. filepath.Ext reports
		// ".2" for the last one, which is why this needs its own check.
		{"libfoo.so", true},
		{"libfoo.so.1", true},
		{"libfoo.so.1.2", true},
		{"libfoo.so.1.2.3", true},
		{"/abs/path/libfoo.so.1", true},

		// Flags are never inputs. Without the leading-dash guard,
		// `-l:libexact.a` has filepath.Ext ".a" and is mistaken for an
		// archive — classify.Target then stats a file literally named
		// "-l:libexact.a", gets Absent, and passes through. Safe, but
		// only by accident; resolveLibFlag is the correct handler.
		{"-l:libexact.a", false},
		{"-lfoo", false},
		{"-o", false},
		{"-Wl,--as-needed", false},
		{"-L/usr/lib", false},

		// Not libraries despite superficial resemblance.
		{"libfoo.solid", false},
		{"libfoo.so.1.x", false}, // non-numeric version segment
		{"libfoo.so.", false},    // empty trailing segment
		{"notes.txt", false},
		{"main.c", false},
		{"", false},
	} {
		t.Run(tc.in, func(t *testing.T) {
			if got := isLinkInput(tc.in); got != tc.want {
				t.Errorf("isLinkInput(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseLinkArgsSharedLibIsAnInput pins that a positional shared
// library reaches `inputs`, not `flags`. As a flag it would be baked
// into the drv as a bare path with no corresponding staged file.
func TestParseLinkArgsSharedLibIsAnInput(t *testing.T) {
	_, inputs, flags, ok := parseLinkArgs(
		[]string{"main.o", "libfoo.so", "libbar.so.1.2", "-o", "prog"})
	if !ok {
		t.Fatal("parseLinkArgs returned !ok")
	}
	want := []string{"main.o", "libfoo.so", "libbar.so.1.2"}
	if !reflect.DeepEqual(inputs, want) {
		t.Errorf("inputs = %q, want %q", inputs, want)
	}
	for _, f := range flags {
		if strings.Contains(f, ".so") {
			t.Errorf("shared lib landed in flags (%q) — it would be baked into "+
				"the drv as a path that does not exist in the sandbox", flags)
		}
	}
}

// TestResolveLibFlagExactNameForm pins `-l:libfoo.a`, the spelling build
// systems use to pin a static archive when a shared one also exists (ld
// takes the name literally instead of expanding lib…/.a).
func TestResolveLibFlagExactNameForm(t *testing.T) {
	dir := t.TempDir()
	stub := filepath.Join(dir, "libexact.a")
	body := "#!nixgg-drvref\n/nix/store/" + strings.Repeat("a", 32) + "-ar-libexact.a.drv\n"
	if err := os.WriteFile(stub, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	// A foreign archive under the exact-name form must NOT be claimed.
	foreign := filepath.Join(dir, "libforeign.a")
	if err := os.WriteFile(foreign, []byte("!<arch>\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name string
		arg  string
		want string
	}{
		{"exact name resolves to our stub", ":libexact.a", stub},
		{"plain name still works", "exact", stub},
		{"foreign archive not claimed", ":libforeign.a", ""},
		{"absent file", ":libnope.a", ""},
		{"bare -l: is not a filename", ":", ""},
		{"path separator is not a plain filename", ":sub/libexact.a", ""},
		{"empty name", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveLibFlag(tc.arg, []string{dir}); got != tc.want {
				t.Errorf("resolveLibFlag(%q) = %q, want %q", tc.arg, got, tc.want)
			}
		})
	}
}
