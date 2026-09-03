package realorigin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestIDIsStableAndWellFormed(t *testing.T) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567"
	for _, target := range []string{
		"", "skycoin.com", "explorer.skycoin.com",
		"magnetosphere.net.abcdefghijklmnopqrstuvwxyz234567abcdefghijklmno.dmsg",
		strings.Repeat("x", 4096),
	} {
		id := ID(target)
		if len(id) != IDLen {
			t.Errorf("ID(%q) is %d characters, want %d", target, len(id), IDLen)
		}
		if id != ID(target) {
			t.Errorf("ID(%q) is not stable across calls", target)
		}
		for _, r := range id {
			if !strings.ContainsRune(alphabet, r) {
				t.Errorf("ID(%q) = %q contains %q, which is not a DNS-safe base32 character", target, id, r)
			}
		}
	}
}

// Different targets must not collide, or two sites share a cookie jar.
func TestDistinctTargetsGetDistinctIDs(t *testing.T) {
	seen := map[string]string{}
	for _, target := range []string{
		"skycoin.com", "www.skycoin.com", "explorer.skycoin.com",
		"skycoin.com/", "SKYCOIN.COM", "magnetosphere.net",
	} {
		id := ID(target)
		if prev, dup := seen[id]; dup {
			t.Fatalf("%q and %q both hash to %q", prev, target, id)
		}
		seen[id] = target
	}
}

func TestHostAndIDFromHost(t *testing.T) {
	const suffix = ".mesh.localhost"
	host := Host("skycoin.com", suffix)
	if want := ID("skycoin.com") + suffix; host != want {
		t.Errorf("Host = %q, want %q", host, want)
	}
	id, ok := IDFromHost(host, suffix)
	if !ok || id != ID("skycoin.com") {
		t.Errorf("IDFromHost(%q) = %q, %v; want the original id back", host, id, ok)
	}
}

// A wildcard certificate spans exactly one label, so a multi-label host under
// the suffix could never have been served over HTTPS and is not one of ours.
// Accepting it would let a name we never minted claim a browse origin.
func TestIDFromHostRejectsWhatAWildcardCannotCover(t *testing.T) {
	const suffix = ".mesh.localhost"
	for _, host := range []string{
		"deep.nested.mesh.localhost", // two labels
		"mesh.localhost",             // no label at all
		"evil.example.com",           // wrong suffix
		"notmesh.localhost",          // suffix must start at a dot boundary
	} {
		if id, ok := IDFromHost(host, suffix); ok {
			t.Errorf("IDFromHost(%q) accepted it as %q, but a single-level wildcard cannot cover it", host, id)
		}
	}
}

func TestSuffixLeadingDotIsSupplied(t *testing.T) {
	withDot := Host("a", ".mesh.localhost")
	withoutDot := Host("a", "mesh.localhost")
	if withDot != withoutDot {
		t.Errorf("suffix normalization differs: %q vs %q", withDot, withoutDot)
	}
}

// The identifier is computed on both sides of the bridge — in Go when a caller
// mints a host, and in JavaScript when the app registers a target. If the two
// ever disagree the app resolves an id that no frame will ever present, and
// every navigation fails with "unknown browse origin". This is the one
// invariant worth spending a subprocess on.
func TestGoAndJavaScriptAgreeOnID(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the Go/JS parity of ID() is UNCHECKED here")
	}

	targets := []string{
		"", "skycoin.com", "explorer.skycoin.com", "a/b?c=d",
		"magnetosphere.net.abcdefghijklmnopqrstuvwxyz234567abcdefghijklmno.dmsg",
		"unicode: ✓ ünïcödé", strings.Repeat("x", 1000),
	}
	in, err := json.Marshal(targets)
	if err != nil {
		t.Fatal(err)
	}

	// Reuse the shipped implementation rather than a copy of it: the point is to
	// test what actually runs in the browser.
	src, err := os.ReadFile(filepath.Join("web", "responder.js")) //nolint:gosec // a fixed path inside this package, not user input
	if err != nil {
		t.Fatal(err)
	}
	// The responder installs a message listener on load. Node has no such thing
	// on globalThis, so stub it: this test is about the digest, and guarding the
	// shipped file against a context it never runs in would be noise.
	script := "globalThis.addEventListener = function () {};\n" + string(src) + `
;(async () => {
  const targets = ` + string(in) + `;
  const out = [];
  for (const t of targets) { out.push(await globalThis.realOrigin.id(t)); }
  process.stdout.write(JSON.stringify(out));
})();
`
	dir := t.TempDir()
	path := filepath.Join(dir, "parity.mjs")
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil { //nolint:gosec // path is a fixed name inside this test's own TempDir
		t.Fatal(err)
	}

	out, err := exec.Command(node, path).Output() //nolint:gosec // node is resolved from PATH and the script is written by this test into its own TempDir
	if err != nil {
		t.Fatalf("running the JavaScript implementation failed: %v", err)
	}
	var got []string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if len(got) != len(targets) {
		t.Fatalf("got %d ids for %d targets", len(got), len(targets))
	}
	for i, target := range targets {
		if want := ID(target); got[i] != want {
			t.Errorf("ID(%q): Go says %q, JavaScript says %q", target, want, got[i])
		}
	}
}
