package main

// Serves a portal template checkout the way sa_cloud serves it in production, so
// somebody working on their portal sees a change without committing, pushing and
// staging first. Everything here mirrors how solar-assistant.io serves a portal; a
// preview that behaves differently is worse than no preview, because it invites
// people to build against behaviour production does not have.
//
// The API is not served or proxied here. The pages call solar-assistant.io from
// the browser, which allows any origin, so sign-in works from localhost as it is.

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"math"
	"mime"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Solar-Assistant/go_solar_assistant/cloud"
)

const (
	portalDefaultPort = 8912
	portalConfigFile  = "solar-assistant.json"
	portalIndex       = "index.html"
	portalLogoPath    = "assets/logo.svg"
	portalReloadPath  = "/_sacli/reload"
	portalPollEvery   = 400 * time.Millisecond

	// Everything this command adds to a portal lives under one reserved prefix,
	// so "nothing outside the web root is reachable" stays a statable rule with a
	// single exception rather than a growing list.
	portalKitPrefix = "/_sacli/kit/"

	// The package whose main is the script the pages load. The one piece of
	// layout this cannot discover, so --help says it.
	portalComponentsPackage = "@solar-assistant/components"
)

// The public, unauthenticated redirect to an organization's logo. Redirecting to
// it rather than to the object keeps the bucket, the region and its dev/prod
// difference sa_cloud's problem rather than a second copy of them here.
const portalLogoEndpoint = "/api/v1/organizations/%d/logo"

// Where an organization's brand colours are set.
const portalBrandingPage = "%s/organizations/%d/brand"

func runPortal(args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "-h") {
		printPortalUsage()
		return nil
	}

	orgFlags, args := extractStringFlag(args, "--org")
	portFlags, args := extractStringFlag(args, "--port")
	componentFlags, args := extractStringFlag(args, "--components")

	dir := "."
	if len(args) > 1 {
		return fmt.Errorf("too many arguments — usage: sacli portal [directory]")
	}
	if len(args) == 1 {
		dir = args[0]
	}

	port := portalDefaultPort
	if len(portFlags) > 0 {
		n, err := strconv.Atoi(portFlags[len(portFlags)-1])
		if err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("invalid --port %q", portFlags[len(portFlags)-1])
		}
		port = n
	}

	orgID, err := portalOrgID(orgFlags)
	if err != nil {
		return err
	}

	org, err := fetchOrganization(orgID)
	if err != nil {
		return err
	}
	if !org.branded() {
		// The link doubles as the access check this command does not make: an
		// organization you are not a member of refuses you there, which is the
		// answer somebody who mistyped an id actually needs.
		return fmt.Errorf("branding not configured on organization %d (%s), please configure here:\n%s",
			org.ID, org.Name, fmt.Sprintf(portalBrandingPage, cloud.DefaultBaseURL, org.ID))
	}

	// Only once it is known to be real: a typo that got stored would go on
	// failing every later run, long after the flag that caused it was dropped.
	if err := rememberPortalOrgID(orgID); err != nil {
		return err
	}

	root, err := portalRoot(dir)
	if err != nil {
		return err
	}

	var components *portalComponents
	if len(componentFlags) > 0 {
		if components, err = resolveComponents(componentFlags[len(componentFlags)-1]); err != nil {
			return err
		}
	}

	server := &portalServer{root: root, org: org, components: components, hub: newReloadHub()}
	return server.listen(port)
}

func printPortalUsage() {
	fmt.Println(`Usage: sacli portal [directory] [--org <id>] [--port <n>] [--components <where>]

For solar businesses that build their own custom cloud portal: edit the template
you are working on and see the result straight away, instead of committing,
pushing and going to look at staging-<your-org>.solar-power.live.

It opens a web server on 127.0.0.1 and serves the checkout the way
SolarAssistant serves it in production — clean URLs, your organization's name,
colours and logo substituted into each page, and the browser reloaded whenever
a file changes.

The directory is the repository root — the one holding solar-assistant.json —
and defaults to the current one. Only the directory that file's "root" names is
served, so nothing outside it can be reached, exactly as in production.

Starting from the minimal template, with your own organization id:

  git clone https://github.com/Solar-Assistant/portal-minimal.git
  cd portal-minimal
  sacli portal --org 42

The id is remembered, so later runs in that repository are just "sacli portal".

The web components a portal page is built from live at
https://github.com/Solar-Assistant/js_solar_assistant. If you are working on
those too, --components points the pages at your own copy of them instead of
the published bundle:

  git clone https://github.com/Solar-Assistant/js_solar_assistant.git
  cd portal-minimal
  sacli portal --components ../js_solar_assistant

A checkout is served from source with no build step. --components also takes a
built .js file, or a URL for one hosted elsewhere.

--port moves the server off the default ` + fmt.Sprint(portalDefaultPort) + `.`)
}

