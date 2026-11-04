# team-ratatoskr

Six seats build Ratatoskr as a Go agent-network application. All authoritative architecture docs and diagrams belong to architect; lead integrates the application; client/backend owners design first, delegate an initial implementation to code monkey, then take over refinement and verification.

| Seat | Runtime/model | Responsibility |
| --- | --- | --- |
| design.architect | Claude Code default | System architecture, design docs/HTML/CSS diagrams, sole external contact |
| delivery.lead | Claude Code default | Coordination and application integration |
| engineering.frontend | Codex gpt-6.1-sol | UI design and all client-side applications, including CLI/TUI |
| engineering.backend | Claude Code default | Go core, code design/patterns, correctness and performance |
| build.monkey | Pi deepseek/deepseek-flash | Supervised first-version coding only |
| platform.devops | Codex default | Infrastructure and networking |

## Communication and implementation

Architect consults analysis-architect@codebase-explorer for prior analysis and brainstorming, verifying the live address first. No other seat contacts outside the rig. Architect communicates high-level plans to lead and never contacts monkey. Lead coordinates frontend/backend/DevOps. Frontend or backend specifies a scoped implementation brief; monkey checks it with that engineer, obtains confirmation, implements the initial version and hands it back. The owning engineer refines/tests and returns evidence to lead. Backend consults DevOps whenever unsure about anything or needing networking help. Only one monkey task/owner is active at a time. Both senior engineers have delegation edges into monkey; OpenRig's escalation-parent heuristic is not the task supervisor, so track the named supervisor in each brief and route any mismatched recovery through lead. Edges do not enforce messaging restrictions.

Every seat has an individually delivered agents/<role>/guidance.md; shared CULTURE.md and context.md define the team contract. Do not replace role guidance with the last managed role block in a shared project file. Tests belong to frontend/backend/DevOps in their domains; lead coordinates integration checks. No extra QA seat is introduced.

## Portable launch

From the project checkout, first preview:

```bash
rig up rigs/team-ratatoskr/rig.yaml --cwd "$PWD" --plan
# After approval:
rig up rigs/team-ratatoskr/rig.yaml --cwd "$PWD"
```

Definitions are suitable for Git. Member cwd ../.. resolves to the project from this folder; explicit --cwd is recommended. Claude Code and Codex must be installed/authenticated. Pi uses DeepSeek Platform model deepseek-flash; configure its credentials in its isolated managed-seat agent directory using the supported Pi authentication flow. Never put credentials in this repo, spec env fields or queues. No credentials are copied from another seat by this definition. Provisioning/authentication and actual model execution must be verified at launch.

No permission policy is selected; retain team defaults unless the user separately chooses a policy. Native prompts can still occur. OpenRig launch adds managed instructions/metadata to the project and may apply trust/config changes in the selected runtime home. Startup orientation alone does not authorize feature implementation. The initial design assignment should go to architect, including the user goal, root, constraints and the mandatory explorer consultation.

## Verification

Validate rig.yaml with rig spec validate, audit and preflight. Validate each agent.yaml with rig agent validate. A dry-run plan checks structural launchability, not credentials or runtime behavior. Verify six ready seats, actual model/provider access, external-routing discipline and the senior -> monkey -> senior handoff after launch. Cross-host behavior is untested until exercised.
