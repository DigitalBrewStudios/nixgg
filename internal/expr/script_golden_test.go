package expr

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The drv-script golden test.
//
// nixgg emits the SAME derivation two ways: Go's Derivation.script()
// bakes the bash body straight into a JSON drv (sandbox mode), while
// nix/{builder,linker,archiver}.nix re-derive that body from the thunk's
// arguments at eval time (native mode). Nothing in the type system ties
// the two together — they are byte-identical only because someone kept
// them that way by hand. When they drift, native and sandbox produce
// different drv hashes for the same compile, which is the one invariant
// this project rests on.
//
// tests/drv-equivalence.sh already catches drift, but it costs minutes,
// needs the patched Nix plus an alt store, and only covers flag/input
// shapes that the four pinned fixtures happen to contain. That blind
// spot is not hypothetical: it is exactly how the `'` quoting divergence
// (fixed in 6aa52c7) survived — 81 pinned drvs and not one apostrophe
// among them.
//
// These tests close it at the unit level. Each one renders a Derivation
// through Go, renders the same arguments through the real .nix helper via
// `nix-instantiate --eval --raw` (pure eval, ~30ms, no daemon, no store
// writes, reading `drvAttrs.args` rather than instantiating), and demands
// the two strings match byte for byte. The cases deliberately include
// shapes no fixture exercises — apostrophes, spaces, backslashes, empty
// flag lists, `-l` splitting on both sides of its fallback branch.
//
// If you change the shell layout in script(), this fails until you make
// the matching .nix edit, and vice versa. That is the entire point: it
// converts "identical by discipline" into "identical by test".
//
// Related but narrower: TestShellQuoteFlagsMatchesNixHelper pins just
// the quoting function. This pins the whole assembled script, so it also
// covers PATH construction, input rendering, and argument order.

// nixEvalScript renders `helper` with `args` and returns args[1] of the
// resulting derivation — the bash body. helper is a basename in nix/.
func nixEvalScript(t *testing.T, helper string, argsNix string) string {
	t.Helper()
	dir, err := filepath.Abs("../../nix")
	if err != nil {
		t.Fatal(err)
	}
	expr := "builtins.elemAt (import " + filepath.Join(dir, helper) + " " + argsNix + ").drvAttrs.args 1"
	return runNixEval(t, expr)
}

// runNixEval evaluates `expr` and returns its raw string value. Nix's
// diagnostics go to stderr and are the whole story when eval fails
// (e.g. "context key ... is not a store path"), so capture and report
// them — an "exit status 1" alone sends you back here to re-run by hand.
func runNixEval(t *testing.T, expr string) string {
	t.Helper()
	cmd := exec.Command("nix-instantiate", "--eval", "--raw", "--impure", "--expr", expr)
	// A user's NIX_PATH / overlays can't affect these helpers (they take
	// every store path as an argument and never touch <nixpkgs>), but keep
	// the environment minimal so the test can't pick up an eval cache from
	// a different checkout.
	cmd.Env = append(os.Environ(), "NIX_PATH=")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("nix-instantiate failed: %v\nexpr:\n%s\nstderr:\n%s", err, expr, stderr.String())
	}
	return string(out)
}

