// Command realorigin-demo is a working example of the substrate with an
// ordinary HTTP transport.
//
// It runs the two halves the design requires:
//
//	the app     on http://localhost:7999      — holds the transport
//	browse origins on http://<id>.demo.localhost:7998
//
// Both names resolve to loopback with no DNS, and browsers treat *.localhost as
// a secure context, so a service worker registers over plain HTTP and no
// certificate is needed. Two names are still required: same host and port would
// mean one origin, and the content would be inside the app.
//
// The transport here fetches over plain HTTP from the app's own process, which
// makes the demo a real-origin proxy that sidesteps CORS — the browsed page runs
// at an origin of its own with working storage, history and cookies, and never
// gets a handle on anything the app holds. Swap the transport for a mesh, an
// onion route or an encrypted archive and nothing else changes.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	_ "embed"

	"github.com/0magnet/realorigin"
)

//go:embed app.html
var appHTML []byte

func main() {
	appAddr := flag.String("app", "127.0.0.1:7999", "listen address for the app origin")
	browseAddr := flag.String("browse", "127.0.0.1:7998", "listen address for the browse origins")
	suffix := flag.String("suffix", ".demo.localhost", "browse-origin domain suffix")
	flag.Parse()

	browsePort := *browseAddr
	if i := strings.LastIndex(browsePort, ":"); i >= 0 {
		browsePort = browsePort[i+1:]
	}
	appOrigin := "http://localhost" + portOf(*appAddr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The browse origins: the static half. It serves the worker and the shell and
	// never sees any content.
	cfg := realorigin.Config{Addr: *browseAddr, Suffix: *suffix, AppOrigin: appOrigin}
	go func() {
		if err := cfg.ListenAndServe(ctx); err != nil {
			log.Fatalf("browse origin: %v", err)
		}
	}()

	// The app origin: the page, the responder, and the transport's server side.
	mux := http.NewServeMux()
	mux.HandleFunc("/responder.js", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		_, _ = w.Write(realorigin.ResponderJS()) //nolint:errcheck // nothing to do about a client that hung up
	})
	mux.HandleFunc("/via", proxy)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		cfgJSON, _ := json.Marshal(map[string]string{"suffix": *suffix, "browsePort": browsePort}) //nolint:errcheck // a map of two strings cannot fail to marshal
		body := strings.Replace(string(appHTML), "<script src=\"/responder.js\">",
			"<script>window.__DEMO="+string(cfgJSON)+";</script>\n<script src=\"/responder.js\">", 1)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, body) //nolint:errcheck // as above
	})

	srv := &http.Server{Addr: *appAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { <-ctx.Done(); _ = srv.Close() }() //nolint:errcheck // shutdown path; the error has nowhere to go

	fmt.Printf("app     %s\n", appOrigin)
	fmt.Printf("browse  http://<id>%s:%s\n", *suffix, browsePort)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

// proxy is the demo's transport. A real one would not be an HTTP client.
func proxy(w http.ResponseWriter, r *http.Request) {
	target := r.URL.Query().Get("url")
	if !strings.HasPrefix(target, "http://") && !strings.HasPrefix(target, "https://") {
		http.Error(w, "only http and https targets", http.StatusBadRequest)
		return
	}
	//nolint:gosec // G704: fetching a caller-supplied URL is exactly what a proxy is for; this demo binds to loopback
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target, r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	// Forward the browsed page's own headers, minus anything about this hop.
	var fwd map[string]string
	if err := json.Unmarshal([]byte(r.Header.Get("x-ro-forward")), &fwd); err == nil {
		for k, v := range fwd {
			switch strings.ToLower(k) {
			case "host", "origin", "referer", "connection", "content-length":
			default:
				req.Header.Set(k, v)
			}
		}
	}
	resp, err := (&http.Client{Timeout: 45 * time.Second}).Do(req) //nolint:gosec // G704: as above
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // close on the read path
	for k, vs := range resp.Header {
		switch strings.ToLower(k) {
		// The browser recomputes framing, and a frame-busting header from the
		// target would kill the demo's own iframe.
		case "content-length", "transfer-encoding", "connection",
			"content-security-policy", "x-frame-options":
		default:
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body) //nolint:errcheck // a truncated copy means the client left mid-stream
}

func portOf(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	return ""
}
