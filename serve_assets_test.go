package realorigin

import (
	"bytes"
	"net/http"
	"testing"
)

// handlerWith builds a handler for cfg, filling in the two required origins.
func handlerWith(t *testing.T, cfg Config) http.Handler {
	t.Helper()
	cfg.Suffix, cfg.AppOrigin = ".mesh.localhost", "https://app.example"
	h, err := Handler(cfg)
	if err != nil {
		t.Fatalf("Handler: %v", err)
	}
	return h
}

// An embedder that supplies its own worker must actually get it served: running
// a different implementation on the browse origin is the point of the seam.
func TestWorkerOverrideIsServed(t *testing.T) {
	rec := get(t, handlerWith(t, Config{Worker: []byte("// mine\n")}), "/sw.js")
	if !bytes.Equal(rec.Body.Bytes(), []byte("// mine\n")) {
		t.Errorf("worker body = %q, want the override", rec.Body.String())
	}
}

// A wasm module must arrive as application/wasm or instantiateStreaming rejects
// it, and the failure reads like a corrupt module rather than a header problem.
func TestAssetsAreServedWithTheRightType(t *testing.T) {
	h := handlerWith(t, Config{Assets: map[string][]byte{
		"/sw.wasm":     []byte("\x00asm"),
		"wasm_exec.js": []byte("//x"),
	}})
	if ct := get(t, h, "/sw.wasm").Header().Get("Content-Type"); ct != "application/wasm" {
		t.Errorf("/sw.wasm content-type = %q, want application/wasm", ct)
	}
	// A key given without a leading slash is still reachable at its path.
	if code := get(t, h, "/wasm_exec.js").Code; code != http.StatusOK {
		t.Errorf("/wasm_exec.js status = %d, want 200", code)
	}
}

// An asset shadowing the worker path would silently replace the bridge, so it is
// refused outright rather than resolved by precedence.
func TestAssetCollidingWithTheWorkerIsRefused(t *testing.T) {
	if _, err := Handler(Config{
		Suffix: ".mesh.localhost", AppOrigin: "https://app.example",
		Assets: map[string][]byte{"/sw.js": []byte("x")},
	}); err == nil {
		t.Fatal("an asset colliding with SWPath was accepted")
	}
}

// Assets must not eat the catch-all: every other path is still the shell, and
// navigation depends on that.
func TestAssetsDoNotDisplaceTheShell(t *testing.T) {
	h := handlerWith(t, Config{Assets: map[string][]byte{"/sw.wasm": []byte("\x00asm")}})
	if !bytes.Contains(get(t, h, "/some/page").Body.Bytes(), []byte("realorigin-hello")) {
		t.Error("an unknown path no longer serves the bootstrap shell")
	}
}

// The caller's map is copied: mutating it afterwards must not change what a
// running server hands to the untrusted origin.
func TestAssetMapIsCopied(t *testing.T) {
	m := map[string][]byte{"/sw.wasm": []byte("\x00asm")}
	h := handlerWith(t, Config{Assets: m})
	m["/sneak.js"] = []byte("alert(1)")
	if get(t, h, "/sneak.js").Header().Get("Content-Type") == "text/javascript; charset=utf-8" {
		t.Error("a key added after Handler() was served as an asset")
	}
}
