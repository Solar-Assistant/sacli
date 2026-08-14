package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The portal command is a second implementation of what sa_cloud does in Elixir,
// and the failure worth guarding against is drift rather than a crash: a preview
// showing what production would not serve is worse than having no preview, because
// somebody then builds against behaviour that does not exist.
//
// These cases are the ones sa_cloud states as authoritative, so a failure here is
// a real defect rather than a difference of reading. Change them only alongside a
// change on that side.

func TestPortalPrimaryResolution(t *testing.T) {
	tests := []struct {
		name  string
		org   portalOrg
		light string
		dark  string
	}{
		// A stated colour has no privileged rung. It is a candidate like any
		// other and wins only by being legible on the theme it is measured
		// against, so these three cases have to disagree with each other.
		{
			name:  "a stated primary is used where it passes",
			org:   portalOrg{ColorPrimary: "rgb(81,137,14)", Color2: "rgba(55,169,232,1)"},
			light: "rgb(81,137,14)",
			dark:  "rgb(81,137,14)",
		},
		{
			// An implementation that short-circuits on the column being set
			// serves this tint, which is the invisible sign-in button itself.
			name:  "a stated primary that fails is rejected for the next candidate",
			org:   portalOrg{ColorPrimary: "rgba(126,213,21,0.1)", Color2: "rgba(81,137,14,1)"},
			light: "rgba(81,137,14,1)", // stated is 1.07:1 on white
			dark:  "rgba(81,137,14,1)", // and 1.21:1 on #1a1a1a
		},
		{
			// The half nobody looks at: a short-circuit gets light right and dark
			// wrong, because the stated colour is genuinely legible — on white.
			name:  "a stated primary is judged per theme, not once",
			org:   portalOrg{ColorPrimary: "rgba(47,43,115,1)"},
			light: "rgba(47,43,115,1)",  // 12.25:1 on white
			dark:  portalNeutralPrimary, // 1.42:1 on #1a1a1a
		},
		{
			name:  "no stated primary falls back to the saturated stop while it is legible",
			org:   portalOrg{Color2: "rgba(81,137,14,1)"}, // 4.26:1 on white
			light: "rgba(81,137,14,1)",
			dark:  "rgba(81,137,14,1)",
		},
		// The two themes disagree for 85% of branded organizations, so neither
		// direction below is an edge case — but they take different paths, and a
		// suite holding only the first never exercises the second.
		{
			// A pale brand: the dark chain falls through to the stop and succeeds
			// where the light one refused it.
			name:  "an illegible stop is refused for light and taken for dark",
			org:   portalOrg{Color2: "rgba(55,169,232,1)"}, // 2.62:1 white, 6.63:1 dark
			light: portalNeutralPrimary,
			dark:  "rgba(55,169,232,1)",
		},
		{
			// A dark brand, and the direction everybody reasons past: the light
			// chain succeeds, and the dark one exhausts every candidate. Nothing
			// else here makes the dark chain run out after light has found a hue.
			name:  "a colour legible only on white exhausts the dark chain",
			org:   portalOrg{Color2: "rgba(47,43,115,1)"}, // 12.25:1 white, 1.42:1 dark
			light: "rgba(47,43,115,1)",
			dark:  portalNeutralPrimary,
		},
		{
			name:  "a fully transparent colour is the background, not its hue",
			org:   portalOrg{Color2: "rgba(0,0,0,0)"},
			light: portalNeutralPrimary,
			dark:  portalNeutralPrimary,
		},
		{
			name:  "blank columns resolve to the neutral",
			org:   portalOrg{},
			light: portalNeutralPrimary,
			dark:  portalNeutralPrimary,
		},
		{
			name:  "an unparseable colour is never chosen",
			org:   portalOrg{ColorPrimary: "cornflowerblue", Color2: "rgba(81,137,14,1)"},
			light: "rgba(81,137,14,1)",
			dark:  "rgba(81,137,14,1)",
		},
		{
			name: "the dark chain borrows the light primary before giving up on the hue",
			org: portalOrg{
				ColorPrimary: "rgba(81,137,14,1)",
				ColorDark2:   "rgba(0,0,0,1)", // illegible on #1a1a1a
			},
			light: "rgba(81,137,14,1)",
			dark:  "rgba(81,137,14,1)",
		},
		{
			name:  "a stated dark primary wins the dark chain",
			org:   portalOrg{ColorPrimary: "rgba(81,137,14,1)", ColorDarkPrimary: "#ffffff"},
			light: "rgba(81,137,14,1)",
			dark:  "#ffffff",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := portalPrimary(&test.org, false); got != test.light {
				t.Errorf("light primary = %q, want %q", got, test.light)
			}
			if got := portalPrimary(&test.org, true); got != test.dark {
				t.Errorf("dark primary = %q, want %q", got, test.dark)
			}
		})
	}
}

