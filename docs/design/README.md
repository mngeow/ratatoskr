# Design index

Maintained by architect. A document here is not evidence of implemented behavior unless it says so.

| Document | Status | Summary |
| --- | --- | --- |
| [Ground rules](ground-rules.md) | In force | Project constraints set by the user: Go; Bubble Tea for TUIs; no testing or pushing until instructed |
| [Two-Pane Bridge](mvp-two-pane-bridge.html) | v0.17, the user reports mouse focus, Ctrl+C, automatic Codex startup and messages both ways working by hand on macOS; the team has run nothing, and no automated test has run | First version: Bubble Tea TUI running Claude Code and Codex side by side, with messaging between them and SQLite-backed message history. Cross-machine messaging is the planned next step |
| [Terminal Approach Proposal](terminal-approach-proposal.html) | Not adopted; kept as history | Alternative using tmux panes. The user chose to keep panes embedded in Bubble Tea |
| [ADR-0001: MCP agent messaging](../adr/0001-mcp-agent-messaging/index.html) | Accepted | Why agents send through an MCP tool and receive through a gated paste, and how that works in the code. Architecture decisions live in `docs/adr/`; the [README](../../README.md#architecture-decisions) explains the convention |
| [ADR-0002: Cross-machine messaging](../adr/0002-cross-machine-messaging/index.html) | Accepted for implementation 2026-11-05, Direct LAN added 2026-11-06; written and compile-checked, never run | One `rtk` binary with gateway and runtime startup commands, a central gateway on the tailnet or, by opt-in, a LAN, with pairing, separately deployable runtimes, full message storage and immediate failure when a host is unreachable, plus earlier options as history, drawbacks and open questions |
| [Cross-machine contract](../implementation/cross-machine-contract.md) | Describes the current candidate | Commands, protocol, result statuses and the defaults chosen for ADR-0002's open items |
| [Outstanding issues](../implementation/outstanding-issues.md) | Maintained by lead | Confirmed defects, unverified risks and deferred features |

Candidate report: [candidate-report.md](../implementation/candidate-report.md). It's the first candidate's report, kept as history. Current status is in Outstanding issues.
