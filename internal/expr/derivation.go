// Package expr constructs Nix derivations. A single Derivation struct
// holds every field that both wire formats — the `.nix` thunk file
// (native mode, run through `nix-instantiate`) and the JSON drv
// description (sandbox mode, run through `nix derivation add`) — need
// to agree on. Both serializers work off the same struct, so if you
// add a new attribute you can't accidentally set it in one format and
// forget the other.
//
// The invariant we care about: same Derivation → same drv hash,
// regardless of which serializer produces the wire bytes. Enforced
// externally by tests/drv-equivalence.sh (runs both paths, compares
// resulting drv-store-paths).
package expr

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// Kind identifies which of the three build steps this derivation
// represents. Different Kinds emit different shell scripts and
// slightly different helper Nix files, but share the same env-dict
// shape.
type Kind int

const (
	KindCompile Kind = iota // per-TU compile: builder.nix
	KindLink                // linker step: linker.nix
	KindArchive             // ar step: archiver.nix
)

// Derivation is the intermediate representation both serializers
// consume. Fields mirror the union of what builder.nix / linker.nix /
// archiver.nix accept plus the fields Nix itself requires (name,
// system, builder, args, env, inputs, outputs).
//
// Not every field applies to every Kind:
//   - Compile uses SrcStore, Source, OutName, Flags.
//   - Link uses Inputs, Flags.
//   - Archive uses Inputs, ARFlags.
//
// StoreDeps and WrapperEnv apply to all Kinds.
type Derivation struct {
	Kind Kind

	// The name Nix assigns to the derivation's .drv basename. For
	// compile this is "tu-<outName>"; link "bin-<outName>"; archive
	// "ar-<outName>".
	Name string

	// Nix system (e.g. "x86_64-linux").
	System string

	// Toolchain roots — full /nix/store/… paths. All three kinds use
	// bash (builder) + coreutils (PATH). Compile + link use the
	// compiler; archive uses the binutils dir (via AR).
	Bash, Coreutils string
	Compiler        string // gcc-wrapper root; unused by Archive
	AR              string // binutils root (parent of bin/ar); Archive only

	// Compile-specific.
	Tool     string // "cc", "gcc", "c++", "g++"; used by Compile + Link
	SrcStore string // /nix/store/…-<tuID>: the staged src tree
	Source   string // relative to SrcStore, e.g. "src/foo.c"
	OutName  string // e.g. "foo.o"

	// Link + Archive: inputs (either sibling drvs or already-realised
	// store paths). Compile leaves this empty.
	Inputs []derivInput

	// Link + Compile: compiler flags. Archive uses ARFlags instead.
	Flags []string

	// Archive-only: `ar` modifier string (e.g. "rcs").
	ARFlags string

	// /nix/store/… roots referenced by Flags or WrapperEnv content;
	// must be mounted in the sandbox. Serialized as _storeDeps env var
	// (colon-joined) and threaded into inputs.srcs (JSON mode) /
	// storeDepsJSON (native mode).
	StoreDeps []string

	// Nix's gcc-wrapper env (NIX_CFLAGS_COMPILE, NIX_LDFLAGS, etc.).
	// Preserved verbatim in the env dict.
	WrapperEnv map[string]string
}

// derivInput is the internal per-input record used by Derivation's
// serializers. Kept separate from the shim-facing Input type in
// expr.go (which uses Kind="store"/"nix" via a different field name
// for historical reasons). inputsFromExpr / inputsFromJSON convert.
type derivInput struct {
	// InputKind = "store" (already realised, use the store path as
	// builtins.storePath) or "nix" (a sibling drv/thunk we
	// reference — becomes `import /path/thunk.nix` in native mode
	// and inputs.drvs entry in JSON mode).
	InputKind string
	// Ref: for "store", a canonical /nix/store/<hash>-<name> root;
	// for "nix", the absolute path to the sibling drv/thunk.
	Ref string
	// Name: the basename inside that ref's output dir (e.g. "foo.o").
	Name string
}

// outputPlaceholder returns the `builtins.placeholder "out"` value —
// what every derivation's `$out` env var interpolates to at build
// time. Same digest for every CA derivation with a single `out`
// output; caching for one call site is fine.
func outputPlaceholder() string { return "/" + OutPlaceholderNix32 }

// Native mode's script carries markers where only Nix knows the value at
// eval time: an input may be an unrealised sibling thunk whose drv hash —
// hence CA output placeholder — doesn't exist yet, and the toolchain roots
// are the helper's own arguments. nix/resolve-script.nix substitutes them;
// store context survives replaceStrings, so dependency edges stay intact.
//
// The tag is per-script, not fixed. Markers are just text, so a flag
// spelling one (`-DAT=@NIXGG_COMPILER@`) would be substituted too.
const markerTagBase = "NIXGG"