// The id is the one thing a checkout cannot tell us, and the loop it belongs to
// restarts often, so it is remembered rather than repeated on every run.
func portalOrgID(flags []string) (int, error) {
	cfg, err := loadConfig()
	if err != nil {
		return 0, err
	}

	if len(flags) == 0 {
		if cfg.PortalOrgID == 0 {
			return 0, fmt.Errorf("no organization id — run: sacli portal --org <id>")
		}
		return cfg.PortalOrgID, nil
	}

	id, err := strconv.Atoi(flags[len(flags)-1])
	if err != nil || id < 1 {
		return 0, fmt.Errorf("invalid --org %q", flags[len(flags)-1])
	}
	return id, nil
}

func rememberPortalOrgID(id int) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.PortalOrgID == id {
		return nil
	}
	cfg.PortalOrgID = id
	return saveConfig(cfg)
}

// ── the organization ──────────────────────────────────────────────────────────

// Unauthenticated and open to any origin, so no API key is involved.
type portalOrg struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Logo string `json:"logo"`
	// Read to decide whether this organization is branded at all, and for
	// nothing else. color1 is the *unsaturated* gradient stop and is never a
	// candidate for a brand colour — see the colour resolution below.
	Color1           string `json:"color1"`
	ColorDark1       string `json:"color_dark1"`
	Color2           string `json:"color2"`
	ColorDark2       string `json:"color_dark2"`
	ColorPrimary     string `json:"color_primary"`
	ColorDarkPrimary string `json:"color_dark_primary"`
}

// Whether any brand colour has been set. An organization with none renders every
// page in the neutral palette, which is indistinguishable from a mistyped id —
// the one mistake here that costs an afternoon before anybody notices. Refusing
// sends the person to the branding page, which refuses them in turn if the
// organization is not theirs.
//
// Existence, not legibility. A stop too pale to use still means somebody set it
// deliberately, and plenty of live portals have one that resolves to the neutral.
// Those are partners, not typos.
//
// cloud_host is deliberately not part of this. It is set when a portal goes live,
// so a partner building one has none — and they are who this command is for.
func (org *portalOrg) branded() bool {
	return org.ColorPrimary != "" || org.ColorDarkPrimary != "" ||
		org.Color1 != "" || org.Color2 != "" ||
		org.ColorDark1 != "" || org.ColorDark2 != ""
}

func fetchOrganization(id int) (*portalOrg, error) {
	url := fmt.Sprintf("%s/api/v1/organizations/%d", cloud.DefaultBaseURL, id)
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("cannot reach %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("no organization %d", id)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cannot read organization %d: HTTP %d", id, resp.StatusCode)
	}

	var org portalOrg
	if err := json.NewDecoder(resp.Body).Decode(&org); err != nil {
		return nil, fmt.Errorf("cannot parse organization %d: %w", id, err)
	}
	return &org, nil
}

// ── the components kit ────────────────────────────────────────────────────────

// Where a page's web components are loaded from.
//
// A portal page references the published bundle at an absolute CDN URL, which is
// built by something outside the kit repository — so a component cannot be seen
// running at all without swapping that tag by hand. Pointing at a checkout runs
// the working tree instead.
//
// No build step is involved: the kit's source is plain ES modules and the only
// bare specifier in it is the api package, so an import map resolves everything
// the browser needs.
type portalComponents struct {
	// The script tag's new src.
	entry string
	// Bare specifier to URL, empty when the value was a single file or a URL.
	imports map[string]string
	// The checkout served under the kit prefix, empty when it is a URL.
	root string
	// What to print at startup, and what a directory resolved to.
	source string
}

