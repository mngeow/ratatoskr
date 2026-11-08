# ADR0005 pure render helpers — prepared, slot release required

Parent eef772cd. Frontend senior-owned integration already authorised separately.
Do NOT dispatch before lead releases monkey after backend scanner.
Read host-cards-contract §§2,5,6,8a,10, accepted graph-frontend-design.md changes,
actual protocol/card.go graph.go (LocationVerified rename), theme.go sanitization.

Only new internal/tui/card_detail.go and graph_nodes.go. No other files/tests,
model/keys/editor/layout/wire/deps/docs/Git/owner changes. UNTESTED under GR-3.

Implement package-private pure functions, no Model receiver/IO/goroutines/clock:

1. cardSectionLines(card *protocol.HostCard, poll *protocol.GraphCard,
   local *protocol.CardView, section int, width int) []string.
   Sections0..4 Card, Skills, Security, Capabilities, Raw JSON. Caller owns scroll.
   Return plain lines HARD-wrapped by cell width; clean each field with cleanUISpan
   BEFORE wrapping. No ANSI styling, no clipping away final content; width<=0 nil.
   Card: name/description/revision, interface binding/version/URL, poll result/
   source and restored text; omitted body explicit. Skills: owner DECLARED entries,
   tags/descriptions, extension evidence found hash/scope/harness/time. With local
   view, found inventory/evidence separately, not-scanned list + notes/truncated;
   LocationVerified says ONLY documented scan location, never skill capability.
   No allowed/verified skill claim. Explicit project/plugin/managed 'not scanned'.
   Security: unauthenticated read-only card; transport parsed from card URL's
   scheme only; no securitySchemes, secure, verified or reach claims.
   Capabilities: actual booleans/extension metadata, explain discovery only,
   never infer usability/tasks from skills.
   Raw: 'Raw JSON - what the gateway kept' for polled card, show UnknownFields
   count>0, card_omitted notice; local 'Raw JSON - local card'. MarshalIndent the
   parsed typed card, split lines FIRST, clean each then hard-wrap. Preserve JSON
   escape spellings e.g. \\u001b and final lines of64KiB cards. No nil dereference.
   Unsupported section visible error, nil card readable absence with context.

2. graphCardMark(card protocol.GraphCard, utf8 bool) string:
   fresh='● fresh'/ASCII'o fresh'; stale='… stale'/'. stale'; offline='○ offline'/
   'o offline'; none='? no card'; rejected='✗ rejected'/'X rejected'. Unknown state
   cleaned literal plus '(unknown to this build)'; no idle/active/alive. Restored
   adds 'from the database, not yet polled'. Rejected explains newest rejected,
   last good kept in detail, not a claim last card was accepted now.
3. graphNodeText(host protocol.GraphHost, age string, utf8 bool) []string:
   three plain logical lines: cleaned host name, card mark, source+provided age.
   No connection merged with mark. Caller owns crop/geometry/style.
4. graphLegendLines() []string: concise plain legend: card mark=card poll only;
   connection separate gateway-stream; messages_seen means sends the gateway
   decided, host_stored subset reached a host, source gateway-ledger, never
   delivered; poll spokes source gateway-poll. No inference or network calls.

These signatures are the integration contract; ask before changing. Use existing
ansi.Hardwrap + stdlib json/url, no dependencies. A card's URL comes only from
SupportedInterfaces, not local listener fallback. Every displayed value cleaned.
Do not build editor/clipboards, do not invoke provider/model/draft operation.

Confirm brief and ambiguities, await explicit go. gofmt your two files, Darwin
build/vet -mod=readonly ./internal/tui only if no native prompt; skip/report if
senior's in-flight integration makes package temporarily incomplete. No tests,
app/rtk/TUI/browser/harness/SSH/daemon/model/probes, no commits/push. Stop at
bounded draft, return exact checks/files/gaps through queue; senior takes over.
