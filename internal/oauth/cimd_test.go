package oauth

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestCIMDHost(t *testing.T) {
	host, ok := cimdHost("https://claude.ai/oauth/mcp-oauth-client-metadata")
	if !ok || host != "claude.ai" {
		t.Errorf("claude.ai: got %q, %v", host, ok)
	}
	host, ok = cimdHost("https://bücher.example/c.json")
	if !ok || host != "xn--bcher-kva.example" {
		t.Errorf("unicode host: got %q, %v", host, ok)
	}
	long := "https://claude.ai/" + strings.Repeat("a", 513-len("https://claude.ai/"))
	if len(long) != 513 {
		t.Fatalf("long id is %d bytes", len(long))
	}
	for _, s := range []string{
		"https://claude.ai/",
		"https://claude.ai",
		"http://claude.ai/x",
		"https://claude.ai:8443/x",
		"https://claude.ai:443/x",
		"https://claude.ai:/x",
		"https://claude.ai/x?y=1",
		"https://claude.ai/x?",
		"https://claude.ai/x#f",
		"https://u@claude.ai/x",
		"https://@claude.ai/x",
		"https://claude.ai/a/../b",
		"https://claude.ai/a/./b",
		"https://claude.ai/a/%2e%2e/b",
		long,
		"https://exa_mple.com/c.json",
		"https://-x.example/c.json",
		"https://claude.ai./x",
		"https:///x",
		"https://b%C3%BCcher.example/c.json",
		"https://[2606:4700::1111]/x",
		"",
	} {
		if h, ok := cimdHost(s); ok {
			t.Errorf("cimdHost(%q) = %q, true; want not ok", s, h)
		}
	}
}

var fixtures = []struct{ file, clientID, name string }{
	{"claude-ai.json", "https://claude.ai/oauth/mcp-oauth-client-metadata", "Claude"},
	{"claude-code.json", "https://claude.ai/oauth/claude-code-client-metadata", "Claude Code"},
	{"chatgpt.json", "https://chatgpt.com/oauth/client.json", "ChatGPT"},
}

func TestParseDocumentFixtures(t *testing.T) {
	for _, f := range fixtures {
		body, err := os.ReadFile("testdata/" + f.file)
		if err != nil {
			t.Fatal(err)
		}
		var raw struct {
			RedirectURIs []string `json:"redirect_uris"`
		}
		if err := json.Unmarshal(body, &raw); err != nil {
			t.Fatal(err)
		}
		doc, err := parseDocument(f.clientID, body, testSelf)
		if err != nil {
			t.Errorf("%s: %v", f.file, err)
			continue
		}
		if doc.ClientID != f.clientID || doc.Name != f.name || !slices.Equal(doc.RedirectURIs, raw.RedirectURIs) || !bytes.Equal(doc.Raw, body) {
			t.Errorf("%s: got %+v", f.file, doc)
		}
	}
}

const testClientID = "https://app.example/c.json"

func testDoc() map[string]any {
	return map[string]any{
		"client_id":                  testClientID,
		"client_name":                "App",
		"redirect_uris":              []string{"https://a.com/cb"},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	}
}