func resolveComponents(value string) (*portalComponents, error) {
	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return &portalComponents{entry: value, source: value}, nil
	}

	path, err := filepath.Abs(value)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("no components at %s", value)
	}

	// A built bundle somebody handed you: served as it is, nothing to resolve.
	if !info.IsDir() {
		return &portalComponents{
			entry:  portalKitPrefix + filepath.Base(path),
			root:   filepath.Dir(path),
			source: path,
		}, nil
	}
	return resolveComponentsWorkspace(path)
}

// Builds the import map from the repository's own metadata — workspaces, then
// each package's name and main. Discovered rather than assumed, so a package
// appearing or `src` moving costs nothing, and no directory layout is baked into
// a binary that ships separately from the repository it points at.
func resolveComponentsWorkspace(root string) (*portalComponents, error) {
	directories, err := workspaceDirectories(root)
	if err != nil {
		return nil, err
	}

	kit := &portalComponents{imports: map[string]string{}, root: root, source: root}

	for _, directory := range directories {
		name, main, err := packageEntry(filepath.Join(directory, "package.json"))
		if err != nil || name == "" {
			continue
		}
		relative, err := filepath.Rel(root, filepath.Join(directory, main))
		if err != nil {
			continue
		}
		url := portalKitPrefix + filepath.ToSlash(relative)

		kit.imports[name] = url
		if name == portalComponentsPackage {
			kit.entry = url
		}
	}

	if kit.entry == "" {
		return nil, fmt.Errorf("no package named %s under %s — that is the one this serves as the\n"+
			"page's script; a fork that renames it has to be pointed at its built file instead",
			portalComponentsPackage, root)
	}
	return kit, nil
}

func workspaceDirectories(root string) ([]string, error) {
	body, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return nil, fmt.Errorf("no package.json in %s, so there is no workspace to read", root)
	}

	var manifest struct {
		// Both spellings are in the wild; neither costs anything to accept.
		Workspaces json.RawMessage `json:"workspaces"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return nil, fmt.Errorf("cannot parse %s/package.json: %w", root, err)
	}

	var patterns []string
	if err := json.Unmarshal(manifest.Workspaces, &patterns); err != nil {
		var object struct {
			Packages []string `json:"packages"`
		}
		if err := json.Unmarshal(manifest.Workspaces, &object); err != nil {
			return nil, fmt.Errorf("%s/package.json declares no workspaces", root)
		}
		patterns = object.Packages
	}

	var directories []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(pattern)))
		if err != nil {
			continue
		}
		directories = append(directories, matches...)
	}
	if len(directories) == 0 {
		return nil, fmt.Errorf("%s/package.json declares workspaces, but none of them match anything", root)
	}
	return directories, nil
}

func packageEntry(manifestPath string) (name string, main string, err error) {
	body, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", err
	}

	var manifest struct {
		Name string `json:"name"`
		Main string `json:"main"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return "", "", err
	}
	if manifest.Main == "" {
		manifest.Main = "index.js"
	}
	return manifest.Name, manifest.Main, nil
}

// The published bundle, however a page spells it. Matching on the filename rather
// than the full URL so a page pointing at a staging CDN is still redirected.
var portalBundleTag = regexp.MustCompile(`(?i)<script\b[^>]*\bsrc="[^"]*solar-assistant\.js"[^>]*>\s*</script>`)

// Replaces the page's bundle tag with the import map and the checkout's entry.
//
// The import map has to precede the first module script that loads, which is why
// it replaces the tag rather than being prepended to the document: the tag it
// replaces is that script.
func (kit *portalComponents) rewrite(page string) (string, bool) {
	replaced := false

	rewritten := portalBundleTag.ReplaceAllStringFunc(page, func(string) string {
		replaced = true
		return kit.scriptTags()
	})
	return rewritten, replaced
}

func (kit *portalComponents) scriptTags() string {
	var tags strings.Builder

	if len(kit.imports) > 0 {
		imports, _ := json.Marshal(map[string]any{"imports": kit.imports})
		fmt.Fprintf(&tags, `<script type="importmap">%s</script>`+"\n  ", imports)
	}
	fmt.Fprintf(&tags, `<script type="module" src="%s"></script>`, kit.entry)

	return tags.String()
}

