package classify

import (
	"os"
	"path/filepath"
	"strings"
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

// TestStoreSubpathSurvivesClassification is the regression guard for a
// link failure that reached a real build: LLVM's cmake puts an absolute
// positional shared library on the link line, and nixgg linked against a
// path that did not exist.
//
//	ld.bfd: cannot find /nix/store/…-zlib-1.3.2/libz.so
//
// The file is at <root>/lib/libz.so. Classification reduced it to the
// store root (correct — builtins.storePath and inputs.srcs both need a
// root, not a subpath), and the link shim then rebuilt the argv as
// Ref+"/"+filepath.Base(input), silently dropping the "lib/" in between.
//
// Every input nixgg produces itself lives directly in a drv output dir,
// so root+basename was right for all of them, and the 81-drv fixture set
// contains no positional absolute .so. The bug needed an input that
// nixgg did not create.
func TestStoreSubpathSurvivesClassification(t *testing.T) {
	dir := t.TempDir()
	// Fake store layout: <root>/lib/libz.so, reached through a symlink
	// the way a build tree references a dependency.
	root := filepath.Join(dir, "nix", "store", strings.Repeat("a", 32)+"-zlib-1.3.2")
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "lib", "libz.so")
	if err := os.WriteFile(real, []byte("\x7fELF"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "libz.so")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got := Target(link, dir, paths.Layout{})
	if got.Kind != Store {
		t.Fatalf("Kind = %v, want Store", got.Kind)
	}

	wantRef := "/nix/store/" + strings.Repeat("a", 32) + "-zlib-1.3.2"
	if got.Ref != wantRef {
		t.Errorf("Ref = %q, want the store ROOT %q — builtins.storePath and\n"+
			"inputs.srcs reject a subpath", got.Ref, wantRef)
	}
	if got.Sub != "lib/libz.so" {
		t.Errorf("Sub = %q, want \"lib/libz.so\" — without it the argv path\n"+
			"cannot be reconstructed and the link references a nonexistent file", got.Sub)
	}

	// The composed argv path is the whole point: it must be the real
	// file, not root+basename.
	if want := wantRef + "/lib/libz.so"; got.ArgvPath("libz.so") != want {
		t.Errorf("ArgvPath = %q, want %q", got.ArgvPath("libz.so"), want)
	}
}

// TestStoreDirectChildHasNoSub pins the common case: an artifact nixgg
// produced sits directly in its drv output dir, so Sub stays empty and
// ArgvPath falls back to the caller-visible basename. Every one of the 81
// pinned drvs depends on this shape, so a change that started reporting
// Sub here would move hashes across the board.
func TestStoreDirectChildHasNoSub(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "nix", "store", strings.Repeat("b", 32)+"-tu-main.o")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	real := filepath.Join(root, "main.o")
	if err := os.WriteFile(real, []byte("obj"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "main.o")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	got := Target(link, dir, paths.Layout{})
	if got.Kind != Store {
		t.Fatalf("Kind = %v, want Store", got.Kind)
	}
	if got.Sub != "main.o" {
		t.Errorf("Sub = %q, want \"main.o\"", got.Sub)
	}
	if want := "/nix/store/" + strings.Repeat("b", 32) + "-tu-main.o/main.o"; got.ArgvPath("main.o") != want {
		t.Errorf("ArgvPath = %q, want %q", got.ArgvPath("main.o"), want)
	}
}