// markerTag returns a tag whose markers cannot collide with anything
// already in `body`. Deterministic: same body → same tag, which matters
// because the tag ends up in the thunk text and thus in its hash.
func markerTag(body string) string {
	for n := 0; ; n++ {
		tag := markerTagBase
		if n > 0 {
			tag = markerTagBase + strconv.Itoa(n)
		}
		if !strings.Contains(body, "@"+tag+"_") {
			return tag
		}
	}
}

// Marker spellings. The helper reconstructs these from the tag it is
// passed, so any change here needs the matching change in
// nix/resolve-script.nix — TestNativeTemplateResolvesToSandboxScript runs
// the real helper and fails if they disagree.
func coreutilsMarker(tag string) string { return "@" + tag + "_COREUTILS@" }
func compilerMarker(tag string) string  { return "@" + tag + "_COMPILER@" }
func inputMarker(tag string, i int) string {
	return "@" + tag + "_INPUT" + strconv.Itoa(i) + "@"
}

// script returns the bash `-c` body with every store path resolved.
// This is what sandbox mode bakes into its JSON drv.
func (d *Derivation) script() string {
	return d.buildScript("", d.Coreutils, d.compilerOrAR())
}

// scriptTemplate returns the same script with markers where native mode
// cannot know the value yet, plus the tag those markers use. The thunk
// passes both to nix/resolve-script.nix.
func (d *Derivation) scriptTemplate() (template, tag string) {
	// Choose the tag against the resolved body: it contains the same flag
	// text as the template, and unlike the template it has no markers of
	// its own to confuse the scan.
	tag = markerTag(d.script())
	return d.buildScript(tag, coreutilsMarker(tag), compilerMarker(tag)), tag
}

// compilerOrAR reports which store path provides the tools on PATH.
// Compile and Link use the compiler; Archive uses whatever supplies
// `ar` — which native mode passes as compilerRoot and sandbox mode as
// AR. Two names, one meaning.
func (d *Derivation) compilerOrAR() string {
	if d.Kind == KindArchive {
		return d.AR
	}
	return d.Compiler
}

// buildScript is the single source of the shell body — the layout,
// quoting, and argv order that both wire formats must agree on.
//
// tag == "" means resolve everything (sandbox mode). A non-empty tag
// means emit input markers with that tag (native mode); coreutils and
// compiler are then marker text too.
func (d *Derivation) buildScript(tag, coreutils, compiler string) string {
	pathPrefix := fmt.Sprintf(`export PATH="%s/bin:%s/bin"`, coreutils, compiler)

	inputs := func() string {
		parts := make([]string, 0, len(d.Inputs))
		for i, in := range d.Inputs {
			if tag != "" {
				parts = append(parts, "'"+inputMarker(tag, i)+"'")
				continue
			}
			switch in.InputKind {
			case "store":
				ref := in.Ref
				if !strings.HasPrefix(ref, "/nix/store/") {
					ref = "/nix/store/" + ref
				}
				parts = append(parts, fmt.Sprintf("'%s/%s'", ref, in.Name))
			case "nix":
				parts = append(parts,
					fmt.Sprintf("'%s/%s'", caOutputPlaceholder(in.Ref, "out"), in.Name))
			}
		}
		return strings.Join(parts, " ")
	}

	switch d.Kind {
	case KindCompile:
		return fmt.Sprintf(
			`set -euo pipefail
%s
mkdir -p "$out"
cd "$src"
"%s" %s -c "$source" -o "$out/$outName"
`, pathPrefix, d.Tool, shellQuoteFlags(d.Flags))
	case KindLink:
		// Split flags into non-`-l` and `-l<name>` — the classic
		// single-pass ld resolves libraries against object files
		// mentioned BEFORE them. Emit inputs (objects/archives)
		// between the two groups so `-lm`/`-lc` come last, after
		// every object/archive that might reference libm/libc.
		// When there are no `-l` flags, fall through to the plain
		// flags-then-inputs layout so drv content stays byte-
		// identical to the pre-`-l`-split era (no trailing empty
		// slot). This keeps the equivalence set for hello/lua/mosh
		// stable — those never had `-l` flags to begin with.
		var lflags, nonLflags []string
		for _, f := range d.Flags {
			if strings.HasPrefix(f, "-l") && len(f) > 2 {
				lflags = append(lflags, f)
			} else {
				nonLflags = append(nonLflags, f)
			}
		}
		if len(lflags) == 0 {
			return fmt.Sprintf(
				`set -euo pipefail
%s
mkdir -p "$out"
"%s" %s %s -o "$out/%s"
`, pathPrefix, d.Tool, shellQuoteFlags(d.Flags), inputs(), d.OutName)
		}
		return fmt.Sprintf(
			`set -euo pipefail
%s
mkdir -p "$out"
"%s" %s %s %s -o "$out/%s"
`, pathPrefix, d.Tool, shellQuoteFlags(nonLflags), inputs(), shellQuoteFlags(lflags), d.OutName)
	case KindArchive:
		// `ar` is taken from PATH (set above) and `D` is prepended to
		// arFlags for a deterministic archive.
		return fmt.Sprintf(
			`set -euo pipefail
%s
mkdir -p "$out"
ar D%s "$out/%s" %s
`, pathPrefix, d.ARFlags, d.OutName, inputs())
	}
	return ""
}

