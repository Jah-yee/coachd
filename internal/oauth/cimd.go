package oauth

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"golang.org/x/net/idna"
)

const (
	maxClientIDLen   = 512
	maxDocumentBytes = 16 << 10
	maxRedirectURIs  = 20
	maxNameRunes     = 100
	fetchTimeout     = 5 * time.Second

	minCache     = 5 * time.Minute
	maxCache     = 24 * time.Hour
	defaultCache = time.Hour
)

// cimdHost checks clientID against the CIMD URL rules of spec §5.2: https, a
// path other than "/", no query, fragment, userinfo, dot segments or port, at
// most 512 bytes. It returns the host in ASCII (punycode) form, the form
// coachd stores and shows.
func cimdHost(clientID string) (host string, ok bool) {
	rest, ok := strings.CutPrefix(clientID, "https://")
	if !ok || len(clientID) > maxClientIDLen || strings.ContainsAny(clientID, "?#") {
		return "", false
	}
	u, err := url.Parse(clientID)
	if err != nil || u.Opaque != "" || u.User != nil {
		return "", false
	}
	// The authority as written must be the bare host: no userinfo, port or
	// percent-escapes.
	authority, _, _ := strings.Cut(rest, "/")
	if authority != u.Host || u.Host != u.Hostname() || u.Path == "" || u.Path == "/" {
		return "", false
	}
	for _, seg := range strings.Split(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "", false
		}
	}
	host, err = idna.Lookup.ToASCII(u.Hostname())
	if err != nil || slices.Contains(strings.Split(host, "."), "") {
		return "", false
	}
	return host, true
}

// document is a validated Client ID Metadata Document.
type document struct {
	ClientID     string
	Name         string   // client_name made safe by displayName
	RedirectURIs []string // the valid entries only
	Raw          []byte   // the document as fetched
}

// errDocument means a metadata document broke a rule of spec §5.2. The
// message names the rule, never a value from the document.
var errDocument = errors.New("oauth: client metadata invalid")

// parseDocument validates a metadata document fetched from clientID. self is
// coachd's own origin, on which no redirect URI may point. Unknown fields and
// other token_endpoint_auth_method values (ChatGPT's private_key_jwt) are
// ignored: every CIMD client is treated as a public client.
func parseDocument(clientID string, body []byte, self string) (document, error) {
	var m struct {
		ClientID                *string         `json:"client_id"`
		ClientName              string          `json:"client_name"`
		RedirectURIs            []string        `json:"redirect_uris"`
		GrantTypes              []string        `json:"grant_types"`
		ResponseTypes           []string        `json:"response_types"`
		TokenEndpointAuthMethod string          `json:"token_endpoint_auth_method"`
		ClientSecret            json.RawMessage `json:"client_secret"`
		ClientSecretExpiresAt   json.RawMessage `json:"client_secret_expires_at"`
	}
	if err := json.Unmarshal(body, &m); err != nil {
		return document{}, fmt.Errorf("%w: not a JSON object", errDocument)
	}
	var rule string
	switch {
	case m.ClientID == nil || *m.ClientID != clientID:
		rule = "client_id differs from its URL"
	case m.ClientSecret != nil || m.ClientSecretExpiresAt != nil:
		rule = "client secret present"
	case strings.HasPrefix(strings.ToLower(m.TokenEndpointAuthMethod), "client_secret_"):
		rule = "client secret auth method"
	case m.GrantTypes != nil && !slices.Contains(m.GrantTypes, "authorization_code"):
		rule = "grant_types without authorization_code"
	case m.ResponseTypes != nil && !slices.Contains(m.ResponseTypes, "code"):
		rule = "response_types without code"
	case len(m.RedirectURIs) > maxRedirectURIs:
		rule = "too many redirect_uris"
	}
	if rule != "" {
		return document{}, fmt.Errorf("%w: %s", errDocument, rule)
	}
	var redirects []string
	for _, r := range m.RedirectURIs {
		if validRedirectURI(r, self) {
			redirects = append(redirects, r)
		}
	}
	if len(redirects) == 0 {
		return document{}, fmt.Errorf("%w: no valid redirect_uris", errDocument)
	}
	return document{ClientID: clientID, Name: displayName(m.ClientName), RedirectURIs: redirects, Raw: body}, nil
}

// displayName makes a client-supplied name safe to show: control characters
// (Cc) and bidi controls are stripped and the rest truncated to 100 runes. An
// empty result is "unnamed".
func displayName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r) {
			continue
		}
		if n == maxNameRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	if name := strings.TrimSpace(b.String()); name != "" {
		return name
	}
	return "unnamed"
}

// cacheFor returns how long a fetched document may be kept: its max-age
// clamped to [5 min, 24 h], or 1 h without a usable one.
func cacheFor(h http.Header) time.Duration {
	for _, v := range h.Values("Cache-Control") {
		for _, d := range strings.Split(v, ",") {
			name, val, ok := strings.Cut(d, "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "max-age") {
				continue
			}
			n, err := strconv.ParseUint(strings.Trim(strings.TrimSpace(val), `"`), 10, 64)
			switch {
			case errors.Is(err, strconv.ErrRange):
				return maxCache
			case err != nil:
				return defaultCache
			case n < uint64(minCache/time.Second):
				return minCache
			case n > uint64(maxCache/time.Second):
				return maxCache
			}
			return time.Duration(n) * time.Second
		}
	}
	return defaultCache
}

