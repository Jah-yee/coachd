package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/stefiix92/coachd/internal/auth"
	"github.com/stefiix92/coachd/internal/store"
)

const maxTTL = 365 * 24 * time.Hour

const tokenUsage = "usage: coachd token <create --name NAME --scope read|write [--ttl 90d] | list | revoke ID>\n"

// tokenMain opens the store (without requiring serve-only config) and runs a token subcommand.
func tokenMain(args []string) int {
	dir := os.Getenv("COACHD_DATA_DIR")
	if dir == "" {
		dir = "/data"
	}
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(dir, "coachd.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "coachd token: cannot open store: %v\n", err) // error carries a path, not a secret
		return 1
	}
	defer db.Close()
	return runToken(ctx, args, db, time.Now, os.Stdout, os.Stderr)
}

func runToken(ctx context.Context, args []string, db *store.DB, now func() time.Time, stdout, stderr io.Writer) int {
	return runTokenWith(ctx, args, db, db, now, stdout, stderr)
}

func runTokenWith(ctx context.Context, args []string, db *store.DB, aud auth.Auditor, now func() time.Time, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, tokenUsage)
		return 2
	}
	switch args[0] {
	case "create":
		return tokenCreate(ctx, args[1:], db, aud, now, stdout, stderr)
	case "list":
		return tokenList(ctx, db, stdout, stderr)
	case "revoke":
		return tokenRevoke(ctx, args[1:], db, aud, now, stderr)
	default:
		fmt.Fprint(stderr, tokenUsage)
		return 2
	}
}

func parseTTL(s string) (time.Duration, error) {
	var d time.Duration
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days > 3650 {
			return 0, errors.New("invalid --ttl")
		}
		d = time.Duration(days) * 24 * time.Hour
	} else {
		var err error
		if d, err = time.ParseDuration(s); err != nil {
			return 0, errors.New("invalid --ttl")
		}
	}
	if d <= 0 || d > maxTTL {
		return 0, errors.New("--ttl must be > 0 and at most 365d")
	}
	return d, nil
}

func validName(n string) bool {
	if len(n) < 1 || len(n) > 64 {
		return false
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return false
		}
	}
	return true
}

func tokenCreate(ctx context.Context, args []string, db *store.DB, aud auth.Auditor, now func() time.Time, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("token create", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "token name")
	scope := fs.String("scope", "", "read or write")
	ttlS := fs.String("ttl", "90d", "lifetime, e.g. 90d or 24h")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if !validName(*name) {
		fmt.Fprintln(stderr, "coachd token: --name is required, 1-64 characters, no control characters")
		return 2
	}
	var scopes []string
	switch *scope {
	case "read":
		scopes = []string{auth.ScopeRead}
	case "write":
		scopes = []string{auth.ScopeRead, auth.ScopeWrite}
	default:
		fmt.Fprintln(stderr, "coachd token: --scope must be read or write")
		return 2
	}
	ttl, err := parseTTL(*ttlS)
	if err != nil {
		fmt.Fprintln(stderr, "coachd token:", err)
		return 2
	}
	plain, hash, err := auth.GenerateToken()
	if err != nil {
		fmt.Fprintln(stderr, "coachd token: cannot generate token")
		return 1
	}
	idb := make([]byte, 8)
	if _, err := rand.Read(idb); err != nil {
		fmt.Fprintln(stderr, "coachd token: cannot generate id")
		return 1
	}
	id, t := hex.EncodeToString(idb), now().UTC()
	rec := store.TokenRecord{ID: id, Name: *name, Hash: hash, Scopes: scopes, CreatedAt: t, ExpiresAt: t.Add(ttl)}
	if err := db.CreateToken(ctx, rec); err != nil {
		fmt.Fprintln(stderr, "coachd token: cannot store token")
		return 1
	}
	if err := aud.AppendAudit(ctx, store.AuditEvent{At: t, Lane: "cli", Actor: "cli", Event: "token_created", Target: id, Outcome: "ok"}); err != nil {
		fmt.Fprintln(stderr, "coachd token: warning: audit write failed")
	}
	checkpoint(ctx, db, stderr)
	fmt.Fprintln(stdout, plain)
	fmt.Fprintf(stderr, "token shown once, store it now. id=%s expires=%s\n", id, rec.ExpiresAt.Format(time.RFC3339))
	return 0
}

func tokenList(ctx context.Context, db *store.DB, stdout, stderr io.Writer) int {
	list, err := db.ListTokens(ctx)
	if err != nil {
		fmt.Fprintln(stderr, "coachd token: cannot list tokens")
		return 1
	}
	ts := func(p *time.Time) string {
		if p == nil {
			return "-"
		}
		return p.UTC().Format(time.RFC3339)
	}
	w := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(w, "id\tname\tscopes\tcreated\texpires\tlast_used\trevoked")
	for _, r := range list {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", r.ID, r.Name, strings.Join(r.Scopes, ","),
			r.CreatedAt.UTC().Format(time.RFC3339), r.ExpiresAt.UTC().Format(time.RFC3339), ts(r.LastUsedAt), ts(r.RevokedAt))
	}
	w.Flush()
	return 0
}

func tokenRevoke(ctx context.Context, args []string, db *store.DB, aud auth.Auditor, now func() time.Time, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprint(stderr, tokenUsage)
		return 2
	}
	t := now().UTC()
	if err := db.RevokeToken(ctx, args[0], t); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			fmt.Fprintln(stderr, "coachd token: unknown token id")
		} else {
			fmt.Fprintln(stderr, "coachd token: cannot revoke token")
		}
		return 1
	}
	if err := aud.AppendAudit(ctx, store.AuditEvent{At: t, Lane: "cli", Actor: "cli", Event: "token_revoked", Target: args[0], Outcome: "ok"}); err != nil {
		fmt.Fprintln(stderr, "coachd token: warning: audit write failed")
	}
	checkpoint(ctx, db, stderr)
	return 0
}

// checkpoint makes the CLI's commits durable before it exits: under
// synchronous=NORMAL a power cut could otherwise roll back the last commit,
// e.g. undo a revocation. A failure only warns: the change is committed.
func checkpoint(ctx context.Context, db *store.DB, stderr io.Writer) {
	if err := db.Checkpoint(ctx); err != nil {
		fmt.Fprintln(stderr, "coachd token: warning: committed, but the WAL checkpoint did not complete; a power cut before the next checkpoint could undo it")
	}
}