// nixStr renders a Go string as a Nix double-quoted string literal.
func nixStr(s string) string {
	e := strings.ReplaceAll(s, `\`, `\\`)
	e = strings.ReplaceAll(e, `"`, `\"`)
	e = strings.ReplaceAll(e, "${", `\${`)
	return `"` + e + `"`
}

// nixIndentedString renders a Go string as a Nix indented-string literal
// (the two-apostrophe delimiter), which is how the driver passes
// flagsJSON / storeDepsJSON / wrapperEnvJSON. Our JSON payloads contain
// `"`, which needs no escaping there, but could contain the delimiter
// itself or `${`, both of which do.
//
// The delimiter is spelled through strings.ReplaceAll below rather than
// written in this comment on purpose: gofmt rewrites a bare pair of
// apostrophes in a comment into typographic quotes, which would make
// this file permanently unformatted (see internal/expr/expr.go).
func nixIndentedString(s string) string {
	e := strings.ReplaceAll(s, "''", "'''")
	e = strings.ReplaceAll(e, "${", "''${")
	return "''" + e + "''"
}

func flagsJSONOf(t *testing.T, flags []string) string {
	t.Helper()
	if len(flags) == 0 {
		return nixIndentedString("[]")
	}
	b, err := json.Marshal(flags)
	if err != nil {
		t.Fatal(err)
	}
	return nixIndentedString(string(b))
}

// Fixed fake store paths. Real-looking (32-char hash + name) so the
// helpers' pureStorePath and Go's string handling both behave as they
// would in production, but stable so the test is deterministic.
//
// The hash characters must come from the nix32 alphabet — pureStorePath
// calls builtins.appendContext, which validates the path and rejects
// anything containing E, O, U or T (see nix32Chars).
const (
	fakeBash      = "/nix/store/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bash-5.2"
	fakeCoreutils = "/nix/store/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb-coreutils-9.5"
	fakeCompiler  = "/nix/store/cccccccccccccccccccccccccccccccc-gcc-wrapper-15"
	fakeAR        = "/nix/store/dddddddddddddddddddddddddddddddd-binutils-2.43"
	fakeObj       = "/nix/store/gggggggggggggggggggggggggggggggg-tu-main.o"
	fakeArchive   = "/nix/store/hhhhhhhhhhhhhhhhhhhhhhhhhhhhhhhh-ar-libx.a"
)

// toolchainArgsNix is the three-root prefix every helper takes.
func toolchainArgsNix(compilerOrAR string) string {
	return "  bashRoot      = " + nixStr(fakeBash) + ";\n" +
		"  coreutilsRoot = " + nixStr(fakeCoreutils) + ";\n" +
		"  compilerRoot  = " + nixStr(compilerOrAR) + ";\n"
}

// TestCompileScriptMatchesBuilderNix pins Go's KindCompile script
// against nix/builder.nix.
//
// The srcTree argument is a real directory (Nix imports it at eval time)
// but the compile script never interpolates it — it goes through the
// `$src` env var — so the store path it lands on cannot affect the
// comparison. That indirection is deliberate; see script()'s docstring.
func TestCompileScriptMatchesBuilderNix(t *testing.T) {
	requireNixInstantiate(t)

	// A source tree for srcTree to import. Contents are irrelevant.
	srcTree := t.TempDir()
	if err := os.WriteFile(filepath.Join(srcTree, "main.c"), []byte("int main(){}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name  string
		tool  string
		src   string
		out   string
		flags []string
	}{
		{"no flags", "cc", "main.c", "main.o", nil},
		{"plain flags", "gcc", "main.c", "main.o", []string{"-O2", "-Wall"}},
		{"cxx tool", "g++", "src/util.cc", "util.o", []string{"-std=c++17"}},
		// Shapes absent from all four pinned fixtures — the blind spot
		// that let the quoting bug through.
		{"apostrophe in define", "cc", "main.c", "main.o", []string{"-DMSG=it's"}},
		{"space in define", "cc", "main.c", "main.o", []string{"-DMSG=hello world"}},
		{"backslash in define", "cc", "main.c", "main.o", []string{`-DP=a\b`}},
		{"double quote in define", "cc", "main.c", "main.o", []string{`-DS="x"`}},
		{"dollar and backtick", "cc", "main.c", "main.o", []string{"-DX=$HOME`id`"}},
		{"nix interpolation lookalike", "cc", "main.c", "main.o", []string{"-DX=${notNix}"}},
		{"output name with dots", "cc", "a.b.c", "a.b.o", []string{"-O1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Derivation{
				Kind:      KindCompile,
				Tool:      tc.tool,
				Coreutils: fakeCoreutils,
				Compiler:  fakeCompiler,
				Bash:      fakeBash,
				SrcStore:  "/irrelevant", // never appears in the script
				Source:    tc.src,
				OutName:   tc.out,
				Flags:     tc.flags,
			}
			args := "{\n" + toolchainArgsNix(fakeCompiler) +
				"  toolBasename = " + nixStr(tc.tool) + ";\n" +
				"  srcTree      = " + srcTree + ";\n" +
				"  source       = " + nixStr(tc.src) + ";\n" +
				"  outName      = " + nixStr(tc.out) + ";\n" +
				"  flagsJSON    = " + flagsJSONOf(t, tc.flags) + ";\n}"
			assertSameScript(t, d.script(), nixEvalScript(t, "builder.nix", args))
		})
	}
}

// TestLinkScriptMatchesLinkerNix pins Go's KindLink script against
// nix/linker.nix, including the `-l`-after-inputs split and its
// len(lflags)==0 fallback. Both branches are covered because the
// fallback is what keeps 78 pinned drv hashes stable — a change that
// merged the branches would look harmless here and shift every hash.
func TestLinkScriptMatchesLinkerNix(t *testing.T) {
	requireNixInstantiate(t)

	twoInputs := []derivInput{
		{InputKind: "store", Ref: fakeObj, Name: "main.o"},
		{InputKind: "store", Ref: fakeArchive, Name: "libx.a"},
	}
	twoInputsNix := "[\n" +
		"    { drv = ps " + nixStr(fakeObj) + "; name = \"main.o\"; }\n" +
		"    { drv = ps " + nixStr(fakeArchive) + "; name = \"libx.a\"; }\n" +
		"  ]"

	for _, tc := range []struct {
		name      string
		tool      string
		out       string
		flags     []string
		inputs    []derivInput
		inputsNix string
	}{
		{"no flags no -l", "cc", "prog", nil, twoInputs, twoInputsNix},
		{"flags but no -l (fallback branch)", "c++", "prog",
			[]string{"-O2", "-Wl,-E"}, twoInputs, twoInputsNix},
		{"one -l (split branch)", "cc", "prog",
			[]string{"-O2", "-lm"}, twoInputs, twoInputsNix},
		{"several -l interleaved", "cc", "prog",
			[]string{"-lm", "-O2", "-ldl", "-Wl,--as-needed", "-lpthread"}, twoInputs, twoInputsNix},
		// `-l` alone is not a library flag (len > 2 guard). Both
		// implementations must agree on that or the split desynchronises.
		{"bare -l is not a lib flag", "cc", "prog",
			[]string{"-l", "-O2"}, twoInputs, twoInputsNix},
		{"only -l flags", "cc", "prog",
			[]string{"-lm", "-lc"}, twoInputs, twoInputsNix},
		{"no inputs", "cc", "prog", []string{"-O2"}, nil, "[ ]"},
		{"apostrophe in flag", "cc", "prog",
			[]string{"-DMSG=it's"}, twoInputs, twoInputsNix},
		{"apostrophe and -l together", "cc", "prog",
			[]string{"-DMSG=it's", "-lm"}, twoInputs, twoInputsNix},
		{"-l with exact-name form", "cc", "prog",
			[]string{"-l:libfoo.a"}, twoInputs, twoInputsNix},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Derivation{
				Kind:      KindLink,
				Tool:      tc.tool,
				Coreutils: fakeCoreutils,
				Compiler:  fakeCompiler,
				Bash:      fakeBash,
				OutName:   tc.out,
				Flags:     tc.flags,
				Inputs:    tc.inputs,
			}
			args := "{\n" + toolchainArgsNix(fakeCompiler) +
				"  toolBasename = " + nixStr(tc.tool) + ";\n" +
				"  outName      = " + nixStr(tc.out) + ";\n" +
				"  inputs       = " + tc.inputsNix + ";\n" +
				"  flagsJSON    = " + flagsJSONOf(t, tc.flags) + ";\n}"
			assertSameScript(t, d.script(), nixEvalScriptWithPS(t, "linker.nix", args))
		})
	}
}