// refusedPrefixes are the special-purpose ranges the netip predicates let
// through (spec §5.2). 100.64.0.0/10 holds the tailnet addresses.
var refusedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fec0::/10"),
	// The IPv4-embedding forms Unmap leaves alone: IPv4-compatible
	// (deprecated, still tunnelled by some stacks) and IPv4-translated.
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
}

// publicAddr reports whether a is public unicast: an IPv4-mapped address is
// judged as its IPv4 form, and loopback, private, link-local, multicast,
// unspecified, zoned and refusedPrefixes addresses are not.
func publicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if a.Zone() != "" || !a.IsGlobalUnicast() || a.IsPrivate() {
		return false
	}
	for _, p := range refusedPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// fetcherOpts are test seams; the zero value is production and no code
// outside _test.go files sets them.
type fetcherOpts struct {
	resolve   func(ctx context.Context, host string) ([]netip.Addr, error)      // nil: net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	allow     func(netip.Addr) bool                                             // nil: publicAddr
	tlsConfig *tls.Config                                                       // nil: system roots
	dial      func(ctx context.Context, network, addr string) (net.Conn, error) // nil: net.Dialer; addr is the checked IP and port
}

// fetcher fetches metadata documents with a plain http.Client: no proxy, no
// redirects, no OpenTelemetry instrumentation.
type fetcher struct {
	client *http.Client
	o      fetcherOpts
}

// errFetch is every fetch failure, a refused address included. Callers show
// one page for all of them, so the fetch cannot serve as a port or address
// oracle; the wrapped reason is for the error log only and, like the rest of
// the text, never carries the URL.
var errFetch = errors.New("oauth: client metadata unavailable")

// errAddress means an address of the client_id host failed the allow check.
var errAddress = errors.New("oauth: client metadata address refused")

func newFetcher(o fetcherOpts) *fetcher {
	if o.resolve == nil {
		o.resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	if o.allow == nil {
		o.allow = publicAddr
	}
	if o.dial == nil {
		o.dial = (&net.Dialer{}).DialContext
	}
	f := &fetcher{o: o}
	f.client = &http.Client{
		Transport: &http.Transport{
			Proxy:                  nil, // HTTP_PROXY and friends are ignored
			DialContext:            f.dialContext,
			TLSClientConfig:        o.tlsConfig,
			TLSHandshakeTimeout:    fetchTimeout,
			DisableKeepAlives:      true,
			MaxResponseHeaderBytes: 64 << 10,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Timeout:       fetchTimeout,
	}
	return f
}

// dialContext resolves the host itself, refuses the connection if any
// address fails the allow check, and connects to the first one. The address
// checked is the address dialled, so DNS rebinding cannot swap it.
func (f *fetcher) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	addrs, err := f.o.resolve(ctx, host)
	if err != nil {
		return nil, err
	}
	if len(addrs) == 0 {
		return nil, errAddress
	}
	for _, a := range addrs {
		if !f.o.allow(a) {
			return nil, errAddress
		}
	}
	return f.o.dial(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
}

// fetch gets and validates the metadata document at clientID, and returns it
// with how long it may be cached. Every failure wraps errFetch.
func (f *fetcher) fetch(ctx context.Context, clientID, self string) (document, time.Duration, error) {
	host, ok := cimdHost(clientID)
	if !ok {
		return document{}, 0, fmt.Errorf("%w: invalid client_id", errFetch)
	}
	u, err := url.Parse(clientID)
	if err != nil {
		return document{}, 0, fmt.Errorf("%w: invalid client_id", errFetch)
	}
	u.Host = host
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return document{}, 0, fmt.Errorf("%w: request", errFetch)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := f.client.Do(req)
	if err != nil {
		return document{}, 0, fmt.Errorf("%w: %s", errFetch, failure(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return document{}, 0, fmt.Errorf("%w: status %d", errFetch, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxDocumentBytes+1))
	if err != nil {
		return document{}, 0, fmt.Errorf("%w: %s", errFetch, failure(err))
	}
	if len(body) > maxDocumentBytes {
		return document{}, 0, fmt.Errorf("%w: document too large", errFetch)
	}
	doc, err := parseDocument(clientID, body, self)
	if err != nil {
		return document{}, 0, fmt.Errorf("%w: %w", errFetch, err)
	}
	return doc, cacheFor(resp.Header), nil
}

// failure names why a request failed without quoting the error, which
// carries the URL.
func failure(err error) string {
	var t interface{ Timeout() bool }
	switch {
	case errors.Is(err, errAddress):
		return "address refused"
	case errors.As(err, &t) && t.Timeout():
		return "timeout"
	default:
		return "request failed"
	}
}