// ── the web root ──────────────────────────────────────────────────────────────

// Resolves solar-assistant.json's "root" against the checkout.
//
// Production falls back to the source root on any error, so that a typo cannot
// take a live portal down. Here the person who can fix the typo is standing in
// front of it, so the fallback comes with a warning, and a root naming a
// directory that does not exist stops rather than serving 404s that read as a
// broken template.
func portalRoot(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", dir)
	}

	declared, err := portalDeclaredRoot(filepath.Join(abs, portalConfigFile))
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v — serving %s itself, which is what production would do\n", err, dir)
		return abs, nil
	}
	if declared == "" {
		return abs, nil
	}

	root := filepath.Join(abs, filepath.FromSlash(declared))
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		return "", fmt.Errorf("solar-assistant.json names %q, which is not a directory here — "+
			"a framework that writes it is usually in .gitignore, so it never got committed", declared)
	}
	return root, nil
}

func portalDeclaredRoot(configPath string) (string, error) {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return "", fmt.Errorf("no %s here, and every portal repository must have one", portalConfigFile)
	}

	var config map[string]any
	if err := json.Unmarshal(body, &config); err != nil {
		return "", fmt.Errorf("%s is not valid JSON", portalConfigFile)
	}
	for key := range config {
		if key != "root" {
			return "", fmt.Errorf("%s has an unknown property %q", portalConfigFile, key)
		}
	}
	value, ok := config["root"]
	if !ok {
		return "", fmt.Errorf("%s has no \"root\"", portalConfigFile)
	}
	root, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s's \"root\" is not a string", portalConfigFile)
	}

	// Partner-controlled input, so the same suspicion as a request path.
	trimmed := strings.Trim(strings.TrimSpace(root), "/")
	if trimmed == "" || trimmed == "." {
		return "", nil
	}
	for _, segment := range strings.Split(trimmed, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("%s's \"root\" %q points outside the repository", portalConfigFile, root)
		}
	}
	return trimmed, nil
}

// ── serving ───────────────────────────────────────────────────────────────────

type portalServer struct {
	root       string
	org        *portalOrg
	components *portalComponents
	hub        *reloadHub
}

