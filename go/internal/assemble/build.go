package assemble

import (
	"path"
	"strings"

	"github.com/tbereknyei/nixgg/internal/expr"
)

// BuildParams describes the tree-assembly drv: restore a captured tree
// verbatim, then overwrite every discovered stub with its real,
// resolved content.
type BuildParams struct {
	Name      string // drv name, e.g. "gg-tree-hello-2.12.3"
	System    string
	Bash      string // full /nix/store/... path
	Coreutils string // full /nix/store/... path
	// TreeSrc is the store-path basename of the whole captured tree
	// (already `nix store add --scan`ed by the caller — this package
	// only builds the drv, it doesn't touch the store itself).
	TreeSrc string
	Stubs   []Stub
}

// Build assembles the JSONDrv. The builder script:
//  1. copies TreeSrc (every real file AND every stub placeholder,
//     verbatim) into $out — this is the tree AS IT WAS, unresolved;
//  2. overwrites each stub's RelPath with the real artifact from its
//     producing drv's own "out" output.
//
// Step 2 finds that artifact via the SAME convention link.go/archive.go
// already use to reference each other's outputs (expr.ArtifactSubdir,
// keyed on the stub's own basename): a stub's RelPath is the filename
// callers on disk expected (make/cmake chose it, not us), so the
// producing drv's own $out layout — flat for a .o, lib/ for a .a,
// bin/ for anything else — is recoverable from that same basename
// without needing the drv's internal Name (which nixgg's compile/
// link/archive drvs prefix with "tu-"/"bin-"/"ar-" for their OWN
// naming reasons, unrelated to this).
func Build(p BuildParams) expr.JSONDrv {
	drvs := map[string]expr.JSONDrvRef{}
	srcs := []string{expr.StoreBasename(p.Bash), expr.StoreBasename(p.Coreutils), p.TreeSrc}

	var script strings.Builder
	script.WriteString("set -euo pipefail\n")
	script.WriteString(`export PATH="` + p.Coreutils + `/bin"` + "\n")
	script.WriteString(`mkdir -p "$out"` + "\n")
	script.WriteString(`cp -a "/nix/store/` + p.TreeSrc + `/." "$out/"` + "\n")
	script.WriteString(`chmod -R u+w "$out"` + "\n")

	for _, s := range p.Stubs {
		key := expr.StoreBasename(s.DrvPath)
		ref := drvs[key]
		ref.Outputs = []string{"out"}
		ref.DynamicOutputs = map[string]any{}
		drvs[key] = ref

		base := path.Base(s.RelPath)
		subdir := expr.ArtifactSubdir(base)
		src := expr.CAOutputPlaceholder(s.DrvPath, "out")
		if subdir != "" {
			src += "/" + subdir
		}
		src += "/" + base
		script.WriteString(`cp -a ` + shellQuote(src) + ` "$out/` + s.RelPath + `"` + "\n")
	}

	return expr.JSONDrv{
		Name:    p.Name,
		System:  p.System,
		Builder: p.Bash + "/bin/bash",
		Args:    []string{"-c", script.String()},
		Env: map[string]string{
			"out":            "/" + expr.OutPlaceholderNix32,
			"name":           p.Name,
			"system":         p.System,
			"builder":        p.Bash + "/bin/bash",
			"outputHashAlgo": "sha256",
			"outputHashMode": "nar",
		},
		Inputs: expr.JSONDrvInputs{
			Drvs: drvs,
			Srcs: srcs,
		},
		Outputs: map[string]expr.JSONOut{
			"out": {Method: "nar", HashAlgo: "sha256"},
		},
		Version: 4,
	}
}

// shellQuote wraps s in single quotes, escaping any single quote it
// contains. Every value this package quotes (a downstream placeholder
// plus a store-internal subdir/basename) is nixgg- or Nix-generated,
// never arbitrary user text, but a filename inside a real project tree
// (RelPath's basename) is NOT — quoting defensively here, rather than
// assuming project filenames stay shell-safe, costs nothing.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