// TestArchiveScriptMatchesArchiverNix pins Go's KindArchive script
// against nix/archiver.nix.
//
// Note the PATH asymmetry this locks in: archiver.nix puts
// `compilerRoot` on PATH, and Go's KindArchive puts d.AR there. They
// agree only because the sandbox caller passes the binutils root as AR
// while the native caller passes it as compilerRoot. The test passes the
// same path to both, which is what production does — but it is worth
// knowing that these two fields mean "whatever provides ar" and not what
// their names suggest.
func TestArchiveScriptMatchesArchiverNix(t *testing.T) {
	requireNixInstantiate(t)

	twoInputs := []derivInput{
		{InputKind: "store", Ref: fakeObj, Name: "main.o"},
		{InputKind: "store", Ref: fakeArchive, Name: "libx.a"},
	}
	twoInputsNix := "[\n" +
		"    { drv = ps " + nixStr(fakeObj) + "; name = \"main.o\"; }\n" +
		"    { drv = ps " + nixStr(fakeArchive) + "; name = \"libx.a\"; }\n" +
		"  ]"

	for _, tc := range []struct {
		name      string
		out       string
		arFlags   string
		inputs    []derivInput
		inputsNix string
	}{
		{"rcs", "libfoo.a", "rcs", twoInputs, twoInputsNix},
		{"cru (the helper default)", "libfoo.a", "cru", twoInputs, twoInputsNix},
		{"no inputs", "libempty.a", "rcs", nil, "[ ]"},
		{"single input", "libone.a", "rcs", twoInputs[:1],
			"[\n    { drv = ps " + nixStr(fakeObj) + "; name = \"main.o\"; }\n  ]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := &Derivation{
				Kind:      KindArchive,
				Coreutils: fakeCoreutils,
				Bash:      fakeBash,
				// Native's archiver.nix reads compilerRoot for the ar dir;
				// Go reads AR. Same path either way — see the docstring.
				AR:      fakeAR,
				OutName: tc.out,
				ARFlags: tc.arFlags,
				Inputs:  tc.inputs,
			}
			args := "{\n" + toolchainArgsNix(fakeAR) +
				"  outName = " + nixStr(tc.out) + ";\n" +
				"  inputs  = " + tc.inputsNix + ";\n" +
				"  arFlags = " + nixStr(tc.arFlags) + ";\n}"
			assertSameScript(t, d.script(), nixEvalScriptWithPS(t, "archiver.nix", args))
		})
	}
}