// Derived, never stored, and emitted with the spaces sa_cloud emits — the first
// thing anyone does when a colour looks wrong is diff this line against production.
func TestPortalAccentIsThePrimaryAtTenPercent(t *testing.T) {
	org := portalOrg{Color2: "rgba(81,137,14,1)"}

	if got, want := portalAccent(&org, false), "rgba(81, 137, 14, 0.1)"; got != want {
		t.Errorf("accent = %q, want %q", got, want)
	}
	if got, want := portalAccent(&portalOrg{}, false), portalNeutralAccent; got != want {
		t.Errorf("neutral accent = %q, want %q", got, want)
	}
}

func TestPortalContrast(t *testing.T) {
	tests := []struct {
		name       string
		value      string
		background portalColor
		want       float64
	}{
		// The alpha is composited onto the background before the ratio is taken: a
		// partner's tint reads as nearly the background rather than as the hue it
		// names. This one is the invisible sign-in button.
		{"a tint is measured composited, not as its hue", "rgba(126,213,21,0.1)", portalLightBackground, 1.07},
		{"the threshold is inclusive: 4.26 passes", "rgba(81,137,14,1)", portalLightBackground, 4.26},
		{"3.25 fails on white", "rgba(96,158,66,1)", portalLightBackground, 3.25},
		{"and the same hue passes on #1a1a1a", "rgba(96,158,66,1)", portalDarkBackground, 5.36},
		// The other direction: comfortable on white, nowhere near it on dark.
		{"a dark brand passes on white", "rgba(47,43,115,1)", portalLightBackground, 12.25},
		{"and fails on #1a1a1a", "rgba(47,43,115,1)", portalDarkBackground, 1.42},
		{"hex parses", "#475569", portalLightBackground, 7.58},
		{"an unparseable value can never be legible", "cornflowerblue", portalLightBackground, 1.0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := portalContrast(test.value, test.background)
			if math.Abs(got-test.want) > 0.01 {
				t.Errorf("contrast(%q) = %.2f, want %.2f", test.value, got, test.want)
			}
		})
	}
}

// Real rows hold rgba(150,216,255,255). An alpha above 1 is not a partner's
// intent, it is a unit mistake, and CSS itself clamps rather than rejecting it —
// so it has to measure as the opaque colour and not as a near-invisible one.
func TestPortalAlphaAboveOneIsClamped(t *testing.T) {
	mistake := portalContrast("rgba(150,216,255,255)", portalLightBackground)
	opaque := portalContrast("rgba(150,216,255,1)", portalLightBackground)

	if math.Abs(mistake-opaque) > 0.001 {
		t.Errorf("contrast with alpha 255 = %.2f, want the opaque %.2f", mistake, opaque)
	}
}

// Existence, not legibility, and never the logo. A partner whose colours all
// resolve to the neutral is still a partner; an organization with none at all is
// indistinguishable from a mistyped id.
func TestPortalBranded(t *testing.T) {
	branded := map[string]portalOrg{
		"a stated primary":        {ColorPrimary: "#ff8800"},
		"a stated dark primary":   {ColorDarkPrimary: "#ff8800"},
		"gradient stops":          {Color1: "rgba(218,235,245,1)", Color2: "rgba(55,169,232,1)"},
		"an illegible stop alone": {Color2: "rgba(0,0,0,0)"},
		"the pale stop alone":     {Color1: "rgba(1,2,3,1)"}, // never used, still deliberate
		"dark stops only":         {ColorDark2: "#123456"},
	}
	for name, org := range branded {
		if !org.branded() {
			t.Errorf("%s should count as branded", name)
		}
	}

	unbranded := map[string]portalOrg{
		"nothing at all":       {},
		"a logo but no colour": {Logo: "a7e24379.svg"},
	}
	for name, org := range unbranded {
		if org.branded() {
			t.Errorf("%s should not count as branded", name)
		}
	}
}

