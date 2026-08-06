package mode

import "testing"

// TestFor pins the realise carveout's pattern set. Each entry exists
// because a real project tripped it, so a "cleanup" that narrows these
// silently breaks that project's configure step.
func TestFor(t *testing.T) {
	for _, tc := range []struct {
		path string
		want Mode
	}{
		// autoconf conftests: `if ./conftest; then ... fi`
		{"conftest.c", Realise},
		{"conftest", Realise},
		{"conftest.cpp", Realise},
		{"/tmp/build/conftest.c", Realise},

		// cmake compiler detection
		{"testCCompiler.c", Realise},
		{"testCXXCompilerABI_C.cpp", Realise},
		{"CMakeCCompilerId.c", Realise},
		{"CMakeCXXCompilerABI_CXX.cpp", Realise},

		// cmake Check* macros
		{"CheckFunctionExists.c", Realise},
		{"CheckIncludeFile.c", Realise},
		{"CheckCSourceCompiles.c", Realise},
		{"CheckCSourceRuns.c", Realise},
		{"CheckSymbolExists.c", Realise},
		{"CheckTypeSize.c", Realise},

		// cmake TryCompile scratch dirs, matched on the path not the base
		{"/b/CMakeFiles/CMakeScratch/x/src.c", Realise},
		{"/b/CMakeFiles/CMakeTmp/src.c", Realise},

		// Ordinary sources defer.
		{"main.c", Placeholder},
		{"src/util.cpp", Placeholder},
		{"parseutils.c", Placeholder},

		// Build-time codegen tools are indistinguishable by name from
		// any other binary — llvm-tblgen vs llvm-config. They defer, and
		// builds that exec one mid-build use a phase split instead. See
		// the package doc comment.
		{"llvm-tblgen", Placeholder},
		{"llvm-min-tblgen", Placeholder},
		{"protoc", Placeholder},

		// Near-misses that must NOT realise.
		{"testing.c", Placeholder},            // "test" prefix, no "Compiler"
		{"Checkers.c", Placeholder},           // "Check" prefix, no known suffix
		{"CMakeLists.txt", Placeholder},       // "CMake" prefix, no "Compiler"
		{"my-conftest-helper.c", Placeholder}, // conftest not at the start
	} {
		t.Run(tc.path, func(t *testing.T) {
			if got := For(tc.path); got != tc.want {
				t.Errorf("For(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