// TestDrvRefInputsRenderIdentically pins the "nix"-kind input path: an
// unrealised sibling drv, which Go renders as a downstream CA output
// placeholder and native mode renders by interpolating the imported
// derivation.
//
// These two CANNOT be compared by string equality — that is the point of
// the test. Go computes the placeholder itself (caOutputPlaceholder);
// native lets Nix compute it while instantiating the sibling. Both
// produce a 53-char `/`-prefixed nix32 string in the same argv slot, and
// drv-equivalence.sh proves the values agree end to end. What is checked
// here is the surrounding layout: same quoting, same `/name` suffix, same
// position relative to flags and other inputs. A layout change would
// otherwise only surface as a hash mismatch in the slow test.
func TestDrvRefInputsRenderIdentically(t *testing.T) {
	drv := "/nix/store/" + strings.Repeat("9", 32) + "-tu-other.o.drv"
	d := &Derivation{
		Kind:      KindLink,
		Tool:      "cc",
		Coreutils: fakeCoreutils,
		Compiler:  fakeCompiler,
		OutName:   "prog",
		Flags:     []string{"-O2"},
		Inputs: []derivInput{
			{InputKind: "store", Ref: fakeObj, Name: "main.o"},
			{InputKind: "nix", Ref: drv, Name: "other.o"},
		},
	}
	got := d.script()

	ph := caOutputPlaceholder(drv, "out")
	if len(ph) != 53 || ph[0] != '/' {
		t.Fatalf("placeholder shape wrong: %q", ph)
	}
	// The placeholder must appear quoted, with the input's basename
	// appended — the exact shape `'${i.drv}/${i.name}'` in linker.nix.
	want := "'" + ph + "/other.o'"
	if !strings.Contains(got, want) {
		t.Errorf("drv input not rendered as %q:\n%s", want, got)
	}
	// And it must sit after the realised input, preserving argv order.
	if strings.Index(got, "main.o'") > strings.Index(got, want) {
		t.Errorf("input order inverted — argv order is load-bearing for ld:\n%s", got)
	}
}

