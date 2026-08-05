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
