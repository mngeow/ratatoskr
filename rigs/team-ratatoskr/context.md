# Ratatoskr context

Use the explicit project --cwd at launch as the project root. This definition lives under rigs/team-ratatoskr in the project; its member cwd ../.. supports launching from that location, but an explicit absolute --cwd is recommended. Do not embed host paths or credentials in reusable files.

Read the project README. Ratatoskr is designed as a Go application for agent discovery, messaging and team spawning across harnesses. At authoring time the README explicitly describes early intent with no installable application. Reinspect current sources before claiming implementation state. No exact API, transport, persistence, deployment strategy or UI form has yet been selected by this definition.

Seats: design.architect (Claude Code), delivery.lead (Claude Code), engineering.frontend (Codex gpt-6.1-sol), engineering.backend (Claude Code), build.monkey (Pi deepseek/deepseek-flash), platform.devops (Codex default). Architect alone consults the live architect in codebase-explorer (known address analysis-architect@codebase-explorer; verify availability/address before use). Explorer has prior analyses relevant to Ratatoskr; obtain paths, revisions and evidence instead of assuming what they prove.

Orientation: read role guidance and culture, verify identity and wait for selected work. When beginning project design, architect must consult explorer, capture relevant evidence, shape Go designs, then give lead the engineering handoff. Other seats do not independently contact explorer or begin speculative implementation. Missing provider access goes through lead/architect to operator; never silently substitute runtime/model.