// color1 is read to decide whether an organization is branded and for nothing
// else. It is the unsaturated stop, and using it as a brand colour is the defect
// that caused sa_cloud's colour rework.
func TestPortalColor1IsNeverAPrimary(t *testing.T) {
	org := portalOrg{Color1: "rgba(126,213,21,1)", ColorDark1: "rgba(126,213,21,1)"}

	if got := portalPrimary(&org, false); got != portalNeutralPrimary {
		t.Errorf("light primary = %q, want the neutral — color1 is not a candidate", got)
	}
	if got := portalPrimary(&org, true); got != portalNeutralPrimary {
		t.Errorf("dark primary = %q, want the neutral — color_dark1 is not a candidate", got)
	}
}

func TestPortalSubstitution(t *testing.T) {
	org := &portalOrg{ID: 42, Name: `Ben & Jerry's <b>`, Color2: "rgba(81,137,14,1)"}

	page := substitutePortalTokens(
		`<title>{{ org_name }}</title><sa-sign-in organization-id="{{org_id}}">`+
			`:root { --sa-primary: {{  org_primary  }} } {{ org_nmae }}`, org)

	want := `<title>Ben &amp; Jerry&#39;s &lt;b&gt;</title><sa-sign-in organization-id="42">` +
		`:root { --sa-primary: rgba(81,137,14,1) } {{ org_nmae }}`

	if page != want {
		t.Errorf("substitute:\n got %s\nwant %s", page, want)
	}
}

// The colours land inside a <style> block, where a stray } ends the rule and
// everything after it is attacker-chosen CSS.
func TestEscapeCSSValue(t *testing.T) {
	got := escapeCSSValue(`rgba(1,2,3,1) } body { display: none`)

	if want := `rgba(1,2,3,1)  body  display none`; got != want {
		t.Errorf("escapeCSSValue = %q, want %q", got, want)
	}
}

func TestNormalizePortalPath(t *testing.T) {
	valid := map[string]string{
		"/":                        portalIndex,
		"/sign_in":                 "sign_in.html",
		"/assets/style.css":        "assets/style.css",
		"/assets/logo.svg":         portalLogoPath,
		"/_v/abc123/assets/one.js": "assets/one.js",
		"/_v/abc123":               portalIndex,
	}
	for path, want := range valid {
		if got, err := normalizePortalPath(path); err != nil || got != want {
			t.Errorf("normalizePortalPath(%q) = %q, %v — want %q", path, got, err, want)
		}
	}

	// `..` and `.` arrive as ordinary segments, so neither can be left to the
	// filesystem to refuse.
	for _, path := range []string{"/../README.md", "/./x", "/sign_in/", "/a//b", `/..\README.md`} {
		if _, err := normalizePortalPath(path); err == nil {
			t.Errorf("normalizePortalPath(%q) was accepted", path)
		}
	}
}

func TestPortalDeclaredRoot(t *testing.T) {
	tests := []struct {
		body    string
		want    string
		refused bool
	}{
		{body: `{"root": "dist"}`, want: "dist"},
		{body: `{"root": "/dist"}`, want: "dist"}, // a leading slash is fine
		{body: `{"root": "."}`, want: ""},         // the repository root, said explicitly
		{body: `{"root": "  build/site  "}`, want: "build/site"},
		{body: `{"root": ".."}`, refused: true},
		{body: `{"root": "dist/../.."}`, refused: true},
		{body: `{"root": 7}`, refused: true},
		{body: `{"branch": "main", "root": "dist"}`, refused: true}, // never a branch
		{body: `{}`, refused: true},
		{body: `not json`, refused: true},
	}

	for _, test := range tests {
		path := filepath.Join(t.TempDir(), portalConfigFile)
		if err := os.WriteFile(path, []byte(test.body), 0600); err != nil {
			t.Fatal(err)
		}

		got, err := portalDeclaredRoot(path)
		if test.refused {
			if err == nil {
				t.Errorf("portalDeclaredRoot(%s) = %q, want refused", test.body, got)
			}
			continue
		}
		if err != nil || got != test.want {
			t.Errorf("portalDeclaredRoot(%s) = %q, %v — want %q", test.body, got, err, test.want)
		}
	}
}

