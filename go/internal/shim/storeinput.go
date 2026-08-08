package shim

import (
	"path/filepath"

	"github.com/tbereknyei/nixgg/internal/classify"
	"github.com/tbereknyei/nixgg/internal/expr"
)

// storeInput builds the pair of per-input records a Store-classified
// link/archive input needs — one for each wire format.
//
// The subtlety is `Name`. Both serializers render an input's argv token as
// Ref+"/"+Name, so Name must be the path relative to the store ROOT, not
// the caller-visible basename. Those coincide for everything nixgg
// produces (a drv output dir holding one artifact) but not for a
// dependency it merely consumes: LLVM's cmake puts an absolute
// `…-zlib-1.3.2/lib/libz.so` on the link line, and using the basename
// there yields `…-zlib-1.3.2/libz.so` — a file that does not exist, so the
// link fails with `ld.bfd: cannot find`.
//
// Ref stays the root because that is what `builtins.storePath` (native)
// and inputs.srcs (sandbox) accept; neither takes a subpath.
//
// Shared by link.go and archive.go so the two cannot drift, and so this is
// reachable from a test — the original bug lived in code only exercised
// through a full build.
func storeInput(c classify.Result, callerPath string) (expr.Input, expr.JSONDrvInput) {
	rel := c.Sub
	if rel == "" {
		// No Sub means classification could not observe the artifact's
		// position inside its store path. Two ways that happens, and they
		// need opposite treatment:
		//
		//   - A foreign dependency reached through a symlink: Sub IS set
		//     (classify resolved the link), so we never get here.
		//   - One of OUR OWN outputs that `force` promoted to a real file:
		//     the promoted registry records only the store ROOT, so Sub is
		//     empty and the artifact's FHS subdir has to be re-derived.
		//
		// Missing the second case broke native-mode lua: liblua.a lives at
		// <root>/lib/liblua.a but was referenced as <root>/liblua.a, and
		// luac failed with `ld: cannot find …-ar-liblua.a/liblua.a`.
		base := filepath.Base(callerPath)
		if sub := expr.ArtifactSubdir(base); sub != "" {
			rel = sub + "/" + base
		} else {
			rel = base
		}
	}
	return expr.Input{
			Kind: "store", Ref: c.Ref, Name: rel,
		}, expr.JSONDrvInput{
			Kind: "src", Ref: filepath.Base(c.Ref), Name: rel,
		}
}
