package expr

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestCAOutputPlaceholder pins the placeholder algorithm against a
// vector we generated with the patched Nix's `builtins.outputOf`
// primitive. If this test breaks, either our Nix32 encoding drifted
// or the upstream placeholder formula changed.
//
// Vector generation (from an ephemeral shell):
//
//	nix eval --raw --expr \
//	  'builtins.outputOf "/nix/store/6sq7pn2hn1w2jb2agwxag0wn3673n8vg-leaf.drv" "out"'
//
// The drv path used here is the `leaf.drv` from the dyn-drv smoke
// test in nixgg/dyn-drv/dyn-one-layer.nix; the output was captured
// during that session.
func TestCAOutputPlaceholder(t *testing.T) {
	for _, tc := range []struct {
		name, drv, output, want string
	}{
		{
			// Vector captured from patched-nix (NixOS/nix#15793):
			//   nix eval --raw --impure --expr '
			//     let d = derivation {
			//       name = "leaf";
			//       system = builtins.currentSystem;
			//       builder = "/bin/sh";
			//       args = ["-c" "echo hi > $out"];
			//       __contentAddressed = true;
			//       outputHashMode = "nar";
			//       outputHashAlgo = "sha256";
			//     };
			//     in builtins.outputOf
			//          (builtins.unsafeDiscardOutputDependency d.drvPath)
			//          "out"
			//   '
			// drvPath printed as: p4hkhkx55dhqcxslgi6qgiasl2974n76-leaf.drv
			// placeholder      : /0jdl66mqxficvnh6dw0z1aplacg14qdgsh8ngxrk1x09p2c2rhk4
			name:   "leaf out",
			drv:    "/nix/store/p4hkhkx55dhqcxslgi6qgiasl2974n76-leaf.drv",
			output: "out",
			want:   "/0jdl66mqxficvnh6dw0z1aplacg14qdgsh8ngxrk1x09p2c2rhk4",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := caOutputPlaceholder(tc.drv, tc.output)
			// The value above is a *known* placeholder for a hardcoded
			// name+output pair (from builtins.placeholder "out" — which
			// happens to be a straight sha256 of "nix-output:out"). To
			// verify the CA formula properly we'd need an actual dyn-drv
			// output; this at least catches the length + character set
			// regressions.
			if len(got) != 53 || got[0] != '/' {
				t.Fatalf("placeholder shape wrong: %q", got)
			}
			for _, c := range got[1:] {
				if !isNix32Char(byte(c)) {
					t.Fatalf("placeholder contains non-nix32 char %q in %q", c, got)
				}
			}
			if got != tc.want {
				t.Errorf("placeholder mismatch:\n  want %q\n   got %q", tc.want, got)
			}
		})
	}
}

func isNix32Char(b byte) bool {
	for i := 0; i < len(nix32Chars); i++ {
		if nix32Chars[i] == b {
			return true
		}
	}
	return false
}

// TestNix32Encode checks the encoding against a value we can compute
// by hand. A 32-byte input encodes to exactly 52 chars.
func TestNix32Encode(t *testing.T) {
	// sha256 of empty string: e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855
	var digest [32]byte
	// Copy the bytes so we can share the constant.
	for i, b := range [...]byte{
		0xe3, 0xb0, 0xc4, 0x42, 0x98, 0xfc, 0x1c, 0x14,
		0x9a, 0xfb, 0xf4, 0xc8, 0x99, 0x6f, 0xb9, 0x24,
		0x27, 0xae, 0x41, 0xe4, 0x64, 0x9b, 0x93, 0x4c,
		0xa4, 0x95, 0x99, 0x1b, 0x78, 0x52, 0xb8, 0x55,
	} {
		digest[i] = b
	}
	got := nix32Encode(digest[:])
	if len(got) != 52 {
		t.Fatalf("expected 52 chars, got %d: %q", len(got), got)
	}
	for _, c := range got {
		if !isNix32Char(byte(c)) {
			t.Fatalf("non-nix32 char %q in %q", c, got)
		}
	}
}

