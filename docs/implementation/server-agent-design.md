# internal/server and internal/agent design (draft)

Owner: `engineering.backend`. Status: **design only; no code, nothing run** (GR-3). Everything harness-specific below comes from `--help` and vendor docs and is **untested**. Monkey briefs for these packages follow brief 1 in `router-design.md` and are not issued yet.

## Finding that changes the session directory

Codex requires the user to review and trust the exact hook definition, and skips new or changed hooks until trusted (hooks docs, "Review and trust"). A hook command containing a per-run random session path would change every launch and need review every launch. Proposal: **hook commands are identical across runs.** They contain only the absolute path of `rtk`, `hook`, `--agent <name>`, `--event <Kind>`, `--session-dir <state dir>/session` (absolute, shell-quoted if it has spaces; `RunHook` never derives the directory itself, because env reaching hooks is unverified). The server URL and token live in that directory (mode 0700), which is safe because the per-machine lock allows one ratatoskr per machine. `app` empties it at startup and removes it on exit.

- This refines design invariant 5 ("a private session directory ... removed on exit"): the directory is private and removed on exit, but at a fixed path instead of a random one.
- The trust record's key (command string only, or also the `-c` source) is unverified. If trust is still requested every run, that is a README requirement, not something to bypass: `--dangerously-bypass-hook-trust` is not used.
- The absolute binary path changes when `rtk` is rebuilt; expect one re-review then.

Needs lead and architect's OK because it touches an invariant's wording.

## internal/server

```go
type Inbox interface { Send(...); HookEvent(...) } // as interfaces.md; defined here

type Server struct{ ... }
func New(in Inbox, opts Options) (*Server, error)   // listens on 127.0.0.1:0 immediately
func (s *Server) Addr() string                       // "127.0.0.1:<port>"
func (s *Server) AddAgent(id protocol.AgentID, name string, peers []string) (token string)
func (s *Server) Serve() error                       // blocks; returns after Shutdown
func (s *Server) Shutdown(ctx context.Context) error
func RunHook(args []string, stdin io.Reader) error   // always nil
```

- **Listener:** `net.Listen("tcp", "127.0.0.1:0")`. Refuses anything that is not a loopback address. `http.Server` with `ReadHeaderTimeout`, `MaxHeaderBytes`, and `http.MaxBytesReader` (1 MiB) on every body.
- **Tokens:** 32 bytes from `crypto/rand`, hex. Stored as SHA-256 in `map[[32]byte]agentEntry`; lookup is by hash of the presented token, so comparison time does not depend on a prefix match. Never logged, never written to the store.
- **Per request checks, before anything else:** `Host` header must equal `Addr()` (DNS-rebinding guard); a request with an `Origin` header is refused; bearer token required, otherwise 401 with no body detail. The sender is the agent the token maps to (invariant 4).
- **MCP:** `mcp.NewStreamableHTTPHandler` from `github.com/modelcontextprotocol/go-sdk` v1.8.0 with `Stateless: true` and `JSONResponse: true`, so each request is independent and identity comes from that request's token. `getServer(req)` returns a per-agent `*mcp.Server` (built once per agent in `AddAgent`) carrying one tool, `send_message(to, body, reply_to?)`, and `Instructions` saying who the agent is, who its peers are, and that normal replies are not sent to peers. Tool errors use the section 8 reason strings. Source read: SDK `mcp/streamable.go`, `mcp/server.go`.
- **Hook endpoint:** `POST /hook`, same checks. Body: `{"event": "<Kind>", "payload": <raw hook JSON from the harness, optional>}`. The server extracts `payload.session_id`, `payload.prompt` and, for SessionStart, `payload.source`, each only if it is a string, into `protocol.HookEvent` (needs new fields `SessionID`, `Source`). Unknown event names are accepted and ignored (forward compatibility); the response is always `204`.
- **`RunHook`:** parses `--agent`, `--event`, `--session-dir`; reads `<session-dir>/hook-<agent>.json` (`{"url","token"}`, mode 0600). It forwards the harness's stdin JSON as `payload` and **does no orphan check itself**: the check cannot rely on anything in the session directory (design v0.8), so it lives in the router, which compares `payload.session_id` with the ID bound to the agent (router-design.md, "Session binding"). Writes nothing to stdout, returns nil on every failure (invariant 2), at most one short stderr line.
- **Shutdown:** `Shutdown` stops accepting, waits for in-flight handlers up to ctx, then closes. Handlers pass the request context into `Inbox`.

