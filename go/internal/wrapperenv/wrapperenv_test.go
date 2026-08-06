package wrapperenv

import (
	"os"
	"strings"
	"testing"
)

// clearEnv unsets everything JSON() reads, so a test starts from a known
// state regardless of what the ambient shell exported. t.Setenv restores
// the originals when the test ends.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, b := range bases {
		t.Setenv(b, "")
		t.Setenv(b+"_x86_64_unknown_linux_gnu", "")
	}
	// detectTriple scans all of os.Environ() for the prefix, so any
	// pre-existing trigger var has to go too — t.Setenv to "" still
	// leaves the key present and detectTriple only checks the prefix.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "NIX_CC_WRAPPER_TARGET_HOST_") {
			eq := strings.IndexByte(kv, '=')
			if eq > 0 {
				k := kv[:eq]
				t.Setenv(k, "")
				if err := os.Unsetenv(k); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// TestJSONEmptyEnvIsEmptyObject pins that a build with no wrapper flags
// produces exactly "{}" — not an object holding empty-string values, and
// not the activation trigger on its own.
//
// This is the shape half of the invariant. wrapperEnvJSON is embedded
// verbatim in every compile/link/archive drv, so its byte content is a
// hash input. Native mode (mkShellNoCC) sets no wrapper vars at all, so
// if sandbox mode emitted `{"NIX_LDFLAGS":" "}` or a bare trigger, every
// hello/lua/mosh drv would diverge between modes. That exact bug shipped
// once (an all-whitespace NIX_LDFLAGS surviving as " ") and was fixed in
// fe671b4.
func TestJSONEmptyEnvIsEmptyObject(t *testing.T) {
	clearEnv(t)
	got, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	if got != "{}" {
		t.Errorf("JSON() = %q, want %q — a non-empty object here diverges from "+
			"native mode, which sets no wrapper vars", got, "{}")
	}
}

// TestJSONWhitespaceOnlyIsTreatedAsAbsent pins that a var holding only
// whitespace is dropped rather than emitted.
//
// Regression origin: mkNixggBuild's preBuild scrubs -frandom-seed and
// -rpath out of NIX_CFLAGS_COMPILE / NIX_LDFLAGS with sed. When those
// were the only contents, the result was a single space, not an empty
// string — so the var was still "set" and leaked into the drv as " ",
// diverging from native mode where it was never set at all.
func TestJSONWhitespaceOnlyIsTreatedAsAbsent(t *testing.T) {
	for _, val := range []string{" ", "  ", "\t", " \n "} {
		t.Run(strings.ReplaceAll(val, "\n", "\\n"), func(t *testing.T) {
			clearEnv(t)
			t.Setenv("NIX_LDFLAGS", val)
			got, err := JSON()
			if err != nil {
				t.Fatal(err)
			}
			if got != "{}" {
				t.Errorf("whitespace-only NIX_LDFLAGS=%q leaked: JSON() = %q, want %q",
					val, got, "{}")
			}
		})
	}
}

// TestJSONTriggerOnlyWithRealFlags pins the activation-trigger gate:
// NIX_CC_WRAPPER_TARGET_HOST_<triple> is emitted only when there is at
// least one real wrapper flag to activate on.
//
// The trigger is what makes the INNER drv's cc-wrapper inject
// buildInputs' -isystem/-L. Emitting it unconditionally would add a key
// to every drv's env, including empty-buildInputs builds where native
// mode emits nothing — diverging all 78 pinned hello/lua/mosh hashes.
func TestJSONTriggerOnlyWithRealFlags(t *testing.T) {
	const triple = "x86_64_unknown_linux_gnu"
	const trigger = "NIX_CC_WRAPPER_TARGET_HOST_" + triple

	t.Run("trigger set but no flags -> empty", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(trigger, "1")
		got, err := JSON()
		if err != nil {
			t.Fatal(err)
		}
		if got != "{}" {
			t.Errorf("trigger emitted with no flags to activate on: %q", got)
		}
	})

	t.Run("trigger plus a real flag -> both", func(t *testing.T) {
		clearEnv(t)
		t.Setenv(trigger, "1")
		t.Setenv("NIX_CFLAGS_COMPILE", "-isystem /nix/store/x/include")
		got, err := JSON()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, trigger) {
			t.Errorf("trigger missing with a real flag present — the inner "+
				"cc-wrapper will not inject buildInputs' paths: %q", got)
		}
		if !strings.Contains(got, "NIX_CFLAGS_COMPILE") {
			t.Errorf("flag missing: %q", got)
		}
	})

	t.Run("flag without trigger -> flag only", func(t *testing.T) {
		clearEnv(t)
		t.Setenv("NIX_CFLAGS_COMPILE", "-isystem /nix/store/x/include")
		got, err := JSON()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "NIX_CC_WRAPPER_TARGET_HOST_") {
			t.Errorf("trigger invented with no triple in env: %q", got)
		}
	})
}

// TestJSONKeysAreSorted pins deterministic key order. The output string
// is a drv hash input, so map iteration order must never reach it — two
// runs with identical env must produce identical bytes.
func TestJSONKeysAreSorted(t *testing.T) {
	clearEnv(t)
	t.Setenv("NIX_CFLAGS_COMPILE", "-Ione")
	t.Setenv("NIX_LDFLAGS", "-Ltwo")
	t.Setenv("NIX_HARDENING_ENABLE", "pic")

	first, err := JSON()
	if err != nil {
		t.Fatal(err)
	}
	// Repeat enough times that Go's randomized map iteration would show
	// up if it could leak through.
	for i := 0; i < 50; i++ {
		got, err := JSON()
		if err != nil {
			t.Fatal(err)
		}
		if got != first {
			t.Fatalf("JSON() is not deterministic across calls:\n first: %q\n  then: %q", first, got)
		}
	}

	// Verify the keys really are ascending, not just stable.
	var keys []string
	for _, part := range strings.Split(strings.Trim(first, "{}"), ",") {
		if q := strings.Index(part, "\":"); q > 0 {
			keys = append(keys, strings.Trim(part[:q+1], "\""))
		}
	}
	for i := 1; i < len(keys); i++ {
		if keys[i-1] > keys[i] {
			t.Errorf("keys not sorted: %q (from %q)", keys, first)
			break
		}
	}
}
