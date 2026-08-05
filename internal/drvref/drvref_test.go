package drvref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBodyRoundTrip pins that what Body writes is what Path reads back.
// These are the two halves of a wire format shared by three packages
// (sandbox writes, classify and shim read), so a one-sided change here
// makes the writer and readers disagree.
func TestBodyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	for _, drv := range []string{
		"/nix/store/" + strings.Repeat("a", 32) + "-tu-main.o.drv",
		"/nix/store/" + strings.Repeat("b", 32) + "-ar-libfoo.a.drv",
		"/nix/store/" + strings.Repeat("c", 32) + "-bin-prog.drv",
	} {
		f := filepath.Join(dir, filepath.Base(drv)+".stub")
		if err := os.WriteFile(f, []byte(Body(drv)), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := Path(f); got != drv {
			t.Errorf("Path(Body(%q)) = %q, want the original", drv, got)
		}
		if !Is(f) {
			t.Errorf("Is() false for a stub we just wrote: %q", f)
		}
	}
}

// TestPathRejectsNonStubs pins that Path is safe to call on anything.
// classify.Target calls it on every regular file it sees, so a real
// object or archive must simply return "" — never a false positive that
// would make the link shim reference a drv input that doesn't exist.
func TestPathRejectsNonStubs(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"empty", ""},
		{"ar archive", "!<arch>\n"},
		{"elf-ish", "\x7fELF\x02\x01\x01\x00"},
		{"plain text", "hello\n"},
		{"header without newline", "#!nixgg-drvref"},
		{"similar but wrong magic", "#!nixgg-thunk\n/nix/store/x.drv\n"},
		{"magic not at the start", "junk\n#!nixgg-drvref\n/nix/store/x.drv\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "_"))
			if err := os.WriteFile(f, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := Path(f); got != "" {
				t.Errorf("Path(%q content) = %q, want \"\" — a false positive here "+
					"makes a foreign file get referenced as a drv input", tc.name, got)
			}
			if Is(f) {
				t.Errorf("Is() true for non-stub %q", tc.name)
			}
		})
	}
}

// TestPathOnMissingFile pins that a nonexistent path is "" and not a
// panic — classify.Target reaches here for paths that vanished between
// its Lstat and this read.
func TestPathOnMissingFile(t *testing.T) {
	if got := Path(filepath.Join(t.TempDir(), "nope")); got != "" {
		t.Errorf("Path(missing) = %q, want \"\"", got)
	}
}

// TestHeaderShape pins the two properties other packages rely on: the
// magic ends in a newline (so a stub is two lines, and a prefix check
// can't half-match a longer magic), and it opens with `#!` so a stub
// mistaken for an executable produces an error naming this file.
func TestHeaderShape(t *testing.T) {
	if !strings.HasSuffix(Header, "\n") {
		t.Errorf("Header %q must end in a newline", Header)
	}
	if !strings.HasPrefix(Header, "#!") {
		t.Errorf("Header %q must start with #! so a mis-exec is legible", Header)
	}
	if strings.Count(Header, "\n") != 1 {
		t.Errorf("Header %q must be exactly one line", Header)
	}
}