func (s *portalServer) listen(port int) error {
	address := fmt.Sprintf("127.0.0.1:%d", port)
	listener, err := newPortalListener(address)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go s.watch(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc(portalReloadPath, s.serveReload)
	mux.HandleFunc(portalKitPrefix, s.serveKit)
	mux.HandleFunc("/", s.serve)

	name := s.org.Name
	if name == "" {
		name = "organization " + strconv.Itoa(s.org.ID)
	}
	fmt.Printf("Serving %s (organization %d) from %s\n", name, s.org.ID, s.root)
	// Printed every run, including the default: a flag that silently changes
	// which code executes is the kind that costs an hour, and the page renders
	// either way — only its behaviour differs.
	fmt.Printf("Components: %s\n", s.componentSource())
	fmt.Printf("  http://%s\n\n", address)
	fmt.Printf("Pages talk to the live API at %s. Signing in, inviting a user or\n", cloud.DefaultBaseURL)
	fmt.Println("removing one all act on real data.")
	fmt.Println("Press Ctrl-C to stop.")

	server := &http.Server{Handler: mux}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()

	if err := server.Serve(listener); err != nil && err != http.ErrServerClosed {
		return err
	}
	fmt.Println()
	return nil
}

func (s *portalServer) serve(w http.ResponseWriter, r *http.Request) {
	requested, err := normalizePortalPath(r.URL.Path)
	if err != nil {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	// The logo is the organization's, never the repository's, so it is answered
	// before the root is involved at all.
	if requested == portalLogoPath {
		if s.org.Logo == "" {
			http.Error(w, "This organization has no logo", http.StatusNotFound)
			return
		}
		logo := cloud.DefaultBaseURL + fmt.Sprintf(portalLogoEndpoint, s.org.ID)
		http.Redirect(w, r, logo, http.StatusFound)
		return
	}

	file := filepath.Join(s.root, filepath.FromSlash(requested))
	body, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	// Nothing may be cached: the whole point is that an edit shows up.
	w.Header().Set("Cache-Control", "no-store")

	if !portalTemplate(requested) {
		if kind := mime.TypeByExtension(path.Ext(requested)); kind != "" {
			w.Header().Set("Content-Type", kind)
		}
		w.Write(body)
		return
	}

	page := substitutePortalTokens(string(body), s.org)
	page = s.substituteComponents(page, requested)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(injectReloadScript(page)))
}

func (s *portalServer) componentSource() string {
	if s.components == nil {
		return "the published bundle each page references"
	}
	return s.components.source
}

// Points the page at the checkout, and says so when a page has no bundle tag to
// replace — otherwise --components looks like it worked and quietly did nothing.
func (s *portalServer) substituteComponents(page, requested string) string {
	if s.components == nil {
		return page
	}

	rewritten, replaced := s.components.rewrite(page)
	if !replaced {
		fmt.Fprintf(os.Stderr, "warning: %s loads no component bundle, so --components changes nothing there\n", requested)
	}
	return rewritten
}

// Serves the components checkout, and only from inside it. The kit is the one
// thing reachable from outside the portal's own web root, so the confinement
// check here is the same suspicion the web root gets.
func (s *portalServer) serveKit(w http.ResponseWriter, r *http.Request) {
	if s.components == nil || s.components.root == "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	file := filepath.Join(s.components.root, filepath.FromSlash(strings.TrimPrefix(r.URL.Path, portalKitPrefix)))
	if !withinDirectory(s.components.root, file) {
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	body, err := os.ReadFile(file)
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	if kind := mime.TypeByExtension(path.Ext(file)); kind != "" {
		w.Header().Set("Content-Type", kind)
	}
	w.Write(body)
}

func withinDirectory(directory, file string) bool {
	relative, err := filepath.Rel(directory, file)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// Maps a request path onto a file: the root is the index, an extensionless path
// is a clean URL for the matching .html, anything else is taken literally, and a
// leading _v/<revision> is stripped. `..` and `.` arrive as ordinary segments,
// so neither can be left to the filesystem to refuse.
func normalizePortalPath(requestPath string) (string, error) {
	var segments []string
	if trimmed := strings.TrimPrefix(requestPath, "/"); trimmed != "" {
		segments = strings.Split(trimmed, "/")
	}
	if len(segments) >= 2 && segments[0] == "_v" {
		segments = segments[2:]
	}
	for _, segment := range segments {
		if segment == "" || segment == "." || segment == ".." ||
			strings.ContainsAny(segment, "\\\x00") {
			return "", fmt.Errorf("invalid path")
		}
	}
	if len(segments) == 0 {
		return portalIndex, nil
	}
	joined := strings.Join(segments, "/")
	if path.Ext(joined) == "" {
		joined += ".html"
	}
	return joined, nil
}

func portalTemplate(requested string) bool {
	ext := path.Ext(requested)
	return ext == ".html" || ext == ".htm"
}

// ── substitution ──────────────────────────────────────────────────────────────

var (
	portalToken    = regexp.MustCompile(`\{\{\s*([a-zA-Z0-9_]+)\s*\}\}`)
	portalCSSValue = regexp.MustCompile(`[^A-Za-z0-9#(),.%\s-]`)
)

// Replaces every known token. An unknown one is left exactly as written, so a
// partner sees their own typo rather than a blank — and, unlike in production,
// the warning lands in front of the person who can fix it.
func substitutePortalTokens(page string, org *portalOrg) string {
	values := portalValues(org)

	return portalToken.ReplaceAllStringFunc(page, func(token string) string {
		name := portalToken.FindStringSubmatch(token)[1]
		if value, ok := values[name]; ok {
			return value
		}
		fmt.Fprintf(os.Stderr, "warning: unknown token %s\n", token)
		return token
	})
}

func portalValues(org *portalOrg) map[string]string {
	return map[string]string{
		"org_id":           strconv.Itoa(org.ID),
		"org_name":         escapeHTMLValue(org.Name),
		"org_primary":      escapeCSSValue(portalPrimary(org, false)),
		"org_accent":       escapeCSSValue(portalAccent(org, false)),
		"org_primary_dark": escapeCSSValue(portalPrimary(org, true)),
		"org_accent_dark":  escapeCSSValue(portalAccent(org, true)),
	}
}

// Values are escaped for where they land: org_name goes into <title> and alt="",
// the colours go inside <style>, where a stray } ends the rule and everything
// after it is attacker-chosen CSS. (Production writes " as &quot; where Go
// writes &#34;; the two are the same character to a browser.)
func escapeHTMLValue(value string) string { return html.EscapeString(value) }

func escapeCSSValue(value string) string { return portalCSSValue.ReplaceAllString(value, "") }

// ── colours ───────────────────────────────────────────────────────────────────

// color1/color2 are the two stops of the header gradient and nothing else, so
// neither can simply be used as a brand colour: most are mid-tones that vanish
// on white, and one of them fed straight to --sa-primary is how a partner's
// sign-in button ended up invisible. A stated colour wins; the saturated stop is
// borrowed only while it stays readable; otherwise the neutral.
//
// This mirrors what solar-assistant.io does when it serves a portal. If that
// changes, this has to change with it — a preview showing colours production
// would not serve is the one failure worth more than having no preview at all.
const (
	portalNeutralPrimary = "#475569"
	portalNeutralAccent  = "rgba(71, 85, 105, 0.1)"
	portalLegible        = 4.0
)

var (
	portalLightBackground = portalColor{255, 255, 255, 1}
	portalDarkBackground  = portalColor{26, 26, 26, 1}
)

func portalPrimary(org *portalOrg, dark bool) string {
	candidates := []string{org.ColorPrimary, org.Color2}
	background := portalLightBackground
	if dark {
		// A dark theme borrows the light primary before it gives up on the hue.
		candidates = []string{org.ColorDarkPrimary, org.ColorPrimary, org.ColorDark2, org.Color2}
		background = portalDarkBackground
	}

	for _, candidate := range candidates {
		if candidate != "" && portalContrast(candidate, background) >= portalLegible {
			return candidate
		}
	}
	return portalNeutralPrimary
}

// The wash behind the primary, so the two always belong to one hue.
func portalAccent(org *portalOrg, dark bool) string {
	color, ok := parsePortalColor(portalPrimary(org, dark))
	if !ok {
		return portalNeutralAccent
	}
	return fmt.Sprintf("rgba(%d, %d, %d, 0.1)", color.r, color.g, color.b)
}

// Branding is stored as free text, so this has to cope with whatever a partner
// typed years ago: #rrggbb, rgb(), rgba(), and alphas outside 0..1.
type portalColor struct {
	r, g, b int
	a       float64
}

var (
	portalRGBPattern = regexp.MustCompile(`^rgba?\(\s*(\d+)\s*,\s*(\d+)\s*,\s*(\d+)\s*(?:,\s*([\d.]+)\s*)?\)$`)
	portalHexPattern = regexp.MustCompile(`^#([0-9a-fA-F]{6})$`)
)

func parsePortalColor(value string) (portalColor, bool) {
	value = strings.TrimSpace(value)

	if match := portalRGBPattern.FindStringSubmatch(value); match != nil {
		channels := [3]int{}
		for i := 0; i < 3; i++ {
			channel, err := strconv.Atoi(match[i+1])
			if err != nil || channel < 0 || channel > 255 {
				return portalColor{}, false
			}
			channels[i] = channel
		}
		alpha := 1.0
		if match[4] != "" {
			parsed, err := strconv.ParseFloat(match[4], 64)
			if err != nil {
				return portalColor{}, false
			}
			// An alpha above 1 is not a partner's intent, it is a unit mistake:
			// some rows hold rgba(150,216,255,255), which CSS itself clamps.
			alpha = min(parsed, 1.0)
		}
		return portalColor{channels[0], channels[1], channels[2], alpha}, true
	}

	if match := portalHexPattern.FindStringSubmatch(value); match != nil {
		digits, err := strconv.ParseInt(match[1], 16, 64)
		if err != nil {
			return portalColor{}, false
		}
		return portalColor{int(digits >> 16 & 0xff), int(digits >> 8 & 0xff), int(digits & 0xff), 1}, true
	}

	return portalColor{}, false
}

// The WCAG contrast ratio, 1.0 to 21.0. The colour is composited onto the
// background first: a partner's tint is routinely rgba(...,0.1), which reads as
// nearly the background itself rather than as the hue it names.
func portalContrast(value string, background portalColor) float64 {
	color, ok := parsePortalColor(value)
	if !ok {
		return 1.0
	}

	over := portalLuminance(portalComposite(color, background))
	under := portalLuminance(portalComposite(background, background))

	return (max(over, under) + 0.05) / (min(over, under) + 0.05)
}

func portalComposite(color, background portalColor) [3]float64 {
	return [3]float64{
		float64(color.r)*color.a + float64(background.r)*(1-color.a),
		float64(color.g)*color.a + float64(background.g)*(1-color.a),
		float64(color.b)*color.a + float64(background.b)*(1-color.a),
	}
}

func portalLuminance(channels [3]float64) float64 {
	return 0.2126*portalChannelLuminance(channels[0]) +
		0.7152*portalChannelLuminance(channels[1]) +
		0.0722*portalChannelLuminance(channels[2])
}

func portalChannelLuminance(value float64) float64 {
	value /= 255
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

// ── live reload ───────────────────────────────────────────────────────────────

// The loop this command exists for is *prompt the agent, look at the browser*,
// so a manual refresh per iteration is most of the friction it removes. The page
// holds an event stream open and reloads when anything under the root changes.
//
// Polling rather than watching the filesystem: a portal is a handful of files,
// and an agent rewriting one is not a stream of events worth a dependency.

const portalReloadScript = `
<script>
  // Added by "sacli portal" for local preview only. It is not in your files and
  // SolarAssistant never serves it.
  new EventSource("` + portalReloadPath + `").onmessage = function () { location.reload() }
</script>
`

func injectReloadScript(page string) string {
	if closing := strings.LastIndex(strings.ToLower(page), "</body>"); closing != -1 {
		return page[:closing] + portalReloadScript + page[closing:]
	}
	return page + portalReloadScript
}

type reloadHub struct {
	mu      sync.Mutex
	clients map[chan struct{}]struct{}
}

func newReloadHub() *reloadHub {
	return &reloadHub{clients: map[chan struct{}]struct{}{}}
}

func (h *reloadHub) subscribe() chan struct{} {
	client := make(chan struct{}, 1)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.clients[client] = struct{}{}
	return client
}

func (h *reloadHub) unsubscribe(client chan struct{}) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.clients, client)
}

func (h *reloadHub) broadcast() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for client := range h.clients {
		// A page already told to reload does not need telling twice.
		select {
		case client <- struct{}{}:
		default:
		}
	}
}

func (s *portalServer) serveReload(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	client := s.hub.subscribe()
	defer s.hub.unsubscribe(client)

	for {
		select {
		case <-r.Context().Done():
			return
		case <-client:
			fmt.Fprint(w, "data: reload\n\n")
			flusher.Flush()
		}
	}
}

func (s *portalServer) watch(ctx context.Context) {
	previous := s.fingerprint()
	ticker := time.NewTicker(portalPollEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if current := s.fingerprint(); current != previous {
				previous = current
				s.hub.broadcast()
			}
		}
	}
}

// Names, sizes and modification times of everything served, in the walk's own
// order, so a change anywhere under the root — including a deletion — shows up
// as a different string.
func (s *portalServer) fingerprint() string {
	var state strings.Builder

	// The components checkout is watched too when one is served: editing a
	// component and having to reload by hand is the friction this whole command
	// exists to remove, and it does not stop at the portal's own files.
	directories := []string{s.root}
	if s.components != nil && s.components.root != "" {
		directories = append(directories, s.components.root)
	}

	for _, directory := range directories {
		filepath.WalkDir(directory, func(file string, entry fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if entry.Name() == ".git" || entry.Name() == "node_modules" {
					return fs.SkipDir
				}
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			fmt.Fprintf(&state, "%s %d %d\n", file, info.Size(), info.ModTime().UnixNano())
			return nil
		})
	}

	return state.String()
}

func newPortalListener(address string) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// The URL has to stay put across restarts for the reload loop to be
		// worth anything, so a taken port is reported rather than worked around.
		return nil, fmt.Errorf("cannot listen on %s (something else is using it — "+
			"stop it, or pass --port): %w", address, err)
	}
	return listener, nil
}
