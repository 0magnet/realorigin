package realorigin

import (
	"bytes"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	h, err := Handler(Config{Suffix: ".mesh.localhost", AppOrigin: "https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestHandlerRequiresItsTwoOrigins(t *testing.T) {
	if _, err := Handler(Config{AppOrigin: "https://app.example"}); !errors.Is(err, ErrNoSuffix) {
		t.Errorf("no suffix: got %v, want ErrNoSuffix", err)
	}
	if _, err := Handler(Config{Suffix: ".mesh.localhost"}); !errors.Is(err, ErrNoAppOrigin) {
		t.Errorf("no app origin: got %v, want ErrNoAppOrigin", err)
	}
}

func TestWorkerIsServedAtItsOwnPath(t *testing.T) {
	rec := get(t, testHandler(t), "/sw.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/javascript; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if !bytes.Contains(rec.Body.Bytes(), []byte("addEventListener('fetch'")) {
		t.Error("the body served at the worker path is not the worker")
	}
	// A cached stale worker keeps answering with an old bridge protocol.
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control %q, want no-cache", cc)
	}
}

// The worker does not intercept navigations, so every navigation reaches the
// server — including links to paths deep inside a site. If any of those got
// something other than the shell, following a link would render the wrong thing
// or nothing. Serving the shell only at "/" is the obvious mistake this guards.
func TestEveryPathButTheWorkerServesTheShell(t *testing.T) {
	h := testHandler(t)
	for _, path := range []string{
		"/",
		"/index.html",
		"/deep/nested/page",
		"/page?with=query&more=1",
		"/sw.js.map",
		"/a.js",
		"/favicon.ico",
	} {
		rec := get(t, h, path)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", path, rec.Code)
			continue
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("realorigin-hello")) {
			t.Errorf("%s did not get the bootstrap shell", path)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q, want no-store", path, cc)
		}
	}
}

func TestShellIsFullySubstituted(t *testing.T) {
	body := get(t, testHandler(t), "/").Body.String()
	for _, placeholder := range []string{"__APP_ORIGIN__", "__SUFFIX__"} {
		if bytes.Contains([]byte(body), []byte(placeholder)) {
			t.Errorf("%s survived into the served shell", placeholder)
		}
	}
	if !bytes.Contains([]byte(body), []byte(`"https://app.example"`)) {
		t.Error("the app origin was not substituted in")
	}
	if !bytes.Contains([]byte(body), []byte(`".mesh.localhost"`)) {
		t.Error("the suffix was not substituted in")
	}
}

// A suffix given without its leading dot must still produce a shell whose
// hostname arithmetic works, since the frame derives its id by trimming exactly
// this string off its own hostname.
func TestShellSuffixAlwaysCarriesItsLeadingDot(t *testing.T) {
	h, err := Handler(Config{Suffix: "mesh.localhost", AppOrigin: "https://app.example"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(get(t, h, "/").Body.Bytes(), []byte(`".mesh.localhost"`)) {
		t.Error("a suffix given without a leading dot was not normalized in the shell")
	}
}

func TestCustomWorkerPath(t *testing.T) {
	h, err := Handler(Config{Suffix: ".x.localhost", AppOrigin: "https://app.example", SWPath: "/worker.js"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(get(t, h, "/worker.js").Body.Bytes(), []byte("addEventListener('fetch'")) {
		t.Error("the worker is not at the configured path")
	}
	// The bug this exists to catch: SWPath moved the worker but the shell went
	// on registering /sw.js, so the worker was served where nothing asked for it.
	if !bytes.Contains(get(t, h, "/").Body.Bytes(), []byte(`register('/worker.js'`)) {
		t.Error("the shell does not register the configured worker path")
	}
	if bytes.Contains(get(t, h, "/").Body.Bytes(), []byte("__SW_PATH__")) {
		t.Error("__SW_PATH__ survived into the served shell")
	}
	if !bytes.Contains(get(t, h, "/sw.js").Body.Bytes(), []byte("realorigin-hello")) {
		t.Error("the default path should be an ordinary navigation once the worker moved")
	}
}

func TestResponderIsServable(t *testing.T) {
	if !bytes.Contains(ResponderJS(), []byte("realorigin-hello")) {
		t.Error("ResponderJS does not look like the responder")
	}
}

func TestShellCanBeReplaced(t *testing.T) {
	h, err := Handler(Config{
		Suffix:    ".mesh.localhost",
		AppOrigin: "https://app.example",
		Shell:     []byte(`<!doctype html><p>mine, app=__APP_ORIGIN__ suffix=__SUFFIX__`),
	})
	if err != nil {
		t.Fatal(err)
	}
	body := get(t, h, "/some/path").Body.String()
	if !bytes.Contains([]byte(body), []byte("mine,")) {
		t.Fatal("the replacement shell was not served")
	}
	// A replacement gets the same substitutions, or it cannot find the app.
	if !bytes.Contains([]byte(body), []byte("app=https://app.example")) {
		t.Error("__APP_ORIGIN__ was not substituted into the replacement shell")
	}
	if !bytes.Contains([]byte(body), []byte("suffix=.mesh.localhost")) {
		t.Error("__SUFFIX__ was not substituted into the replacement shell")
	}
	// Replacing the shell must not disturb the worker.
	if !bytes.Contains(get(t, h, "/sw.js").Body.Bytes(), []byte("addEventListener('fetch'")) {
		t.Error("replacing the shell also replaced the worker")
	}
}

func TestBootstrapHTMLIsTheDefaultShell(t *testing.T) {
	if !bytes.Contains(BootstrapHTML(), []byte("realorigin-hello")) {
		t.Error("BootstrapHTML does not look like the shell")
	}
	h, err := Handler(Config{Suffix: ".m.localhost", AppOrigin: "https://a.example", Shell: BootstrapHTML()})
	if err != nil {
		t.Fatal(err)
	}
	// Handing the built-in shell back in must behave exactly as omitting it.
	def := get(t, testHandler(t), "/")
	explicit := get(t, h, "/")
	if bytes.Contains(explicit.Body.Bytes(), []byte("__SUFFIX__")) {
		t.Error("the shell passed back in was not substituted")
	}
	if def.Code != explicit.Code {
		t.Errorf("status differs: %d vs %d", def.Code, explicit.Code)
	}
}
