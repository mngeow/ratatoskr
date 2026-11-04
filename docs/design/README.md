# Design index

Maintained by architect. A document here is not evidence of implemented behavior unless it says so.

| Document | Status | Summary |
| --- | --- | --- |
| [Ground rules](ground-rules.md) | In force | Project constraints set by the user: Go; Bubble Tea for TUIs; no testing or pushing until instructed |
| [Two-Pane Bridge](mvp-two-pane-bridge.html) | v0.13, the user reports mouse focus and Ctrl+C working by hand; the team has run nothing, and no test or other acceptance check has run | First version: Bubble Tea TUI running Claude Code and Codex side by side, with messaging between them and SQLite-backed message history. Cross-machine messaging is the planned next step |
| [Terminal Approach Proposal](terminal-approach-proposal.html) | Not adopted; kept as history | Alternative using tmux panes. The user chose to keep panes embedded in Bubble Tea |
| [Outstanding issues](../implementation/outstanding-issues.md) | Maintained by lead | Confirmed defects, unverified risks and deferred features |

Candidate report: [candidate-report.md](../implementation/candidate-report.md). It has compile checks only; nothing has been run.
