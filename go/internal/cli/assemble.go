// cmdAssemble implements `nixgg assemble <root> <name>`.
//
// This is dynDrvStdenv's phase-1 postBuild step: the buildPhase left
// behind a tree at <root> full of real files interleaved with drvref
// stub files (one per cc/c++/ar/link call the nixgg shims intercepted
// — see internal/drvref). Walk that tree, build ONE assembly drv whose
// builder restores the tree verbatim then overlays each stub with its
// real, resolved artifact, and submit it as the outer builder-rpc-v0
// derivation's "out" output.
//
// Replaces dynDrvStdenv.nix's old inline-bash submitBuildTreeScript,
// which only ever copied the tree through UNRESOLVED — it never walked
// for stubs, because mkNixggBuild's single-target model gave nixgg
// nothing to generalize from until this command existed.
package cli

import (
	"fmt"
	"os"

	"github.com/tbereknyei/nixgg/internal/assemble"
	"github.com/tbereknyei/nixgg/internal/expr"
	"github.com/tbereknyei/nixgg/internal/sandbox"
	"github.com/tbereknyei/nixgg/internal/toolchain"
)

func cmdAssemble(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: nixgg assemble <root> <name>")
	}
	root, name := args[0], args[1]

	cfg, err := toolchain.FromEnv()
	if err != nil {
		return err
	}

	stubs, err := assemble.Walk(root)
	if err != nil {
		return fmt.Errorf("walk %s: %w", root, err)
	}
	fmt.Fprintf(os.Stderr, "[nixgg assemble] %d stub(s) found under %s\n", len(stubs), root)

	// Stage a filtered copy (drops builder-rpc-v0's own .nix-socket,
	// and itself — see StageForScan's docstring for why it must live
	// INSIDE root) before nix store add --scan. Left behind afterward
	// rather than removed: it's excluded from any FUTURE Walk/
	// StageForScan call by name, and removing a directory tree that
	// nix store add just finished hashing/scanning is pure risk for
	// zero benefit inside a build sandbox that gets torn down anyway.
	staged, err := assemble.StageForScan(root)
	if err != nil {
		return fmt.Errorf("stage %s for scan: %w", root, err)
	}

	treeStore, err := sandbox.StoreAddScan(cfg, name+"-tree", staged)
	if err != nil {
		return fmt.Errorf("stage tree to store: %w", err)
	}
	treeBase := expr.StoreBasename(treeStore)

	drv := assemble.Build(assemble.BuildParams{
		Name:      name,
		System:    cfg.System,
		Bash:      cfg.BashRoot,
		Coreutils: cfg.CoreutilsRoot,
		TreeSrc:   treeBase,
		Stubs:     stubs,
	})

	drvPath, err := sandbox.DerivationAdd(cfg, drv)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[nixgg assemble] drv: %s\n", drvPath)

	if err := sandbox.SubmitOutput(cfg, drvPath, "out"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[nixgg assemble] submitted: %s\n", drvPath)
	return nil
}
