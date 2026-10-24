package oauth

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/stefiix92/coachd/internal/store"
)

// errUnknownClient is every client lookup failure. It never carries the
// client_id or a URL.
var errUnknownClient = errors.New("oauth: unknown client")

const negativeTTL = time.Minute

// client is a trusted client record.
type client struct {
	RowID        int64 // 0: DCR client never consented
	ClientID     string
	Kind, Name   string
	RedirectURIs []string
	Host         string // punycode web address; "" for DCR
}

type resolver struct {
	db       *store.DB
	self     string
	now      func() time.Time
	fetchDoc func(ctx context.Context, clientID, self string) (document, time.Duration, error)
	dcrKey   []byte
	budget   *rate.Limiter

	mu       sync.Mutex
	negative map[string]time.Time
}

func newResolver(db *store.DB, self string, dcrKey []byte) *resolver {
	return &resolver{
		db: db, self: self, now: time.Now,
		fetchDoc: newFetcher(fetcherOpts{}).fetch,
		dcrKey:   dcrKey,
		budget:   rate.NewLimiter(rate.Every(2*time.Second), 30),
		negative: map[string]time.Time{},
	}
}

func fromRow(c store.OAuthClient, host string) client {
	return client{RowID: c.ID, ClientID: c.ClientID, Kind: c.Kind, Name: c.Name, RedirectURIs: c.RedirectURIs, Host: host}
}

// stored resolves a client from the store and the DCR key only; it never
// fetches.
func (r *resolver) stored(ctx context.Context, clientID string) (client, error) {
	if strings.HasPrefix(clientID, dcrPrefix) {
		return r.resolveDCR(ctx, clientID)
	}
	host, ok := cimdHost(clientID)
	if !ok {
		return client{}, errUnknownClient
	}
	row, err := r.db.OAuthClientByClientID(ctx, clientID)
	if err != nil || row.Kind != "cimd" {
		return client{}, errUnknownClient
	}
	return fromRow(row, host), nil
}

func (r *resolver) resolveDCR(ctx context.Context, clientID string) (client, error) {
	c, err := verifyDCR(r.dcrKey, clientID)
	if err != nil {
		return client{}, err
	}
	if row, err := r.db.OAuthClientByClientID(ctx, clientID); err == nil {
		c.RowID = row.ID
	} else if !errors.Is(err, store.ErrNotFound) {
		return client{}, errUnknownClient
	}
	return c, nil
}

// resolve turns a client_id into a client, fetching and caching a CIMD
// document when the stored one is absent or past cache_until.
func (r *resolver) resolve(ctx context.Context, clientID string) (client, error) {
	if strings.HasPrefix(clientID, dcrPrefix) {
		return r.resolveDCR(ctx, clientID)
	}
	host, ok := cimdHost(clientID)
	if !ok {
		return client{}, errUnknownClient
	}
	now := r.now()
	row, err := r.db.OAuthClientByClientID(ctx, clientID)
	have := err == nil && row.Kind == "cimd"
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return client{}, errUnknownClient
	}
	if have && now.Before(row.CacheUntil) {
		return fromRow(row, host), nil
	}
	if r.negativeActive(clientID, now) {
		return r.stale(ctx, row, have, host)
	}
	if !r.budget.AllowN(now, 1) {
		return r.stale(ctx, row, have, host)
	}
	doc, ttl, err := r.fetchDoc(ctx, clientID, r.self)
	if err != nil {
		r.markNegative(clientID, now)
		return r.stale(ctx, row, have, host)
	}
	rec := store.OAuthClient{ClientID: clientID, Kind: "cimd", Name: displayName(doc.Name), RedirectURIs: doc.RedirectURIs,
		Metadata: doc.Raw, FetchedAt: now, CacheUntil: now.Add(ttl), CreatedAt: now}
	id, err := r.db.PutOAuthClient(ctx, rec)
	if err != nil {
		return client{}, errUnknownClient
	}
	rec.ID = id
	return fromRow(rec, host), nil
}

// stale serves the stored client when it has a live grant (stale-if-error),
// else refuses.
func (r *resolver) stale(ctx context.Context, row store.OAuthClient, have bool, host string) (client, error) {
	if have {
		if live, err := r.db.ClientHasLiveGrant(ctx, row.ID, r.now()); err == nil && live {
			return fromRow(row, host), nil
		}
	}
	return client{}, errUnknownClient
}

func (r *resolver) negativeActive(clientID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.negative[clientID]
	if ok && !now.Before(until) {
		delete(r.negative, clientID)
		return false
	}
	return ok
}

func (r *resolver) markNegative(clientID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.negative) > 1024 {
		for k, u := range r.negative {
			if !now.Before(u) {
				delete(r.negative, k)
			}
		}
	}
	if len(r.negative) <= 1024 {
		r.negative[clientID] = now.Add(negativeTTL)
	}
}
