// Package assemble walks a build tree left behind by a dynDrvStdenv
// phase-1 buildPhase and finds every drvref stub the nixgg shims wrote
// in place of a real artifact.
//
// This exists because dynDrvStdenv (unlike mkNixggBuild) has no single
// target: `make`/`cmake --build` produces a whole tree — dozens of
// .o/.a/binary outputs — and nothing downstream names them individually
// the way mkNixggBuild.nix's `target` param does. Assembling the final
// tree therefore needs to discover every stub by walking, not by
// argument parsing.
package assemble

import (
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tbereknyei/nixgg/internal/drvref"
)

// Stub is one discovered drvref stub: its path relative to the walked
// root, and the drv that will produce its real content.
type Stub struct {
	// RelPath is slash-separated and relative to the walked root —
	// e.g. "src/hello.o", never an absolute path. The assembly
	// builder script recreates the tree at this exact relative
	// location, so a stub found at $root/src/hello.o must resolve to
	// $out/src/hello.o, not $out/hello.o.
	RelPath string
	// DrvPath is the full /nix/store/<hash>-<name>.drv path recorded
	// in the stub — see drvref.Path.
	DrvPath string
}

// skipNames are sandbox-infrastructure entries that are never real
// build output and must never be treated as ordinary tree content.
// ".nix-socket" is the builder-rpc-v0 daemon's own unix socket
// (NIX_REMOTE points at it) — `nix store add --scan` chokes on a
// socket with "file '...' has an unsupported type" (confirmed
// directly while building the fmt example by hand, before this
// walker existed), and opening one with a plain os.Open for stub
// detection is itself the wrong thing to do to a special file.
// ".gg-stage" is StageForScan's own working directory (see its
// docstring for why it must live INSIDE root, not under an
// os.MkdirTemp("", ...) path — $TMPDIR inside this sandbox already
// resolves under root, which made an early version of this package
// copy its own staging directory into itself, recursively, until
// "file name too long").
var skipNames = map[string]bool{
	".nix-socket": true,
	".gg-stage":   true,
}

// Walk finds every drvref stub under root. Returns stubs in a
// deterministic (lexical, filepath.WalkDir's own) order so the
// resulting JSON drv — and therefore its hash — doesn't depend on
// directory-read ordering.
func Walk(root string) ([]Stub, error) {
	var stubs []Stub
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if skipNames[d.Name()] {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		// drvref.Path only recognizes regular files with the right
		// magic header; a symlink or anything else returns "" and is
		// left as ordinary tree content for the caller to copy
		// verbatim.
		info, err := d.Info()
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		ref := drvref.Path(path)
		if ref == "" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		stubs = append(stubs, Stub{RelPath: filepath.ToSlash(rel), DrvPath: ref})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return stubs, nil
}

// StageForScan copies root into a fresh directory INSIDE root itself
// (named ".gg-stage" — see skipNames), excluding skipNames entries,
// and returns the staged path.
//
// Must be inside root, not under a dest supplied via os.MkdirTemp("",
// ...): confirmed directly that $TMPDIR inside a builder-rpc-v0
// sandbox already resolves to a path under $NIX_BUILD_TOP (root
// itself), so passing an os.MkdirTemp("", ...) result as dest made
// the staging directory a descendant of root — copying root's own
// entries into a destination that is ALSO one of root's entries
// recursed into itself every level ("stage/nixgg-assemble.../stage/
// nixgg-assemble.../stage/...") until the kernel refused with "file
// name too long". Nesting the stage dir at a FIXED, excluded name
// directly under root sidesteps the whole class of bug: it can never
// be a legitimate ANCESTOR of root, so root's own entries can never
// recurse into it.
//
// `nix store add --scan` cannot ingest root directly regardless:
// builder-rpc-v0 leaves a live unix socket (.nix-socket, NIX_REMOTE
// points at it) in $NIX_BUILD_TOP, and --scan chokes on a socket with
// "file '...' has an unsupported type" (confirmed directly building
// the fmt example). Deleting it from the live tree first isn't an
// option either — the caller's own subsequent `nix store add`/
// `nix derivation add`/`nix store submit-output` calls go through
// that exact socket.
func StageForScan(root string) (string, error) {
	staged := filepath.Join(root, ".gg-stage")
	if err := os.MkdirAll(staged, 0o755); err != nil {
		return "", err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if skipNames[e.Name()] {
			continue
		}
		if err := copyRecursive(filepath.Join(root, e.Name()), filepath.Join(staged, e.Name())); err != nil {
			return "", err
		}
	}
	return staged, nil
}

// copyRecursive copies a file, directory, or symlink from src to dst,
// preserving symlinks verbatim (never following them into a copy —
// nixgg-produced SONAME alias chains like libfoo.so -> libfoo.so.1.2.3
// must stay symlinks in the assembled tree, not become independent
// duplicate files).
func copyRecursive(src, dst string) error {
	info, err := os.Lstat(src)
	if err != nil {
		return err
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	case info.IsDir():
		if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
			return err
		}
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := copyRecursive(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
		return nil
	default:
		return copyFile(src, dst, info.Mode().Perm())
	}
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