// ToNix serialises this Derivation as an `import <helper>.nix { … }`
// expression suitable for writing to .nixgg/thunks/<id>.nix. The
// helper (builder.nix / linker.nix / archiver.nix, all in nix/) is
// still what actually calls `derivation`; this emitter just fills
// in its arguments from our shared struct — so if we add a field
// to Derivation, both this native path and toJSON pick it up.
//
// helpers must be the /nix/store/…-nixgg-nix root.
//
// The bytes this produces are byte-equivalent to what the pre-
// Derivation-struct emitters produced (Compile/Link/Archive in
// expr.go). Verified externally by tests/drv-equivalence.sh.
func (d *Derivation) ToNix(helpers string) string {
	tmpl, tag := d.scriptTemplate()
	var b strings.Builder
	switch d.Kind {
	case KindCompile:
		fmt.Fprintf(&b, "import %s/builder.nix {\n", helpers)
		fmt.Fprintf(&b, "  srcTree        = %s;\n", d.SrcStore) // Nix path literal, unquoted
		fmt.Fprintf(&b, "  source         = %q;\n", d.Source)
		fmt.Fprintf(&b, "  outName        = %q;\n", d.OutName)
		fmt.Fprintf(&b, "  scriptTemplate = %s;\n", nixIndentedStringLiteral(tmpl))
		fmt.Fprintf(&b, "  markerTag      = %q;\n", tag)
		fmt.Fprintf(&b, "  storeDepsJSON  = ''%s'';\n", jsonArrayIndented(d.StoreDeps))
		fmt.Fprintf(&b, "  wrapperEnvJSON = ''%s'';\n", jsonObjectSorted(d.WrapperEnv))
	case KindLink:
		fmt.Fprintf(&b, "import %s/linker.nix {\n", helpers)
		fmt.Fprintf(&b, "  outName        = %q;\n", d.OutName)
		fmt.Fprintf(&b, "  inputs         = %s;\n", derivInputsList(d.Inputs))
		fmt.Fprintf(&b, "  scriptTemplate = %s;\n", nixIndentedStringLiteral(tmpl))
		fmt.Fprintf(&b, "  markerTag      = %q;\n", tag)
		fmt.Fprintf(&b, "  storeDepsJSON  = ''%s'';\n", jsonArrayIndented(d.StoreDeps))
		fmt.Fprintf(&b, "  wrapperEnvJSON = ''%s'';\n", jsonObjectSorted(d.WrapperEnv))
	case KindArchive:
		fmt.Fprintf(&b, "import %s/archiver.nix {\n", helpers)
		fmt.Fprintf(&b, "  outName        = %q;\n", d.OutName)
		fmt.Fprintf(&b, "  inputs         = %s;\n", derivInputsList(d.Inputs))
		fmt.Fprintf(&b, "  scriptTemplate = %s;\n", nixIndentedStringLiteral(tmpl))
		fmt.Fprintf(&b, "  markerTag      = %q;\n", tag)
		fmt.Fprintf(&b, "  storeDepsJSON  = ''%s'';\n", jsonArrayIndented(d.StoreDeps))
		fmt.Fprintf(&b, "  wrapperEnvJSON = ''%s'';\n", jsonObjectSorted(d.WrapperEnv))
	}
	b.WriteString("}\n")
	return b.String()
}

// derivInputsList renders `inputs = [ ... ]` for linker/archiver
// helpers, so they can interpolate each input into the script template.
func derivInputsList(inputs []derivInput) string {
	if len(inputs) == 0 {
		return "[ ]"
	}
	var b strings.Builder
	b.WriteString("[ ")
	for _, in := range inputs {
		var drv string
		switch in.InputKind {
		case "store":
			ref := in.Ref
			if strings.HasPrefix(ref, "/nix/store/") {
				drv = fmt.Sprintf("builtins.storePath %q", ref)
			} else {
				drv = fmt.Sprintf("builtins.storePath \"/nix/store/%s\"", ref)
			}
		case "nix":
			drv = "import " + in.Ref
		}
		fmt.Fprintf(&b, "{ drv = %s; name = %q; } ", drv, in.Name)
	}
	b.WriteString("]")
	return b.String()
}