func marshal(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseDocumentRefusals(t *testing.T) {
	if _, err := parseDocument(testClientID, marshal(t, testDoc()), testSelf); err != nil {
		t.Fatalf("base document refused: %v", err)
	}
	minimal := testDoc()
	for _, k := range []string{"client_name", "grant_types", "response_types", "token_endpoint_auth_method"} {
		delete(minimal, k)
	}
	if doc, err := parseDocument(testClientID, marshal(t, minimal), testSelf); err != nil || doc.Name != "unnamed" {
		t.Fatalf("minimal document: %+v, %v", doc, err)
	}
	twenty := make([]string, 20)
	for i := range twenty {
		twenty[i] = fmt.Sprintf("https://a.com/cb%d", i)
	}
	d := testDoc()
	d["redirect_uris"] = twenty
	if _, err := parseDocument(testClientID, marshal(t, d), testSelf); err != nil {
		t.Fatalf("20 redirect URIs refused: %v", err)
	}

	cases := map[string]func(map[string]any){
		"client_id mismatch":         func(d map[string]any) { d["client_id"] = "https://app.example/other.json" },
		"client_id missing":          func(d map[string]any) { delete(d, "client_id") },
		"client_secret":              func(d map[string]any) { d["client_secret"] = "s3cret" },
		"client_secret null":         func(d map[string]any) { d["client_secret"] = nil },
		"client_secret_expires_at":   func(d map[string]any) { d["client_secret_expires_at"] = 0 },
		"client_secret_basic":        func(d map[string]any) { d["token_endpoint_auth_method"] = "client_secret_basic" },
		"client_secret_post":         func(d map[string]any) { d["token_endpoint_auth_method"] = "client_secret_post" },
		"no authorization_code":      func(d map[string]any) { d["grant_types"] = []string{"refresh_token"} },
		"empty grant_types":          func(d map[string]any) { d["grant_types"] = []string{} },
		"no code response type":      func(d map[string]any) { d["response_types"] = []string{"token"} },
		"no valid redirect":          func(d map[string]any) { d["redirect_uris"] = []string{"cursor://x", "http://example.com/cb"} },
		"no redirect_uris":           func(d map[string]any) { delete(d, "redirect_uris") },
		"redirect on coachd itself":  func(d map[string]any) { d["redirect_uris"] = []string{testSelf + "/x"} },
		"21 redirect URIs":           func(d map[string]any) { d["redirect_uris"] = append(slices.Clone(twenty), "https://a.com/cb20") },
		"redirect_uris not an array": func(d map[string]any) { d["redirect_uris"] = "https://a.com/cb" },
	}
	for name, mutate := range cases {
		d := testDoc()
		mutate(d)
		if doc, err := parseDocument(testClientID, marshal(t, d), testSelf); err == nil {
			t.Errorf("%s: accepted %+v", name, doc)
		}
	}
	for _, body := range []string{`{"client_id":`, `[]`, `null`, `{"client_id":"https://app.example/c.json"} x`} {
		if _, err := parseDocument(testClientID, []byte(body), testSelf); err == nil {
			t.Errorf("body %q accepted", body)
		}
	}
}

func TestParseDocumentDropsBadRedirects(t *testing.T) {
	d := testDoc()
	d["redirect_uris"] = []string{"cursor://x", "https://a.com/cb"}
	doc, err := parseDocument(testClientID, marshal(t, d), testSelf)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(doc.RedirectURIs, []string{"https://a.com/cb"}) {
		t.Errorf("redirects = %q", doc.RedirectURIs)
	}
}

func TestDisplayName(t *testing.T) {
	cases := map[string]string{
		"Cl\u202eaude\u0007":                   "Claude",
		"a\u2066b\u2069c\u200ed\u200fe\u202af": "abcdef",
		"\x00Chat\nGPT\u0085":                  "ChatGPT",
		"x\u061cy":                             "xy",
		"":                                     "unnamed",
		"\u202e\u0007":                         "unnamed",
		"  ":                                   "unnamed",
		"Claude Code":                          "Claude Code",
	}
	for in, want := range cases {
		if got := displayName(in); got != want {
			t.Errorf("displayName(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("é", 150)
	got := displayName(long)
	if n := utf8.RuneCountInString(got); n != 100 || got != strings.Repeat("é", 100) {
		t.Errorf("150 runes truncated to %d runes", n)
	}
}

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{
		"127.0.0.1", "10.1.2.3", "172.16.0.1", "192.168.0.25", "169.254.1.1", "100.100.1.1",
		"0.1.2.3", "192.0.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1",
		"240.0.0.1", "224.0.0.1", "255.255.255.255", "0.0.0.0",
		"::1", "fe80::1", "fc00::1", "fd12::1", "fec0::1", "2001:db8::1", "2002::1", "64:ff9b::1",
		"64:ff9b:1::1", "100::1", "::ffff:127.0.0.1", "::ffff:10.0.0.1", "::ffff:100.64.0.1", "::", "ff02::1",
		"::7f00:1", "::ffff:0:a00:1", "2606:4700::1111%eth0",
	} {
		if publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("publicAddr(%s) = true, want false", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "2606:4700::1111", "::ffff:1.1.1.1"} {
		if !publicAddr(netip.MustParseAddr(s)) {
			t.Errorf("publicAddr(%s) = false, want true", s)
		}
	}
	if publicAddr(netip.Addr{}) {
		t.Error("publicAddr(zero) = true")
	}
}

func TestCacheFor(t *testing.T) {
	cases := map[string]time.Duration{
		"":                       time.Hour,
		"public, max-age=300":    5 * time.Minute,
		"max-age=60":             5 * time.Minute,
		"max-age=7200":           2 * time.Hour,
		"MAX-AGE=999999":         24 * time.Hour,
		"max-age=99999999999999": 24 * time.Hour,
		"max-age=-1":             time.Hour,
		"max-age=abc":            time.Hour,
		"no-cache":               time.Hour,
	}
	for v, want := range cases {
		h := http.Header{}
		if v != "" {
			h.Set("Cache-Control", v)
		}
		if got := cacheFor(h); got != want {
			t.Errorf("cacheFor(%q) = %v, want %v", v, got, want)
		}
	}
}

func TestFetchRefusesMixedRecords(t *testing.T) {
	cases := map[string]func() ([]netip.Addr, error){
		"public then private": func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("10.0.0.1")}, nil
		},
		"private then public": func() ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("1.1.1.1")}, nil
		},
		"no addresses":   func() ([]netip.Addr, error) { return nil, nil },
		"resolver error": func() ([]netip.Addr, error) { return nil, errors.New("nxdomain") },
	}
	for name, answer := range cases {
		var resolves, dials atomic.Int32
		f := newFetcher(fetcherOpts{
			resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
				resolves.Add(1)
				return answer()
			},
			dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("dial attempted")
			},
		})
		_, _, err := f.fetch(context.Background(), testClientID, testSelf)
		if !errors.Is(err, errFetch) {
			t.Errorf("%s: err = %v, want errFetch", name, err)
		}
		if resolves.Load() != 1 || dials.Load() != 0 {
			t.Errorf("%s: resolves %d, dials %d; want 1, 0", name, resolves.Load(), dials.Load())
		}
	}
}

