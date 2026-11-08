package gateway

// Mounting internal/a2a on the gateway: the route prefix, the adapters from this package's store to the
// interfaces internal/a2a asks for, and the dispatcher that stands in until the managed-session path (P2)
// and the external-service client (P4) exist.
//
// The A2A routes sit OUTSIDE the Ratatoskr wire-version check: an outside A2A client does not know that
// header, and it speaks its own A2A-Version. Authentication is the a2a handler's.
//
// UNTESTED.

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/mngeow/ratatoskr/internal/a2a"
)

// a2aSessions adapts the sessions table to a2a.SessionDirectory.
type a2aSessions struct{ store *Store }

// Resolve finds live sessions by exact id or by friendly name.
func (a a2aSessions) Resolve(ctx context.Context, ref string) ([]a2a.SessionInfo, error) {
	// name@host narrows the friendly name to one host; otherwise an exact id or a bare name.
	if name, host, ok := parseAddress(ref); ok {
		return a.query(ctx, `SELECT id, host, name FROM sessions WHERE ended_at IS NULL AND name=? AND host=? ORDER BY host, name`, name, host)
	}
	return a.query(ctx, `SELECT id, host, name FROM sessions WHERE ended_at IS NULL AND (id=? OR name=?) ORDER BY host, name`, ref, ref)
}

func (a a2aSessions) query(ctx context.Context, query string, args ...any) ([]a2a.SessionInfo, error) {
	rows, err := a.store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []a2a.SessionInfo
	for rows.Next() {
		var s a2a.SessionInfo
		if err := rows.Scan(&s.ID, &s.Host, &s.Name); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// Current reports whether the session id is an open session of the host.
func (a a2aSessions) Current(ctx context.Context, host, sessionID string) (bool, error) {
	var one int
	err := a.store.db.QueryRowContext(ctx,
		`SELECT 1 FROM sessions WHERE id=? AND host=? AND ended_at IS NULL`, sessionID, host).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// a2aHosts adapts Store.Authenticate to a2a.HostAuthenticator.
type a2aHosts struct{ store *Store }

// Authenticate checks a host credential.
func (a a2aHosts) Authenticate(ctx context.Context, credential string) (string, bool, error) {
	host, err := a.store.Authenticate(ctx, credential)
	switch {
	case errors.Is(err, ErrUnauthorized):
		return "", false, nil
	case err != nil:
		return "", false, err
	}
	return host, true, nil
}

// newA2AHandler builds the A2A handler over a service.
func newA2AHandler(store *Store, baseURL string, service *a2a.Service) (*a2a.Handler, error) {
	return a2a.NewHandler(a2a.Config{
		Store:    store.A2AStore(),
		Service:  service,
		Sessions: a2aSessions{store: store},
		Hosts:    a2aHosts{store: store},
		BaseURL:  baseURL,
	})
}

// withA2A sends the A2A prefix to the A2A handler and everything else to next.
func withA2A(a *a2a.Handler, next http.Handler) http.Handler {
	if a == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, a2a.AgentsPath) {
			a.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
