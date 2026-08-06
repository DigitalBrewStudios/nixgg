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
		rel = filepath.Base(callerPath)
	}
	return expr.Input{
			Kind: "store", Ref: c.Ref, Name: rel,
		}, expr.JSONDrvInput{
			Kind: "src", Ref: filepath.Base(c.Ref), Name: rel,
		}
}