Tests (written by me after review, unrun): `httptest` against the handler with a fake `Inbox`: missing/wrong token ⇒ 401; wrong Host ⇒ refused; Origin present ⇒ refused; token for agent A cannot send as B; body cap; hook JSON with and without `prompt`; `RunHook` returns nil and prints nothing when the server is absent, slow, returns 500, or the file is missing; a table that every router error maps to its reason string.

## internal/agent

```go
type Params struct {
    ID protocol.AgentID; DisplayName string
    Harness string              // "claude" | "codex"
    Dir string                  // working directory for the harness
    RtkPath string              // absolute path of the running binary (os.Executable)
    SessionDir string           // <state dir>/session
    ServerAddr, Token string
    BaseEnv []string            // typically os.Environ(); filtered here
}
func Build(p Params) (protocol.LaunchSpec, error)
```

- `PrepareSessionDir(dir) error` creates `dir` (0700) and empties it. `app` calls it **only after `store.Open` has taken the lock**, so it can never wipe a live run's files.
- **Claude session binding:** `Build` generates a UUID (`github.com/google/uuid`) and passes `--session-id <uuid>`; it is returned in `LaunchSpec.SessionID` (new protocol field) so the router binds at registration. **Codex:** no flag found in `codex --help` or the hooks docs; `LaunchSpec.SessionID` stays empty and the router binds from the first `SessionStart` with source `startup`.
- Writes only inside `SessionDir` (0700, files 0600), never `~/.claude` or `~/.codex` (invariant 5). Resolves the harness binary with `exec.LookPath` and puts the absolute path in `Argv[0]`; not found ⇒ error, which `app` turns into `PaneFailed`.
- **Env is complete:** `BaseEnv` minus every `TMUX*`, `OPENRIG_*`, `RIG_*`, and the host terminal's identity (`TERM`, `COLORTERM`, `TERM_PROGRAM*`, `KITTY_*`, `WEZTERM_*`, `ITERM_*`, `VTE_VERSION`, `KONSOLE_*`, `WT_*`, `ALACRITTY_*`), plus `RATATOSKR_TOKEN` (Codex). The child is told it runs in a terminal that `x/vt` emulates: `TERM` and `COLORTERM` are set by exactly one function, `childTerminalEnv()`, whose values come from frontend, who read `x/vt`. Login and locale variables are kept.
- **Claude** (one function, `claudeSpec`, marked UNTESTED, sources `claude --help` 2.1.311 and the hooks docs): `claude --mcp-config <session>/claude-mcp.json --settings <session>/claude-settings.json --allowedTools mcp__ratatoskr__send_message`. `claude-mcp.json`: an `http` server `ratatoskr` with url and an `Authorization: Bearer` header. `claude-settings.json`: `hooks` entries for `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `PostToolUse`, each a `command` hook running the fixed `rtk hook` line. We do not pass `--strict-mcp-config`, so the user's own MCP servers keep working.
- **Codex** (`codexSpec`, UNTESTED, sources `codex --help` 0.166.0 and the Codex hooks/MCP docs): `-c 'mcp_servers.ratatoskr.url="http://127.0.0.1:<port>/mcp"'`, `-c 'mcp_servers.ratatoskr.bearer_token_env_var="RATATOSKR_TOKEN"'`, `-c 'mcp_servers.ratatoskr.tools.send_message.approval_mode="approve"'`, and one `-c 'hooks.<Event>=[{hooks=[{type="command",command="...",timeout=...}]}]'` per event. **The inline-TOML shape of the hooks override is the least certain thing here**: the docs show hooks only as `[[hooks.X]]` tables in files. If it fails, the fallback that keeps invariant 5 is a project-level `<Dir>/.codex/hooks.json`, which is a file in the user's project and needs a design decision, not a silent choice.
- Sandbox: no sandbox or approval flag is added or loosened. DevOps' docs-only report says MCP is outside the command sandbox; command-hook networking is unverified, so a hook that cannot reach loopback would look like "no hook events" and end in `unknown` (held, F4).
- `Version(ctx, harness) string` runs `<harness> --version` for the `agents.harness_version` column. It runs at app start in production only.

Tests (unrun): table tests on the pure parts: env filtering (no `TMUX`, `OPENRIG_*`, `RIG_*`, host terminal variables; `TERM`/`COLORTERM` equal `childTerminalEnv()`), exact Argv, JSON/TOML text, file modes in a temp dir, `Build` writes nothing outside `SessionDir`, hook command strings identical across two `Build` calls with different tokens and ports; Claude `Argv` carries `--session-id` equal to `LaunchSpec.SessionID`, which is a valid UUID and differs between calls; Codex `SessionID` is empty; `PrepareSessionDir` empties and sets 0700.

## What stays unknown until testing is allowed

Codex `-c` hooks shape and trust key; whether hook subprocesses inherit `RATATOSKR_TOKEN` (not needed, by design); whether hooks fire after an interrupt; whether the prompt text is in the payload; paste at a permission prompt. Each has exactly one place to change.

---

## Monkey brief 3: internal/server and RunHook (draft)

**Supervisor: `engineering.backend`. Confirm with me before coding.** Read this file's "internal/server" section, `docs/implementation/interfaces.md` ("server → router", hook paragraphs), `internal/protocol/protocol.go`, and the router's `send.go` for the refusal reasons.

**Rules.** GR-1 Go only; standard library, `internal/protocol` and `github.com/modelcontextprotocol/go-sdk/mcp` (v1.8.0, already in `go.mod`; read its source in the module cache, including `mcp/streamable.go` and `AddTool` in `mcp/server.go`). No other dependency; no `go.mod`/`go.sum` edit. Do **not** import `internal/router`. **GR-3: run nothing** (no `go test`, no binary, no `rtk`, no harness, no SSH, no push, no commit); only `gofmt`, `go vet`, `go build`; write tests but do not run them; never write "works"/"verified"; write "untested". **Allowed paths:** `internal/server/server.go`, `mcp.go`, `hook.go`, `runhook.go`, `hookfile.go`, and `*_test.go` in that directory. Touch nothing else.

**API (exact names):**

```go
type Inbox interface {
    Send(ctx context.Context, from protocol.AgentID, to, body, replyTo string) (id string, err error)
    HookEvent(ctx context.Context, from protocol.AgentID, ev protocol.HookEvent)
}
type Options struct { ReadHeaderTimeout time.Duration; MaxBodyBytes int64 } // defaults 5s, 1<<20; each default commented "UNTESTED guess"
func New(in Inbox, opts Options) (*Server, error)   // net.Listen("tcp", "127.0.0.1:0") immediately
func (s *Server) Addr() string                       // "127.0.0.1:<port>"
func (s *Server) AddAgent(id protocol.AgentID, name string, peers []string) (token string)
func (s *Server) Serve() error                       // blocks; returns nil after Shutdown
func (s *Server) Shutdown(ctx context.Context) error
func WriteHookFile(sessionDir, agentName, serverAddr, token string) error // mode 0600; the only writer of hook-<agent>.json
func RunHook(args []string, stdin io.Reader) error   // ALWAYS returns nil
```

**Behaviour:**

1. **Auth, in this order, for every request** (`/mcp` and `/hook`): `Host` header must equal `Addr()` exactly, else 403; any `Origin` header ⇒ 403; `Authorization: Bearer <token>` required, else 401 with an empty body and `WWW-Authenticate: Bearer`; the token is looked up by its SHA-256 in a `map[[32]byte]entry` guarded by an `RWMutex`; unknown ⇒ 401, no detail. The agent resolved from the token goes into the request context. Tokens: 32 bytes from `crypto/rand`, hex. Never log or return a token. `AddAgent` may be called before or after `Serve`.
2. **Routes:** `/mcp` ⇒ the SDK handler; `POST /hook` ⇒ hook handler; anything else ⇒ 404. Bound every body with `http.MaxBytesReader`. `http.Server` with `ReadHeaderTimeout`.
3. **MCP:** `mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})` where `getServer` returns the per-agent `*mcp.Server` built in `AddAgent`. Each has one tool `send_message` registered with `mcp.AddTool` and typed args `{To string "to"; Body string "body"; ReplyTo string "reply_to,omitempty"}` and result `{ID string "id"; Status string "status"}` (Status is always `"queued"`). Server `Instructions` exactly: `You are <name> on a local ratatoskr network. Your peers: <peers joined by ", ">. Use send_message(to, body, reply_to?) to message a peer. Your normal replies are not sent to peers.` The handler calls `Inbox.Send(ctx, agentFromContext, args.To, args.Body, args.ReplyTo)`. The sender is **never** read from arguments (invariant 4). On error: if `errors.As` finds an error with a `SendReason() string` method, return an error whose text is exactly that reason (for example `recipient_offline`); any other error returns exactly `internal_error` (no detail). The SDK turns a returned error into a tool result with `IsError`.
4. **Hook endpoint:** body `{"event": string, "payload": object|absent}`. Accept events `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `PostToolUse`; any other event name is accepted and ignored. From `payload` take `session_id`, `prompt` and `source`, each only if it is a JSON string, into `protocol.HookEvent{Kind, Prompt, SessionID, Source, At: time.Now()}`. Call `Inbox.HookEvent` and answer `204` with no body for every authenticated request, including malformed JSON (then with an empty event, which is ignored). It never returns anything that could be mistaken for a decision.
5. **`WriteHookFile`:** writes `<sessionDir>/hook-<agentName>.json` containing `{"url":"http://<serverAddr>","token":"<token>"}` with mode 0600 (create with 0600, then `Chmod`), atomically via a temp file and rename. `agentName` must match `^[a-z0-9_-]{1,32}$`, else error.
6. **`RunHook(args, stdin)`:** parse `--agent`, `--event`, `--session-dir` with a `flag.FlagSet` (`ContinueOnError`, output discarded). Read at most 1 MiB of stdin; embed it as `payload` only if it is valid JSON. Validate the agent name with the same pattern (path-traversal guard). Read `hook-<agent>.json` from `--session-dir`. **The URL must parse as `http` with host `127.0.0.1` or `[::1]`; anything else is dropped, never contacted.** POST to `<url>/hook` with `Authorization: Bearer <token>` and an `http.Client{Timeout: 2s}` (UNTESTED guess). **It writes nothing to stdout, returns nil on every path (missing file, bad flags, bad URL, timeout, non-2xx), and prints at most one short line to stderr that never contains the token.** A `PermissionRequest` hook that printed a decision would break invariant 2.

