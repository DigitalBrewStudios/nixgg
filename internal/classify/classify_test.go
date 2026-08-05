package classify

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tbereknyei/nixgg/internal/drvref"
	"github.com/tbereknyei/nixgg/internal/paths"
)

// TestTargetDrvRefStub pins that a sandbox-mode drvref stub classifies
// as Kind.Drv with the recorded drv path.
//
// This is what lets a downstream link/archive shim reference the
// producing drv under inputs.drvs. If it regressed to Regular, the shim
// would fall back to Passthrough and silently stop accelerating — no
// error, just a build that quietly stops using nixgg.
func TestTargetDrvRefStub(t *testing.T) {
	dir := t.TempDir()
	want := "/nix/store/00000000000000000000000000000000-ar-libfoo.a.drv"
	stub := filepath.Join(dir, "libfoo.a")
	if err := os.WriteFile(stub, []byte(drvref.Body(want)), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Target(stub, "", paths.Layout{})
	if got.Kind != Drv {
		t.Errorf("Kind = %v, want Drv", got.Kind)
	}
	if got.Ref != want {
		t.Errorf("Ref = %q, want %q", got.Ref, want)
	}
}

// TestTargetPlainFileIsRegular pins the complement: a file we did not
// produce must be Regular, so the shim passes through rather than
// referencing a drv that doesn't exist.
func TestTargetPlainFileIsRegular(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "libvendored.a")
	if err := os.WriteFile(f, []byte("!<arch>\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Target(f, "", paths.Layout{}); got.Kind != Regular {
		t.Errorf("Kind = %v, want Regular (a vendored archive is not ours)", got.Kind)
	}
}

// TestTargetAbsent pins that a missing path is Absent, not Regular —
// the shims treat both as "pass through", but the distinction shows up
// in the log line and is worth keeping honest.
func TestTargetAbsent(t *testing.T) {
	got := Target(filepath.Join(t.TempDir(), "nope.o"), "", paths.Layout{})
	if got.Kind != Absent {
		t.Errorf("Kind = %v, want Absent", got.Kind)
	}
}

// TestTargetDanglingDrvSymlink documents a case the review proposed
// testing as a regression, which on inspection was NOT the historical
// bug.
//
// A dangling symlink to a .drv still classifies as Drv, because
// readlinkFollow falls back to os.Readlink when EvalSymlinks fails. So
// classify was never the broken layer. The real historical failure was
// a Makefile's shell-level `test -e` on such a symlink (mosh's
// `mosh-client: ../crypto/libmoshcrypto.a`), which is why sandbox mode
// switched to writing regular-file stubs — see internal/drvref.
//
// Kept as documentation of the actual boundary, so nobody "fixes"
// classify for a bug that never lived here.
func TestTargetDanglingDrvSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "libfoo.a")
	target := "/nix/store/00000000000000000000000000000000-ar-libfoo.a.drv"
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot symlink here: %v", err)
	}
	if _, err := os.Stat(link); err == nil {
		t.Skip("target unexpectedly exists; cannot test the dangling case")
	}
	got := Target(link, "", paths.Layout{})
	if got.Kind != Drv {
		t.Errorf("Kind = %v, want Drv — classify resolves a dangling .drv "+
			"symlink via the Readlink fallback; the historical bug was at the "+
			"shell `test -e` layer, not here", got.Kind)
	}
}