func TestFetchRefusesBadClientID(t *testing.T) {
	var resolves atomic.Int32
	f := newFetcher(fetcherOpts{resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
		resolves.Add(1)
		return nil, errors.New("unexpected resolve")
	}})
	for _, id := range []string{"http://app.example/c.json", "https://app.example:8443/c.json", "https://app.example/"} {
		if _, _, err := f.fetch(context.Background(), id, testSelf); !errors.Is(err, errFetch) {
			t.Errorf("%s: err = %v, want errFetch", id, err)
		}
	}
	if resolves.Load() != 0 {
		t.Errorf("resolved %d times for invalid client ids", resolves.Load())
	}
}

// testHost is the name the httptest certificate is valid for.
const testHost = "example.com"

func TestFetchRules(t *testing.T) {
	doc := func(path string) []byte {
		d := testDoc()
		d["client_id"] = "https://" + testHost + path
		return marshal(t, d)
	}
	padded := func(path string, size int) []byte {
		b := doc(path)
		return append(b, bytes.Repeat([]byte(" "), size-len(b))...)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/ok300", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=300")
		w.Write(doc(r.URL.Path))
	})
	mux.HandleFunc("/nohdr", func(w http.ResponseWriter, r *http.Request) { w.Write(doc(r.URL.Path)) })
	mux.HandleFunc("/ok60", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=60")
		w.Write(doc(r.URL.Path))
	})
	mux.HandleFunc("/okbig", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "max-age=999999")
		w.Write(doc(r.URL.Path))
	})
	mux.HandleFunc("/exact", func(w http.ResponseWriter, r *http.Request) { w.Write(padded(r.URL.Path, 16<<10)) })
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) { w.Write(padded(r.URL.Path, 16<<10+1)) })
	mux.HandleFunc("/redirect", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusFound)
	})
	// A document valid for /redirect, so only the refusal to follow the 302
	// can fail that fetch.
	mux.HandleFunc("/target", func(w http.ResponseWriter, r *http.Request) { w.Write(doc("/redirect")) })
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(6 * time.Second):
			w.Write(doc(r.URL.Path))
		case <-r.Context().Done():
		}
	})
	mux.HandleFunc("/missing", http.NotFound)
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)

	roots := x509.NewCertPool()
	roots.AddCert(srv.Certificate())
	loopback := netip.MustParseAddr("127.0.0.1")
	var dials atomic.Int32
	f := newFetcher(fetcherOpts{
		resolve: func(ctx context.Context, host string) ([]netip.Addr, error) {
			if host != testHost {
				return nil, fmt.Errorf("unexpected host %q", host)
			}
			return []netip.Addr{loopback}, nil
		},
		allow:     func(a netip.Addr) bool { return a == loopback },
		tlsConfig: &tls.Config{RootCAs: roots},
		// The production dialer resolves, checks and picks the address; only
		// the port differs, because the test server cannot listen on 443.
		dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			dials.Add(1)
			if addr != "127.0.0.1:443" {
				return nil, fmt.Errorf("dial to unchecked address %q", addr)
			}
			var d net.Dialer
			return d.DialContext(ctx, network, srv.Listener.Addr().String())
		},
	})

	for path, want := range map[string]time.Duration{
		"/ok300": 5 * time.Minute,
		"/nohdr": time.Hour,
		"/ok60":  5 * time.Minute,
		"/okbig": 24 * time.Hour,
		"/exact": time.Hour,
	} {
		id := "https://" + testHost + path
		got, maxAge, err := f.fetch(context.Background(), id, testSelf)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if got.ClientID != id || maxAge != want {
			t.Errorf("%s: client %q, max age %v; want %v", path, got.ClientID, maxAge, want)
		}
	}

	for _, path := range []string{"/redirect", "/big", "/slow", "/missing"} {
		id := "https://" + testHost + path
		start := time.Now()
		_, _, err := f.fetch(context.Background(), id, testSelf)
		if !errors.Is(err, errFetch) {
			t.Errorf("%s: err = %v, want errFetch", path, err)
			continue
		}
		if took := time.Since(start); took >= 6*time.Second {
			t.Errorf("%s: took %v", path, took)
		}
		if strings.Contains(err.Error(), testHost) || strings.Contains(err.Error(), path) {
			t.Errorf("%s: error %q carries the client_id URL", path, err)
		}
	}
	if dials.Load() == 0 {
		t.Error("no dial went through the test dialer")
	}
}
