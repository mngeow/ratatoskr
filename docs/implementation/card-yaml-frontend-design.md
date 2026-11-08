# Card YAML correction: frontend design

Owner: frontend. Parent qitem-20261108004503-81b925286ff011c7. Authority: host-cards-contract.md section 13, including 13.4 and 13.6. No implementation in this correction yet; backend seam and monkey release pending.

## User outcome

The owner edits card.yaml in their own editor. Graph shows where it is, whether the file is valid, whether a last good card is being served, and the listener's independent state. Generate is a deliberate model call followed by review; only Accept writes the file and first makes card.yaml.bak.

## Screens and state

Keep Graph's existing node/list/detail layout, tabs and input isolation. Replace the local-card action/editor with a local status/detail surface: path; valid / needs description / error and line; last-good-served; distinct listener mark and listening ADDR / off / not bound REASON. Listening adds the public-port readability sentence. No endpoint-ready claim otherwise. Local C only works when listening, remote C continues to use the polled card URL.

R requests reload-now for the local file and refreshes Graph; no file opens and no PTY input. G requests a prepared generation plan, without running it. Both accept uppercase and lock modifiers. E is consumed with guidance to edit the shown file externally; the old field editor, publish/history/rollback, dirty working-copy state and not-configured draft screen are removed, with their obsolete tests.

Generation state is explicit: idle, preparing, confirm, running, review, accepting, error. One local operation in flight; no auto-run, auto-accept or replay. Confirmation scrolls the exact backend argv as a JSON array (argument boundaries preserved), exact payload and byte count, plus backend-provided skills/hooks/MCP loading caveats and Accept rewrite/backup notice. Source bytes are retained independently of cleaned display text; controls in argv/payload are shown as visible escapes. A deliberate Yes action (not a bare Enter defaulting to yes) starts the prepared plan. Running remains responsive to tab/detach navigation. Run completion gives a generated-fields-marked diff against the file and explicit Accept / Discard. Accept warns that card.yaml.bak is written first; backend token conflict or uncertain outcome shows an error and requires fresh status rather than retry.

Switching tabs preserves the review until explicit discard or window close. A new prepare invalidates the old plan. Stale results are guarded by operation generation and plan identity. Disconnect never implies success or cancellation of the daemon model operation. The view reports any backend ownership/lifetime semantics exactly; these remain a seam question.

## Code structure and monkey boundary

Backend owns public/shared types and handlers. Frontend adds/replaces windowclient local-card query and operation wrappers only after agreeing those types. A bounded monkey first version will own the replacement pure local status/generation rendering file, plus narrowly agreed controller integration if the seam is small; it must first confirm paths and scope, then wait for explicit go. Senior takes over state transitions, response identity, client wrappers, removal of old editor hooks, tests and integration. Lead must release the slot first; backend currently owns it.

Expected frontend paths: internal/windowclient/card.go; internal/tui/card_status.go and card_generation.go (new); existing graph.go, graph_render.go, model.go, daemon.go and card_detail.go integrations; delete card_editor.go and replace obsolete graph_editor_test.go cases while retaining graph/tab/raw-card tests. No protocol/wire/backend edits, dependency change, app launch, model call, commit or push.

## Seam requested from backend

View/reload must provide path, file validity/error line, last-good served flag and card, listener address/state/reason, inventory. Prepare must provide immutable plan identity/token, exact argv, exact payload bytes/byte count, environment-name/loading caveats, file rewrite/backup notice. Run must identify its plan and return validated draft/diff source with generated-field marks and file concurrency token. Accept must identify the candidate and file token, with explicit success/current status or error, never silent overwrite. Agree run timeout (120s plus transport margin), query slot usage and behavior on disconnect/cancel before client code.

## Verification and handoff

Write unrun tests for status/listener independence, URL gating, exact confirm data, explicit yes, no writes before Accept, stale results, conflict/uncertain outcomes, key isolation and all lock modifiers. Format/build/vet for Darwin and Linux, with and without rtktest, only without approval prompts. No tests, TUI, daemon or model calls under GR-3. Handoff lists changed/deleted files, actual checks and remaining unwritten work.

## Agreed backend lifetime, 00:47Z

Argv includes argv[0]; Binary is the resolved path, displayed separately. The UI never reconstructs the command. Run/status/cancel/accept echo the draft ID. One draft per window; new prepare replaces the previous draft. At most two runs daemon-wide, with busy refusals beyond the bound. Disconnect or close cancels a running draft and discards prepared/ready drafts, writing nothing. Prepared/ready drafts expire after fifteen minutes without an operation, returning cancelled with an expired reason. Run acknowledges immediately, then runs at most 120 seconds followed by two seconds kill grace; status is immediate and frontend's ordinary twelve-second request budget remains sufficient. Frontend polls status at most once per second with one request pending. Accept conflict requires a fresh prepare; retained ready data is for review/discard only. Backend errors never include rejected credential values.
