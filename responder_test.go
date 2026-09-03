package realorigin

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runResponder drives the shipped responder.js under node with the given script
// appended, and returns what it wrote to stdout. Testing the real file rather
// than a transcription of it is the point: this is the trust boundary, and a
// copy would drift.
func runResponder(t *testing.T, script string) []byte {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed, so the responder's behavior is UNCHECKED here")
	}
	src, err := os.ReadFile(filepath.Join("web", "responder.js")) //nolint:gosec // a fixed path inside this package
	if err != nil {
		t.Fatal(err)
	}
	// The responder registers a message listener on load; node has none, so
	// capture the handler instead, which is also how the test delivers events.
	prelude := "var handler = null;\n" +
		"globalThis.addEventListener = function (type, fn) { if (type === 'message') handler = fn; };\n"
	path := filepath.Join(t.TempDir(), "probe.mjs")
	if err := os.WriteFile(path, []byte(prelude+string(src)+script), 0o600); err != nil { //nolint:gosec // a fixed name in this test's own TempDir
		t.Fatal(err)
	}
	out, err := exec.Command(node, path).Output() //nolint:gosec // node from PATH, script written by this test
	if err != nil {
		t.Fatalf("the responder failed under node: %v", err)
	}
	return out
}

const connectHelper = `
  const SUFFIX = '.mesh.localhost';
  function connect(id, origin) {
    return new Promise((resolve) => {
      const source = { postMessage: (msg, o, transfer) => {
        if (msg.error) { resolve({ error: msg.error }); return; }
        resolve({ port: transfer[0] });
      } };
      handler({ data: { type: 'realorigin-hello', shortid: id },
                origin: origin === undefined ? ('https://' + id + SUFFIX) : origin,
                source });
    });
  }
`

// A frame's interstitial should show its own transport's progress and not another
// site's. Without an id the line is a broadcast, which is right when one frame is
// loading and leaky when several are — so an embedder that knows the frame says so.
func TestProgressGoesOnlyToTheFrameItNames(t *testing.T) {
	out := runResponder(t, `
;(async () => {`+connectHelper+`
  realOrigin.configure({ suffix: SUFFIX, fetch: () => Promise.resolve({ status: 200, headers: {}, body: null }) });
  const idA = await realOrigin.register('site-a');
  const idB = await realOrigin.register('site-b');
  const got = { a: [], b: [] };
  const a = await connect(idA), b = await connect(idB);
  a.port.onmessage = (e) => { if (e.data && e.data.progress) got.a.push(e.data.progress.line); };
  b.port.onmessage = (e) => { if (e.data && e.data.progress) got.b.push(e.data.progress.line); };
  realOrigin.progress('a-only', idA);
  realOrigin.progress('everyone');
  realOrigin.progress('b-only', idB);
  realOrigin.progress('nobody', 'not-a-registered-id');
  await new Promise((r) => setTimeout(r, 150));
  process.stdout.write(JSON.stringify(got));
  process.exit(0);
})();
`)
	var got struct{ A, B []string }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	joinedA, joinedB := strings.Join(got.A, ","), strings.Join(got.B, ",")
	if joinedA != "a-only,everyone" {
		t.Errorf("frame A saw %q, want \"a-only,everyone\"", joinedA)
	}
	if joinedB != "everyone,b-only" {
		t.Errorf("frame B saw %q, want \"everyone,b-only\"", joinedB)
	}
}

// An id nobody registered gets no port. Handing one out would let any page under
// the suffix obtain a fetch capability for a target of its choosing.
func TestUnknownIDGetsNoPort(t *testing.T) {
	out := runResponder(t, `
;(async () => {`+connectHelper+`
  realOrigin.configure({ suffix: SUFFIX, fetch: () => Promise.resolve({ status: 200, headers: {}, body: null }) });
  const r = await connect('neverregisteredxxxxx');
  process.stdout.write(JSON.stringify({ error: r.error || null, gotPort: !!r.port }));
  process.exit(0);
})();
`)
	var got struct {
		Error   *string
		GotPort bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.GotPort {
		t.Fatal("an unregistered id was handed a fetch capability")
	}
	if got.Error == nil || *got.Error == "" {
		t.Error("an unregistered id should be told why it was refused")
	}
}

// The origin of the sender is checked before anything else. A page that is not
// under the browse suffix must never get a port, whatever id it claims — this is
// the check that keeps the app's own pages, and any other site, out of the bridge.
func TestOriginOutsideTheSuffixIsIgnored(t *testing.T) {
	out := runResponder(t, `
;(async () => {`+connectHelper+`
  realOrigin.configure({ suffix: SUFFIX, fetch: () => Promise.resolve({ status: 200, headers: {}, body: null }) });
  const id = await realOrigin.register('site-a');
  let answered = false;
  const source = { postMessage: () => { answered = true; } };
  for (const origin of ['https://evil.example', 'https://app.example',
                        'https://' + id + '.mesh.localhost.evil.example']) {
    handler({ data: { type: 'realorigin-hello', shortid: id }, origin, source });
  }
  await new Promise((r) => setTimeout(r, 100));
  process.stdout.write(JSON.stringify({ answered }));
  process.exit(0);
})();
`)
	var got struct{ Answered bool }
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.Answered {
		t.Error("a sender outside the browse suffix got an answer from the responder")
	}
}

// Without a transport there is nothing to answer with, and the caller should be
// told rather than left waiting for a port that never arrives.
func TestNoTransportIsRefusedNotIgnored(t *testing.T) {
	out := runResponder(t, `
;(async () => {`+connectHelper+`
  realOrigin.configure({ suffix: SUFFIX });
  const id = await realOrigin.register('site-a');
  const r = await connect(id);
  process.stdout.write(JSON.stringify({ error: r.error || null, gotPort: !!r.port }));
  process.exit(0);
})();
`)
	var got struct {
		Error   *string
		GotPort bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decoding %q: %v", out, err)
	}
	if got.GotPort {
		t.Fatal("a port was handed out with no transport configured")
	}
	if got.Error == nil || !strings.Contains(*got.Error, "transport") {
		t.Errorf("error was %v, want it to mention the missing transport", got.Error)
	}
}