// TestScriptEndsWithNewline pins a property that is easy to break with an
// innocuous edit and expensive to notice: both emitters end the script
// with exactly one trailing newline. Nix's indented-string literal strips
// the common indent and keeps the final newline; Go's fmt.Sprintf has it
// written literally. Adding or dropping one changes every drv hash in the
// project at once.
func TestScriptEndsWithNewline(t *testing.T) {
	for _, d := range []*Derivation{
		{Kind: KindCompile, Tool: "cc", Coreutils: "/C", Compiler: "/G",
			Source: "a.c", OutName: "a.o", Flags: []string{"-O2"}},
		{Kind: KindLink, Tool: "cc", Coreutils: "/C", Compiler: "/G",
			OutName: "p", Flags: []string{"-O2"}},
		{Kind: KindLink, Tool: "cc", Coreutils: "/C", Compiler: "/G",
			OutName: "p", Flags: []string{"-lm"}}, // the other branch
		{Kind: KindArchive, Coreutils: "/C", AR: "/AR",
			OutName: "l.a", ARFlags: "rcs"},
	} {
		s := d.script()
		if !strings.HasSuffix(s, "\n") {
			t.Errorf("kind %d script has no trailing newline: %q", d.Kind, s)
		}
		if strings.HasSuffix(s, "\n\n") {
			t.Errorf("kind %d script has two trailing newlines: %q", d.Kind, s)
		}
	}
}

// nixEvalScriptWithPS is nixEvalScript for the helpers whose `inputs`
// argument needs pure-store-path in scope as `ps`.
func nixEvalScriptWithPS(t *testing.T, helper string, argsNix string) string {
	t.Helper()
	dir, err := filepath.Abs("../../nix")
	if err != nil {
		t.Fatal(err)
	}
	expr := "let ps = import " + filepath.Join(dir, "pure-store-path.nix") + "; in " +
		"builtins.elemAt (import " + filepath.Join(dir, helper) + " " + argsNix + ").drvAttrs.args 1"
	return runNixEval(t, expr)
}

func requireNixInstantiate(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("nix-instantiate"); err != nil {
		t.Skip("nix-instantiate not on PATH")
	}
}

// assertSameScript reports a byte difference between the two emitters
// with enough context to see which line diverged.
func assertSameScript(t *testing.T, goScript, nixScript string) {
	t.Helper()
	if goScript == nixScript {
		return
	}
	gl := strings.Split(goScript, "\n")
	nl := strings.Split(nixScript, "\n")
	t.Errorf("Go script() and the .nix helper disagree — native and sandbox " +
		"modes would produce different drv hashes for the same build")
	for i := 0; i < len(gl) || i < len(nl); i++ {
		g, n := "<absent>", "<absent>"
		if i < len(gl) {
			g = gl[i]
		}
		if i < len(nl) {
			n = nl[i]
		}
		if g != n {
			t.Errorf("  line %d differs:\n    go : %q\n    nix: %q", i+1, g, n)
		}
	}
}
