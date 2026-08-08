package realise

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tbereknyei/nixgg/internal/paths"
	"github.com/tbereknyei/nixgg/internal/toolchain"
)

// TestPromoteToStoreFindsArtifactInItsFHSSubdir is the regression guard
// for a bug that reached a user's terminal.
//
// Native mode realises a thunk and then copies the artifact out of the
// store into the working tree, so `make` sees a real file with a fresh
// mtime. When link and archive outputs moved under $out/bin and $out/lib,
// this copy kept looking at $out/<basename> and every native build died:
//
//	nixgg: expected /tmp/nixgg-store/nix/store/…-bin-hello/hello to exist
//
// tests/drv-equivalence.sh could not catch it: it compares drv HASHES and
// never reads a realised output, so both modes agreed perfectly on drvs
// that native mode then failed to collect. Anything that reads an output's
// bytes needs coverage here, not there.
func TestPromoteToStoreFindsArtifactInItsFHSSubdir(t *testing.T) {
	for _, tc := range []struct {
		name    string // caller-visible target basename
		wantSub string // where the emitted script puts it
	}{
		{"hello", "bin"},       // link output
		{"mosh-server", "bin"}, // link output
		{"libfoo.a", "lib"},    // archive output
		{"main.o", ""},         // compile output stays flat
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			// Fake alt store holding one realised output in its FHS place.
			storePath := "/nix/store/" + "0000000000000000000000000000000a" + "-out"
			dir := filepath.Join(root, storePath)
			if tc.wantSub != "" {
				dir = filepath.Join(dir, tc.wantSub)
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, tc.name), []byte("ARTIFACT"), 0o644); err != nil {
				t.Fatal(err)
			}

			work := t.TempDir()
			target := filepath.Join(work, tc.name)
			cfg := &toolchain.Config{Store: "local?root=" + root}

			// A real Promoted dir: PromoteToStore records the store origin
			// at the end, and a zero Layout makes that step fail for an
			// unrelated reason, masking what this test is about.
			l := paths.Layout{Promoted: filepath.Join(work, ".nixgg", "promoted")}
			if err := os.MkdirAll(l.Promoted, 0o755); err != nil {
				t.Fatal(err)
			}
			err := PromoteToStore(l, cfg, "thunkid", storePath, target)
			if err != nil {
				t.Fatalf("PromoteToStore failed for a %q artifact that IS in %q: %v\n"+
					"this is the shape of the bug that broke every native build",
					tc.name, tc.wantSub, err)
			}
			got, err := os.ReadFile(target)
			if err != nil {
				t.Fatalf("target not created: %v", err)
			}
			if string(got) != "ARTIFACT" {
				t.Errorf("copied the wrong bytes: %q", got)
			}
		})
	}
}

// TestPromoteToStoreReportsAMissingArtifactClearly pins that a genuinely
// absent artifact still produces the naming-the-path error rather than
// silently creating an empty file.
func TestPromoteToStoreReportsAMissingArtifactClearly(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	cfg := &toolchain.Config{Store: "local?root=" + root}
	l := paths.Layout{Promoted: filepath.Join(work, ".nixgg", "promoted")}
	if err := os.MkdirAll(l.Promoted, 0o755); err != nil {
		t.Fatal(err)
	}
	err := PromoteToStore(l, cfg,
		"thunkid", "/nix/store/0000000000000000000000000000000a-out",
		filepath.Join(work, "hello"))
	if err == nil {
		t.Fatal("expected an error for an artifact that does not exist")
	}
	if _, statErr := os.Stat(filepath.Join(work, "hello")); statErr == nil {
		t.Error("created the target anyway; a failed promote must leave nothing behind")
	}
}