**Tests to write (unrun), table-driven, calling the unexported `http.Handler` with `httptest.NewRequest` and `req.Host = s.Addr()` (no network needed), with a fake `Inbox` that records calls:** missing/wrong token ⇒ 401; wrong `Host` ⇒ 403; `Origin` present ⇒ 403; token A cannot send as B (the `from` the fake sees is A's); sender in arguments is ignored; every refusal reason maps to its exact text and an unrelated error maps to `internal_error` with no detail; over-size body rejected; hook JSON with and without `prompt`, with a non-string `prompt`, malformed JSON ⇒ 204 and an ignored event; unknown event name ignored; `WriteHookFile` mode 0600 and name validation; `RunHook`: returns nil and writes nothing to a captured stdout when the file is missing, flags are bad, the URL is non-loopback, the server returns 500, and the server is slow (use a local `httptest.Server`), and never contacts a non-loopback URL. One end-to-end style test that connects the SDK's own client (`mcp.NewClient` with `StreamableClientTransport`) to an `httptest.Server` wrapping the handler and lists/calls `send_message`.

**Done when:** `gofmt -l internal/server` prints nothing; `go vet ./internal/server` and `go build ./internal/server` succeed. Report back to me only, with files written, numbered ambiguities, and anything you changed.

---

## Monkey brief 4: internal/agent (draft, not yet issued)

**Supervisor: `engineering.backend`. Confirm with me before coding.** Read this file's "internal/agent" section, `internal/protocol/protocol.go` (`LaunchSpec`), `internal/server/hookfile.go` (the agent-name pattern; `server.WriteHookFile` is called by `app`, **not** by this package), and `claude --help` / `codex --help` output (read-only).

**Rules.** GR-1 Go only; standard library, `internal/protocol` and `github.com/google/uuid` (already in `go.mod`). No other dependency; no `go.mod`/`go.sum` edit. **GR-3: run nothing** (no `go test`, no built binary, no harness, no `claude`/`codex` execution by you; `gofmt`, `go vet`, `go build` only). Tests are written, never run. Never write "works"/"verified". **Allowed paths:** `internal/agent/*.go` only. Do not import `internal/server` or `internal/router`.

**Terminal coupling to isolate:** `childTerminalEnv()` is the only function that names terminal values (`TERM=xterm-256color`, `COLORTERM=truecolor`, from frontend's read of `x/vt`). Architect is reassessing the pane approach; this function and the strip list are the only things that may change.

**API (exact names):**

```go
type Params struct {
    ID protocol.AgentID; DisplayName string
    Harness string          // "claude" | "codex"; anything else is an error
    Binary string           // optional absolute path; default exec.LookPath(Harness)
    Dir string              // working directory for the harness
    RtkPath string          // absolute path of the running rtk binary
    SessionDir string       // absolute, .../session
    ServerAddr, Token string
    BaseEnv []string        // typically os.Environ()
}
func Build(p Params) (protocol.LaunchSpec, error)
func PrepareSessionDir(dir string) error
func Version(ctx context.Context, harness string) string
func HookCommand(rtkPath, agentName, event, sessionDir string) string
```

**Behaviour:**

1. **Validation:** empty ID, name, Dir, RtkPath, SessionDir, ServerAddr or Token ⇒ error. `DisplayName` must match `^[a-z0-9_-]{1,32}$` (it is also the hook-file name). `RtkPath` and `SessionDir` must be absolute. The harness binary not found ⇒ error (the caller turns it into `PaneFailed`).
2. **`HookCommand`:** `<rtkPath> hook --agent <name> --event <event> --session-dir <sessionDir>`, each argument shell-quoted: left bare if it matches `^[A-Za-z0-9_./:@%+=,-]+$`, otherwise wrapped in single quotes with every `'` written as `'\''`. Deterministic: the same inputs give the same string every run (Codex asks the user to re-trust a changed hook definition). No token, port or run ID appears in it.
3. **Events:** `SessionStart`, `UserPromptSubmit`, `Stop`, `PermissionRequest`, `PostToolUse`. One exported `var HookEvents = []string{...}` in that order. No `matcher` is set.
4. **Claude (`claudeSpec`, comment "UNTESTED: shapes from the Claude Code mcp and hooks docs and claude --help 2.1.311"):**
   - Session ID: `uuid.NewString()`, returned in `LaunchSpec.SessionID`.
   - Writes `<SessionDir>/claude-mcp.json` (mode 0600): `{"mcpServers":{"ratatoskr":{"type":"http","url":"http://<ServerAddr>/mcp","headers":{"Authorization":"Bearer <Token>"}}}}`. The token is written literally: whether `${VAR}` expands in a `--mcp-config` file is not stated in the docs.
   - Writes `<SessionDir>/claude-settings.json` (mode 0600), no token in it: `{"hooks":{"<Event>":[{"hooks":[{"type":"command","command":"<HookCommand>","timeout":10}]}]}}` for each event.
   - `Argv`: `[binary, "--session-id", <uuid>, "--mcp-config", <mcp file>, "--settings", <settings file>, "--allowedTools", "mcp__ratatoskr__send_message"]` (`--allowedTools` last because it is variadic).
5. **Codex (`codexSpec`, comment "UNTESTED: nested -c shapes are not shown in the docs"):** `LaunchSpec.SessionID` stays empty. `Argv`: `[binary]` followed by pairs `"-c", value` for, in this order: `mcp_servers.ratatoskr.url="http://<ServerAddr>/mcp"`, `mcp_servers.ratatoskr.bearer_token_env_var="RATATOSKR_TOKEN"`, `mcp_servers.ratatoskr.tools.send_message.approval_mode="approve"`, then per event `hooks.<Event>=[{hooks=[{type="command",command=<TOML string>,timeout=10}]}]`. `<TOML string>` is a TOML basic string: wrapped in `"`, with `\` and `"` backslash-escaped and control characters escaped as `\uXXXX`. **No sandbox, approval or hook-trust flag is ever added, and `--dangerously-*` flags are never used.** Writes no file.
6. **Env (complete, not inherited):** from `BaseEnv` drop every variable whose name is, or starts with: `TMUX`, `OPENRIG_`, `RIG_`, `RATATOSKR_`, `TERM`, `COLORTERM`, `COLUMNS`, `LINES`, `TERM_PROGRAM`, `TERM_SESSION_ID`, `VTE_VERSION`, `WINDOWID`, `WT_SESSION`, `KITTY_`, `WEZTERM_`, `ITERM_`, `KONSOLE_`, `ALACRITTY_` (exact names where no underscore prefix is meant: `TERM`, `COLORTERM`, `COLUMNS`, `LINES`, `VTE_VERSION`, `WINDOWID`, `WT_SESSION`, `TERM_SESSION_ID`). Keep everything else, including `PATH`, `HOME`, locale and login variables. Then append `childTerminalEnv()` and, for Codex only, `RATATOSKR_TOKEN=<Token>`. The result goes in `LaunchSpec.Env`; `Argv` and `Dir` are set; `ID`, `DisplayName`, `Harness` are copied.
7. **`PrepareSessionDir(dir)`:** `dir` must be absolute and its base name must be exactly `session` (a guard so it can never empty some other directory); creates it with 0700, `Chmod` 0700, then removes every entry inside (not the directory itself). Called by `app` only after the store lock is held.
8. **`Version(ctx, harness)`:** resolves the harness binary with `exec.LookPath` ("claude" or "codex"; any other name returns "") and runs `<binary> --version` with a 5 s timeout, returns the first non-empty line trimmed to 64 bytes, or `""` on any error. Production use only (`app` at startup).
9. Files are written with a temp file and rename, mode 0600 from creation. Nothing is written outside `SessionDir`.

**Tests (unrun):** validation table; `HookCommand` quoting table including a path with a space and a single quote, and equality across two calls with different tokens and ports; Claude `Argv` exact, UUID valid and different per call, JSON files parse and have mode 0600, no token in the settings file; Codex `Argv` exact, TOML string escaping table, `SessionID` empty, no file written; env table (every stripped name gone, `PATH` kept, `TERM`/`COLORTERM` equal `childTerminalEnv()`, `RATATOSKR_TOKEN` only for Codex, no `TMUX*`/`OPENRIG_*`/`RIG_*`); `PrepareSessionDir` refuses a relative path and a base name other than `session`, empties a populated directory, sets 0700.

**Done when:** `gofmt -l internal/agent` prints nothing; `go vet ./internal/agent` and `go build ./internal/agent` succeed. Report to me only.
