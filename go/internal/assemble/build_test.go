package assemble

import (
	"strings"
	"testing"

	"github.com/tbereknyei/nixgg/internal/expr"
)

func TestBuildReferencesEachStubOnce(t *testing.T) {
	drv := Build(BuildParams{
		Name:      "gg-tree-hello",
		System:    "x86_64-linux",
		Bash:      "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bash",
		Coreutils: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-coreutils",
		TreeSrc:   "cccccccccccccccccccccccccccccccc-gg-tree-hello-tree",
		Stubs: []Stub{
			{RelPath: "src/hello.o", DrvPath: "/nix/store/dddddddddddddddddddddddddddddddd-tu-hello.o.drv"},
			{RelPath: "bin/hello", DrvPath: "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-bin-hello.drv"},
			{RelPath: "lib/libfoo.a", DrvPath: "/nix/store/ffffffffffffffffffffffffffffffff-ar-libfoo.a.drv"},
		},
	})

	if len(drv.Inputs.Drvs) != 3 {
		t.Fatalf("expected 3 drv inputs, got %d: %+v", len(drv.Inputs.Drvs), drv.Inputs.Drvs)
	}
	for _, key := range []string{
		"dddddddddddddddddddddddddddddddd-tu-hello.o.drv",
		"eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-bin-hello.drv",
		"ffffffffffffffffffffffffffffffff-ar-libfoo.a.drv",
	} {
		if _, ok := drv.Inputs.Drvs[key]; !ok {
			t.Errorf("missing drv input key %q; got keys %v", key, keysOf(drv.Inputs.Drvs))
		}
	}

	script := drv.Args[1]
	// .o artifacts stay flat inside their producing drv's $out
	// (expr.ArtifactSubdir's documented rule) — the cp source must NOT
	// insert a bin/ or lib/ segment for the compile-drv case.
	if !strings.Contains(script, `"$out/src/hello.o"`) {
		t.Errorf("script missing copy destination for src/hello.o:\n%s", script)
	}
	if !strings.Contains(script, `"$out/bin/hello"`) {
		t.Errorf("script missing copy destination for bin/hello:\n%s", script)
	}
	if !strings.Contains(script, `"$out/lib/libfoo.a"`) {
		t.Errorf("script missing copy destination for lib/libfoo.a:\n%s", script)
	}
	// The archive stub's producing drv places its OWN artifact under
	// lib/ (expr.ArtifactSubdir(".a") == "lib") regardless of the
	// stub's own RelPath — confirms the source side reads the
	// PRODUCING drv's layout, not the caller's.
	if !strings.Contains(script, "/lib/libfoo.a") {
		t.Errorf("script's archive source should read from the producing drv's lib/ subdir:\n%s", script)
	}
}

func TestBuildRestoresTreeBeforeOverlayingStubs(t *testing.T) {
	drv := Build(BuildParams{
		Name:      "gg-tree-hello",
		System:    "x86_64-linux",
		Bash:      "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bash",
		Coreutils: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-coreutils",
		TreeSrc:   "cccccccccccccccccccccccccccccccc-gg-tree-hello-tree",
	})
	script := drv.Args[1]
	restoreIdx := strings.Index(script, "cp -a \"/nix/store/cccccccccccccccccccccccccccccccc-gg-tree-hello-tree/.\"")
	if restoreIdx < 0 {
		t.Fatalf("script doesn't restore the captured tree:\n%s", script)
	}
}

func TestBuildDedupesRepeatedStubDrv(t *testing.T) {
	// Two stubs from the SAME drv (e.g. a link drv whose "out" produced
	// both a binary and a matching debug symlink some build systems
	// leave behind) must not appear twice in inputs.drvs.
	drv := Build(BuildParams{
		Name:      "gg-tree-hello",
		System:    "x86_64-linux",
		Bash:      "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bash",
		Coreutils: "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-coreutils",
		TreeSrc:   "cccccccccccccccccccccccccccccccc-gg-tree-hello-tree",
		Stubs: []Stub{
			{RelPath: "bin/hello", DrvPath: "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-bin-hello.drv"},
			{RelPath: "bin/hello2", DrvPath: "/nix/store/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee-bin-hello.drv"},
		},
	})
	if len(drv.Inputs.Drvs) != 1 {
		t.Fatalf("expected exactly 1 deduped drv input, got %d: %+v", len(drv.Inputs.Drvs), drv.Inputs.Drvs)
	}
}

func keysOf(m map[string]expr.JSONDrvRef) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
