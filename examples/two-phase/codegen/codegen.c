/* Tiny stand-in for llvm-tblgen: prints a C header whose content
 * depends on the argv the caller passes. `codegen banana` produces
 *   #define MSG "banana"
 * The point isn't the content; the point is that phase 2's build
 * has to *exec* this binary mid-build to produce a header, and
 * phase 2's own source #includes that header. */
#include <stdio.h>
int main(int argc, char **argv) {
	const char *word = argc > 1 ? argv[1] : "default";
	printf("#define MSG \"%s\"\n", word);
	return 0;
}
