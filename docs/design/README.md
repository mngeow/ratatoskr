# Design index

Maintained by architect. A document here is not evidence of implemented behavior unless it says so.

| Document | Status | Summary |
| --- | --- | --- |
| [Ground rules](ground-rules.md) | In force | Project constraints set by the user: Go; Bubble Tea for TUIs; no testing or pushing until instructed |
| [Two-Pane Bridge](mvp-two-pane-bridge.html) | v0.17, the user reports mouse focus, Ctrl+C, automatic Codex startup and messages both ways working by hand on macOS; the team has run nothing, and no automated test has run | First version: Bubble Tea TUI running Claude Code and Codex side by side, with messaging between them and SQLite-backed message history. Cross-machine messaging is the planned next step |
| [Terminal Approach Proposal](terminal-approach-proposal.html) | Not adopted; kept as history | Alternative using tmux panes. The user chose to keep panes embedded in Bubble Tea |
| [ADR-0001: MCP agent messaging](../adr/0001-mcp-agent-messaging/index.html) | Accepted | Why agents send through an MCP tool and receive through a gated paste, and how that works in the code. Architecture decisions live in `docs/adr/`; the [README](../../README.md#architecture-decisions) explains the convention |
| [ADR-0002: Cross-machine messaging](../adr/0002-cross-machine-messaging/index.html) | Proposed, awaiting the user's decision; full gateway message storage is decided, and the overall design isn't selected or built | Options for connecting agents on different machines, a proposed central gateway with separately deployable runtimes, drawbacks and open questions |
| [Outstanding issues](../implementation/outstanding-issues.md) | Maintained by lead | Confirmed defects, unverified risks and deferred features |

Candidate report: [candidate-report.md](../implementation/candidate-report.md). It's the first candidate's report, kept as history. Current status is in Outstanding issues.
