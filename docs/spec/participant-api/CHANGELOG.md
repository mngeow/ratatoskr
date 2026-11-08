# Participant API changelog

One line per contract change. The major version is the path prefix (`/participant/v1`); the minor is the `X-Ratatoskr-Contract` response header. Within a major, changes only add.

- **1.0** (draft, 2026-11-08): first contract. Pair, discovery, register and end sessions, directory, send (six results), long-poll inbox, ack, status, read message. Implemented on `feat/adapter-integration`; nothing has been run.