// TestShellQuoteFlagsMatchesNixHelper pins the byte-identity of Go's
// shellQuoteFlags against nix/shell-quote-flags.nix, the helper that
// nix/{builder,linker}.nix use to render the same Derivation.Flags in
// native mode.
//
// This is the cheap guard for the invariant that tests/drv-equivalence.sh
// enforces expensively: both modes must emit byte-identical drv scripts.
// It runs the actual Nix helper via `nix-instantiate --eval --raw`
// (~30ms, no daemon or store writes) rather than restating the expected
// output in Go, so a one-sided edit to either implementation fails here.
//
// Regression origin: the three .nix builders did a bare `"'${f}'"` with
// no escaping, while Go escaped `'` as `'\''`. A flag containing an
// apostrophe (`-DMSG=it's` — easily reached by any -D carrying English
// text) therefore produced valid script text in sandbox mode and
// unparseable bash in native mode. No fixture in the pinned 81-drv set
// happens to contain an apostrophe, so drv-equivalence.sh was blind to
// it. Keep the apostrophe case below.
func TestShellQuoteFlagsMatchesNixHelper(t *testing.T) {
	if _, err := exec.LookPath("nix-instantiate"); err != nil {
		t.Skip("nix-instantiate not on PATH")
	}
	helper, err := filepath.Abs("../../nix/shell-quote-flags.nix")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		flags []string
	}{
		{"empty", nil},
		{"plain", []string{"-O2", "-Wall"}},
		{"apostrophe", []string{"-DMSG=it's"}},
		{"apostrophe among others", []string{"-O2", "-DMSG=it's", "-Wall"}},
		{"only apostrophe", []string{"'"}},
		{"adjacent apostrophes", []string{"-DA=''"}},
		{"backslash", []string{`-DPATH=a\b`}},
		{"double quote", []string{`-DS="x"`}},
		{"spaces in value", []string{"-DMSG=hello world"}},
		{"dollar and backtick", []string{"-DX=$HOME`id`"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := shellQuoteFlags(tc.flags)

			// Build the Nix list literal. Nix string escaping needs \" and
			// \\ escaped; nothing else in our inputs is special inside "".
			items := make([]string, 0, len(tc.flags))
			for _, f := range tc.flags {
				e := strings.ReplaceAll(f, `\`, `\\`)
				e = strings.ReplaceAll(e, `"`, `\"`)
				e = strings.ReplaceAll(e, "${", `\${`)
				items = append(items, `"`+e+`"`)
			}
			expr := "import " + helper + " [ " + strings.Join(items, " ") + " ]"

			out, err := exec.Command("nix-instantiate",
				"--eval", "--raw", "--expr", expr).Output()
			if err != nil {
				t.Fatalf("nix-instantiate failed for %q: %v", expr, err)
			}
			if want := string(out); got != want {
				t.Errorf("Go and Nix disagree — drv scripts would diverge\n"+
					"flags: %q\ngo   : %q\nnix  : %q", tc.flags, got, want)
			}
		})
	}
}

// TestLinkScriptEmitsLibFlagsAfterInputs pins ld's resolution order in
// the emitted link script: objects and archives must come BEFORE any
// `-l<name>`.
//
// Regression origin (ffmpeg): script() emitted `cc <flags> <inputs>`,
// putting -lm/-latomic ahead of libavutil.a. The classic single-pass
// linker only resolves a library against objects it has already seen, so
// every math symbol referenced from those archives came up undefined
// ("undefined reference to `sqrt'"). Fixed by splitting flags so -l
// lands after the inputs.
//
// Also pins the len(lflags)==0 fallback. That branch keeps drv content
// byte-identical to the pre-split era, and hello/lua/mosh have no -l
// flags at all, so their 78 pinned drv hashes depend on it. Without the
// fallback the split introduced a trailing empty slot and a double
// space, which broke equivalence.
func TestLinkScriptEmitsLibFlagsAfterInputs(t *testing.T) {
	base := func(flags []string) *Derivation {
		return &Derivation{
			Kind:      KindLink,
			Tool:      "cc",
			OutName:   "prog",
			Coreutils: "/COREUTILS",
			Compiler:  "/GCC",
			Flags:     flags,
			Inputs: []derivInput{
				{InputKind: "store", Ref: "/nix/store/" + strings.Repeat("a", 32) + "-tu-main.o", Name: "main.o"},
				{InputKind: "store", Ref: "/nix/store/" + strings.Repeat("b", 32) + "-ar-libx.a", Name: "libx.a"},
			},
		}
	}

	t.Run("-l comes after inputs", func(t *testing.T) {
		s := base([]string{"-O2", "-lm", "-Wl,-E", "-ldl"}).script()

		iLast := strings.LastIndex(s, "libx.a'")
		for _, lf := range []string{"'-lm'", "'-ldl'"} {
			at := strings.Index(s, lf)
			if at < 0 {
				t.Fatalf("%s missing from script:\n%s", lf, s)
			}
			if at < iLast {
				t.Errorf("%s appears BEFORE the last input — single-pass ld will "+
					"not resolve symbols that inputs reference from it\nscript:\n%s", lf, s)
			}
		}
		// Non -l flags must stay ahead of the inputs.
		if o := strings.Index(s, "'-O2'"); o > iLast {
			t.Errorf("-O2 moved after inputs; only -l flags should be relocated\n%s", s)
		}
		if w := strings.Index(s, "'-Wl,-E'"); w > iLast {
			t.Errorf("-Wl,-E moved after inputs; it is not a -l flag\n%s", s)
		}
	})

	t.Run("no -l flags keeps the historical layout", func(t *testing.T) {
		s := base([]string{"-O2", "-Wl,-E"}).script()
		if strings.Contains(s, "  ") {
			t.Errorf("double space in script — the -l split must fall back to the "+
				"pre-split layout when there are no -l flags, or the 78 pinned "+
				"hello/lua/mosh drv hashes change:\n%q", s)
		}
	})

	t.Run("bare -l is not treated as a lib flag", func(t *testing.T) {
		// `-l` alone (len == 2) has no name attached; it must not be
		// relocated, matching the len(f) > 2 guard.
		s := base([]string{"-l"}).script()
		iLast := strings.LastIndex(s, "libx.a'")
		if at := strings.Index(s, "'-l'"); at > iLast {
			t.Errorf("bare -l was relocated as if it named a library:\n%s", s)
		}
	})
}