// The layout is read from the workspace's own metadata, so a package appearing
// or `src` moving costs nothing and no directory layout is baked in here.
func TestResolveComponentsWorkspace(t *testing.T) {
	root := t.TempDir()
	write := func(path, body string) {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}

	write("package.json", `{"workspaces": ["packages/*"]}`)
	write("packages/api/package.json", `{"name": "@solar-assistant/api", "main": "src/index.js"}`)
	write("packages/components/package.json", `{"name": "`+portalComponentsPackage+`", "main": "lib/main.js"}`)
	write("packages/tools/package.json", `{"name": "@solar-assistant/tools"}`) // no main

	kit, err := resolveComponentsWorkspace(root)
	if err != nil {
		t.Fatal(err)
	}

	if want := portalKitPrefix + "packages/components/lib/main.js"; kit.entry != want {
		t.Errorf("entry = %q, want %q — main is read, not assumed", kit.entry, want)
	}
	if want := portalKitPrefix + "packages/api/src/index.js"; kit.imports["@solar-assistant/api"] != want {
		t.Errorf("api import = %q, want %q", kit.imports["@solar-assistant/api"], want)
	}
	if want := portalKitPrefix + "packages/tools/index.js"; kit.imports["@solar-assistant/tools"] != want {
		t.Errorf("a package with no main = %q, want the default %q", kit.imports["@solar-assistant/tools"], want)
	}

	// Without the entry package there is nothing to put in the script tag, and
	// serving the wrong package would fail in the browser rather than here.
	write("packages/components/package.json", `{"name": "@acme/widgets", "main": "src/index.js"}`)
	if _, err := resolveComponentsWorkspace(root); err == nil {
		t.Error("a workspace with no components package should be refused")
	}
}

func TestResolveComponentsValueShapes(t *testing.T) {
	kit, err := resolveComponents("https://staging.example.com/sa.js")
	if err != nil || kit.entry != "https://staging.example.com/sa.js" || kit.root != "" {
		t.Errorf("a URL should be used as the src and serve nothing: %+v, %v", kit, err)
	}

	bundle := filepath.Join(t.TempDir(), "bundle.js")
	if err := os.WriteFile(bundle, []byte("//"), 0600); err != nil {
		t.Fatal(err)
	}
	kit, err = resolveComponents(bundle)
	if err != nil {
		t.Fatal(err)
	}
	// A built bundle resolves its own imports, so an import map would be noise.
	if kit.entry != portalKitPrefix+"bundle.js" || len(kit.imports) != 0 {
		t.Errorf("a file should be served directly with no import map: %+v", kit)
	}

	if _, err := resolveComponents(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a path that does not exist should be refused")
	}
}

func TestComponentsRewrite(t *testing.T) {
	kit := &portalComponents{
		entry:   portalKitPrefix + "packages/components/src/index.js",
		imports: map[string]string{"@solar-assistant/api": portalKitPrefix + "packages/api/src/index.js"},
	}

	page, replaced := kit.rewrite(
		`<script type="module" src="https://cdn.solar-assistant.io/js/solar-assistant.js"></script>`)
	if !replaced {
		t.Fatal("the bundle tag was not replaced")
	}
	if !strings.Contains(page, `<script type="importmap">`) ||
		!strings.Contains(page, `"@solar-assistant/api":"`+portalKitPrefix+`packages/api/src/index.js"`) {
		t.Errorf("import map missing or wrong: %s", page)
	}
	// It must precede the module script, or the browser resolves the bare
	// specifier before the map exists.
	if strings.Index(page, "importmap") > strings.Index(page, `type="module"`) {
		t.Errorf("import map must come first: %s", page)
	}

	// A page with no bundle tag reports rather than silently changing nothing.
	if _, replaced := kit.rewrite(`<body></body>`); replaced {
		t.Error("a page with no bundle tag should report no replacement")
	}
}

func TestWithinDirectory(t *testing.T) {
	if !withinDirectory("/kit", "/kit/packages/api/src/index.js") {
		t.Error("a file inside the checkout should be allowed")
	}
	for _, file := range []string{"/kit/../etc/passwd", "/etc/passwd", "/kitten/x.js"} {
		if withinDirectory("/kit", filepath.Clean(file)) {
			t.Errorf("%s should be refused", file)
		}
	}
}

func TestInjectReloadScript(t *testing.T) {
	if got := injectReloadScript("<body>x</body>"); got == "<body>x</body>" {
		t.Error("reload script was not injected")
	}
	// A page with no </body> still has to reload, or an edit to it looks ignored.
	if got := injectReloadScript("<p>x"); got == "<p>x" {
		t.Error("reload script was not appended to a page with no closing body")
	}
}
