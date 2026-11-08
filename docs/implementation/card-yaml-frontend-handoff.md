# Card YAML frontend correction candidate

Owner frontend; next owner delivery lead. Parent qitem-20261108004503-81b925286ff011c7. Section 13 supersedes the old editor workflow. Candidate includes backend's concurrently landed YAML/draft implementation; nothing has been launched or executed under GR-3.

## Result

Local Graph status shows the card.yaml path, file validity and error line, current/retained last-good status, and independent prominent listener marks: + listening ADDR, - off, ! not bound REASON. File validity never implies listener availability. Listening includes the public-port readability notice. The local row carries a compact listener/file summary; selecting local status gives the complete scrollable view. C copies/opens the URL panel only while the local listener is listening; remote URL behavior continues to use the polled card URL.

R reloads now and refreshes Graph. The view also reads local file status every two seconds, one local request at a time. G prepares generation without running anything. Its confirmation shows resolved binary, exact JSON argv, complete payload with visible control spellings/trailing newline, byte counts, environment names and backend loading caveats. Y explicitly starts the prepared command; Enter does nothing. Run acknowledges immediately, followed by one status request at most per second on every tab. Ready shows field diffs and generated markers, backup and comment-rewrite notices. A explicitly Accepts using the original draft BaseToken; D discards and X cancels. No file write is requested before A. Conflict or unknown outcome blocks Accept/run; there is no automatic retry/rebase. Closing the window cancels/discards its draft on the daemon, as the screen explains. Prepared/ready expiry is fifteen minutes after the last operation.

Esc leaves generation review, preserving it when switching tabs; closing the window discards it. G inside generation explicitly prepares a new draft. R/E/C/G uppercase and lock modifiers remain safe; E only explains external file editing. All Graph paste and unrelated keys remain consumed, with no child draft/input/control action. F5/F6/F7/F10, tab strip geometry and remote grid/list/details retain their existing routes. No local editor, publish, history, rollback or not-configured drafting screen remains.

## Changed files

- New tui/card_status.go: monkey's pure first version, then senior refinements for exact escaping, truthful key instructions and retained-card wording when listener is off.
- New tui/card_generation.go: controller, result identity/generation checks, polling, read-only status and explicit operations.
- Deleted tui/card_editor.go.
- Updated tui/graph.go, graph_render.go, model.go, daemon.go, card_detail.go for the new view and removal of editor hooks.
- Updated windowclient/card.go to return CardFileView and wire.Result, preserve conflict views, validate matching draft identity and refuse removed operations.
- Replaced obsolete editor tests in tui/graph_editor_test.go while retaining graph/tab/raw-card cases; added tui/card_generation_test.go and windowclient/card_test.go.

Design and renderer brief: card-yaml-frontend-design.md and card-yaml-renderer-brief.md. Monkey source/delivery rows are closed and the slot was released to lead at 00:54Z.

## Checks actually completed

After backend finished its migration, all of these completed with exit 0, CGO disabled:

- Darwin arm64 and Linux amd64 go build -mod=readonly ./internal/attach ./internal/tui ./internal/windowclient.
- Both platforms go vet -mod=readonly for the same packages.
- Both platforms go vet -mod=readonly -tags=rtktest ./internal/pane ./internal/tui.
- Changed Go files formatted.

Earlier standalone native retries were blocked by Go cache permissions; no escalation was requested. An earlier wider check saw backend's migration in progress; the final dependency checks above passed. Tests are authored but UNRUN. No rtk/TUI/daemon/model/SSH/clipboard call, commit or push.

## Residual risks and unwritten proof

No runtime/visual proof exists. Actual model loading behavior and generation quality, asynchronous cancellation, expiry, token conflicts, backup writes and comment rewriting remain unexercised. Tests cover explicit run/accept gating, conflict preservation, stale response rejection, separate file/listener facts, exact escaping, result/draft identity and removal of old operations; they are not exhaustive interaction proof. Large confirmations/diffs are scrollable; Y/A are explicit actions and are not forced behind a scroll-to-end gate. Card status is refreshed every two seconds and may lag a file edit briefly. An unknown operation outcome requires explicit fresh preparation or reattachment; no mutation is replayed. Nothing requested remains intentionally unwritten in the frontend production path; execution proof remains prohibited by GR-3.
