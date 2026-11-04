package store

import "errors"

var (
	// ErrAlreadyRunning means another ratatoskr holds the per-machine lock.
	ErrAlreadyRunning = errors.New("store: another ratatoskr is already running on this machine")
	// ErrNewerSchema means the database was written by a newer binary and is left untouched.
	ErrNewerSchema = errors.New("store: database is newer than this binary")
	// ErrUnavailable means a write failed. Every later write fails too, because an
	// unrecorded write is not allowed (design section 11).
	ErrUnavailable = errors.New("store: storage unavailable")
	// ErrClosed means the store has been closed.
	ErrClosed = errors.New("store: closed")
	// ErrStateChanged means the message was not in the state the caller expected.
	ErrStateChanged = errors.New("store: message not in expected state")
	// ErrIllegalTransition means from -> to is not an edge of the message lifecycle.
	ErrIllegalTransition = errors.New("store: illegal message transition")
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("store: not found")
)
