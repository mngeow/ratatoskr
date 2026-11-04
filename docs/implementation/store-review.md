# Static review: `internal/store`

Reviewer: `delivery.lead`, 2026-11-04, at the operator's request. Reviewed by reading the non-test source and migration against design sections 8-10. **Nothing was run** (GR-3). Test files were not reviewed. Statements about SQLite and `modernc.org/sqlite` behaviour come from memory or from the code's stated assumptions, and are unverified.

**Provenance:** backend wrote this package directly, without a monkey first version. That was a process deviation (see the candidate report). The operator's decision is to keep it and not rewrite it for process.

## Verdict

Matches the design's scope and contracts as far as reading can show. No blocking findings. No scope creep: there is no ownership, claims or resend path.

| Design requirement | Finding |
| --- | --- |
| Tables (section 9) | Match. Tokens have no column. |
| Lifecycle (Figure 6) | `legal` matches every listed edge. `unconfirmed` → `accepted` is the only edge out of a final state. |
| Message and event commit together, only from the expected state | `Transition` and `InsertMessage` do this in one transaction. |
| Commit before claim or write (invariant 8) | The API supports it. The router must honour it. |
| One writer; WAL; `synchronous=FULL` | Yes. |
| Recovery reasons | Match the table. `FinishRun` and `Recover` share one path. |
| Never resend (invariant 9) | No path leaves `interrupted`. |
| Lock before touching the database (check 11) | Yes. |
| Failed write stops delivery (check 12) | The failure latches `ErrUnavailable` for all later writes. |
| Retention, schema version, newer database refused | Present. |
| Test seam absent from release build | `FailNextCommit` exists only under `rtktest`. |

## Findings

1. **Router requirement, most important: `UserPromptSubmit` can overtake `pasted`.** The harness can fire the hook before the delivery goroutine reports that `PasteSubmit` returned. The store correctly has no `delivering` → `accepted` edge, so an unhandled early hook would fail with `ErrIllegalTransition`, and the message would later become `unconfirmed` even though it was accepted. The router must hold a confirmation that arrives during `delivering` and apply it straight after `pasted` commits.
2. **Router requirement: a user's prompt can be credited to a message.** A hook from the user's own submit can land just after a paste. Where the payload carries prompt text, credit only a prompt containing the message ID. Without text, `accepted` remains the design's weaker claim.
3. **Low: an existing database file keeps its old mode.** `O_CREATE` with 0600 applies only on creation. Design section 9 states 0600; chmod it, as the code already does for the directory.
4. **Low: the `VACUUM INTO` backup may not be 0600.** It lies inside the 0700 directory, so exposure is limited. Set its mode after creating it. Backups also fall outside retention, so old bodies outlive 30 days once a second migration exists. Nothing triggers this yet because there is only one migration.
5. **Low: any non-domain error latches `ErrUnavailable`,** including a caller bug such as a duplicate key. This fails safe, and the cause is in the first error message. Make sure the router's banner surfaces that first error.

## Notes for lead's own integration

- Call `Recover` **before** `StartRun`. It closes every unclosed run, so calling it later would mark the live run `crashed`.
- The router enforces the 64 KiB body cap; the store doesn't.
- `app` sets the 30-day cutoff passed to `Retain`.
- README: to erase everything, delete the whole directory, not only the `.db` file. WAL files and backups sit beside it.
