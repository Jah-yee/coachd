package oauth

import (
	"context"
	"crypto/tls"
	"net"
	"net/netip"
)

// This file is compiled only by `go test` of this directory, never into a
// binary (spec M18): it lets the external end-to-end test point the CIMD
// fetcher at a local document server.

// NewWithFetcher is New with the CIMD fetcher's test seams set.
func NewWithFetcher(d Deps, o fetcherOpts) (*Server, error) { return newServer(d, o) }

// FetcherOpts builds the fetcher's test seams; see fetcherOpts for what each
// replaces.
func FetcherOpts(resolve func(context.Context, string) ([]netip.Addr, error), allow func(netip.Addr) bool,
	tlsConfig *tls.Config, dial func(ctx context.Context, network, addr string) (net.Conn, error)) fetcherOpts {
	return fetcherOpts{resolve: resolve, allow: allow, tlsConfig: tlsConfig, dial: dial}
}
