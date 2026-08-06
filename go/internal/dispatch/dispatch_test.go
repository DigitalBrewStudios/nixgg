package dispatch

import "testing"

// TestFromArgv0 pins the argv[0] → role mapping.
//
// An unclassified name returns ToolUnknown and the shim has no role to
// play, so acceleration silently never engages for that tool: no error,
// no drv, nothing in the log — the build just quietly runs
// unaccelerated. Real build systems invoke compilers under
// target-triple and version decorations, so the matcher has to be
// generous about spelling.
func TestFromArgv0(t *testing.T) {
	for _, tc := range []struct {
		argv0 string
		want  Tool
	}{
		// The six canonical names.
		{"cc", ToolCC},
		{"gcc", ToolGCC},
		{"c++", ToolCXX},
		{"g++", ToolGXX},
		{"ar", ToolAR},
		{"ranlib", ToolRanlib},

		// Full paths — only the basename matters.
		{"/usr/bin/gcc", ToolGCC},
		{"/nix/store/xxx-gcc-wrapper/bin/c++", ToolCXX},
		{"./cc", ToolCC},

		// Version-decorated, as Debian/Fedora ship parallel compilers.
		{"gcc-15", ToolGCC},
		{"g++-14", ToolGXX},
		{"g++-14.2", ToolGXX},
		{"clang-18", ToolGCC},
		{"clang++-18", ToolGXX},

		// Target-triple prefixed, from cross and explicit-triple setups.
		{"x86_64-unknown-linux-gnu-gcc", ToolGCC},
		{"x86_64-linux-gnu-g++", ToolGXX},
		{"aarch64-linux-gnu-ar", ToolAR},
		{"arm-none-eabi-ranlib", ToolRanlib},

		// Both decorations at once.
		{"x86_64-linux-gnu-g++-14", ToolGXX},
		{"x86_64-unknown-linux-gnu-gcc-15", ToolGCC},

		// clang maps onto the gcc/g++ roles: nixgg pins its own
		// compiler, so the role only selects C vs C++ mode.
		{"clang", ToolGCC},
		{"clang++", ToolGXX},

		// binutils equivalents.
		{"llvm-ar", ToolAR},
		{"llvm-ranlib", ToolRanlib},

		// Not compilers.
		{"ld", ToolUnknown},
		{"make", ToolUnknown},
		{"python3", ToolUnknown},
		{"nixgg", ToolUnknown},
		{"", ToolUnknown},
		// A trailing dash leaves nothing to match.
		{"gcc-", ToolUnknown},
	} {
		t.Run(tc.argv0, func(t *testing.T) {
			if got := FromArgv0(tc.argv0); got != tc.want {
				t.Errorf("FromArgv0(%q) = %v, want %v", tc.argv0, got, tc.want)
			}
		})
	}
}

// TestBasenameIsClosedOverSixNames pins the property that makes widening
// FromArgv0 safe for drv hashes: however a tool was spelled on the
// command line, Basename() — which is what lands in the derivation as
// toolBasename — returns one of six canonical strings.
//
// So `gcc-15` dispatches as ToolGCC and the drv still says "gcc". That
// is deliberate, not a lossy shortcut: the drv must name a tool that
// exists inside the sandbox, which contains nixgg's pinned compiler and
// not the caller's versioned one.
func TestBasenameIsClosedOverSixNames(t *testing.T) {
	canonical := map[string]bool{
		"cc": true, "gcc": true, "c++": true,
		"g++": true, "ar": true, "ranlib": true,
	}
	spellings := []string{
		"cc", "gcc", "c++", "g++", "ar", "ranlib",
		"gcc-15", "clang", "clang++", "llvm-ar",
		"x86_64-unknown-linux-gnu-gcc", "x86_64-linux-gnu-g++-14",
		"/usr/bin/gcc", "arm-none-eabi-ranlib",
	}
	for _, s := range spellings {
		tool := FromArgv0(s)
		if tool == ToolUnknown {
			t.Errorf("FromArgv0(%q) = ToolUnknown; expected a real role", s)
			continue
		}
		b := tool.Basename()
		if !canonical[b] {
			t.Errorf("FromArgv0(%q).Basename() = %q, which is not one of the six "+
				"canonical names — this WOULD change toolBasename in the drv and "+
				"break drv-equivalence", s, b)
		}
	}
	// ToolUnknown has no basename; nothing should map to "".
	if got := ToolUnknown.Basename(); got != "" {
		t.Errorf("ToolUnknown.Basename() = %q, want empty", got)
	}
}
