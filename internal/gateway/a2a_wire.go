package gateway

// Where internal/a2a meets the gateway: the SQLite handle, the admin logic, and the card fetcher.
//
// internal/a2a imports nothing from this package; this file hands it what it needs. The card dial rules
// (cardtarget.go, carddial.go) stay as they are and are reused here, not moved.
//
// UNTESTED.

import (
	"context"
	"time"

	"github.com/mngeow/ratatoskr/internal/a2a"
)

// A2AStore is the A2A participant and task store over this database.
func (s *Store) A2AStore() *a2a.SQLiteStore { return a2a.NewSQLiteStore(s.db) }

// newA2AAdmin builds the owner's enrolment logic over the store.
func newA2AAdmin(store *Store, now func() time.Time) *a2a.Admin {
	return &a2a.Admin{Store: store.A2AStore(), Fetcher: newA2ACardFetcher(store), Now: now, PurgeMessages: store.purgeMessages}
}

// purgeMessages deletes ledger messages and their events. It is used only by the owner's delete-tasks
// command, for the dispatch and wake messages of the tasks being deleted; the ledger is otherwise never
// pruned.
func (s *Store) purgeMessages(ctx context.Context, ids []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM message_events WHERE message_id=?`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM messages WHERE id=?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// newA2ACardFetcher returns the fetcher register uses: the outbound client over the dial-ruled factory.
// The card fetch carries no credential (its destination has no Auth).
func newA2ACardFetcher(store *Store) a2a.CardFetcher {
	return &a2a.Remote{Factory: a2aClientFactoryFor(store)}
}

// a2aFinisher is the callback Admin.Revoke uses to end one open task as FAILED with a reason. It needs
// only the store: ending a task dispatches nothing and notifies no one from the admin process.
func a2aFinisher(store *Store, now func() time.Time) func(context.Context, a2a.Task, string) error {
	service := &a2a.Service{Store: store.A2AStore(), Now: now}
	return service.Finish
}