// jsonObjectSorted renders a map as a sorted-key JSON object,
// byte-deterministic. Used for wrapperEnvJSON.
func jsonObjectSorted(m map[string]string) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		kj, _ := jsonMarshal(k)
		vj, _ := jsonMarshal(m[k])
		b.Write(kj)
		b.WriteByte(':')
		b.Write(vj)
	}
	b.WriteByte('}')
	return b.String()
}

// jsonMarshal is a thin wrapper for readability.
func jsonMarshal(v any) ([]byte, error) { return json.Marshal(v) }

// toJSON serialises this Derivation for `nix derivation add`. The
// caller passes extraSrcs (basenames of already-realised store paths
// the sandbox must mount) and optional wrapper-env overrides via
// WrapperEnv on the struct. Inputs are split into inputs.drvs (for
// Kind=="nix") and inputs.srcs (for Kind=="store").
func (d *Derivation) toJSON(extraSrcs []string, _reserved any) JSONDrv {
	drvs := map[string]JSONDrvRef{}
	srcs := append([]string{}, extraSrcs...)
	seenSrc := map[string]bool{}
	for _, s := range srcs {
		seenSrc[s] = true
	}
	for _, in := range d.Inputs {
		switch in.InputKind {
		case "nix":
			refKey := in.Ref
			if i := strings.LastIndexByte(refKey, '/'); i >= 0 {
				refKey = refKey[i+1:]
			}
			ref := drvs[refKey]
			ref.Outputs = appendUnique(ref.Outputs, "out")
			if ref.DynamicOutputs == nil {
				ref.DynamicOutputs = map[string]any{}
			}
			drvs[refKey] = ref
		case "store":
			base := in.Ref
			if i := strings.LastIndexByte(base, '/'); i >= 0 {
				base = base[i+1:]
			}
			if !seenSrc[base] {
				srcs = append(srcs, base)
				seenSrc[base] = true
			}
		}
	}
	for _, sd := range d.StoreDeps {
		base := sd
		if i := strings.LastIndexByte(base, '/'); i >= 0 {
			base = base[i+1:]
		}
		if !seenSrc[base] {
			srcs = append(srcs, base)
			seenSrc[base] = true
		}
	}
	return JSONDrv{
		Name:    d.Name,
		System:  d.System,
		Builder: d.Bash + "/bin/bash",
		Args:    []string{"-c", d.script()},
		Env:     d.envDict(),
		Inputs: JSONDrvInputs{
			Drvs: drvs,
			Srcs: srcs,
		},
		Outputs: map[string]JSONOut{
			"out": {Method: "nar", HashAlgo: "sha256"},
		},
		Version: 4,
	}
}

// envDict is the env-var dict every derivation gets. Both serializers
// use it as-is — that's what pins them to the same hash.
func (d *Derivation) envDict() map[string]string {
	env := map[string]string{
		"out":            outputPlaceholder(),
		"name":           d.Name,
		"system":         d.System,
		"builder":        d.Bash + "/bin/bash",
		"outputHashAlgo": "sha256",
		"outputHashMode": "nar",
		"_storeDeps":     strings.Join(d.StoreDeps, ":"),
	}
	// Compile derivations pass source/outName/src via env vars too
	// (that's what builder.nix's script consults — see the
	// `cd "$src"` / `"$source"` / `"$outName"` in script()).
	if d.Kind == KindCompile {
		env["src"] = d.SrcStore
		env["source"] = d.Source
		env["outName"] = d.OutName
	}
	for k, v := range d.WrapperEnv {
		env[k] = v
	}
	return env
}

// nixIndentedStringLiteral renders `s` as a Nix indented-string literal.
//
// Two constructs inside that form are not literal, and both are reachable
// from user flags: a doubled apostrophe closes the string, and `${` opens
// an interpolation. Unescaped, they give a thunk that won't parse or that
// interpolates at eval time.
//
// Order matters: the apostrophe rule runs first, or it would also rewrite
// the apostrophes introduced when escaping `${`.
func nixIndentedStringLiteral(s string) string {
	e := strings.ReplaceAll(s, "''", "'''")
	e = strings.ReplaceAll(e, "${", "''${")
	return "''" + e + "''"
}
