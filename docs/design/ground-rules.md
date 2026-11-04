# Ground rules

Status: **in force**, set by the user on 2026-11-04. Recorded by architect; enforced by `delivery.lead`.

These are project constraints, not design proposals. Changing or excepting one needs the user's decision: raise it through lead to architect.

## GR-1: Go

Ratatoskr application code is written in Go.

- Applies to the application itself: core, services and clients.
- Does not apply to documentation, design diagrams or rig definitions.
- A component that cannot be Go goes to architect as an exception request before it is written. See the open question below.

## GR-2: Bubble Tea for TUIs

Any terminal UI in Ratatoskr is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

- Use the current major version, imported as `charm.land/bubbletea/v2`. This is the module path declared in the upstream `go.mod`.
- Do not import `github.com/charmbracelet/bubbletea`: without a `/v2` suffix it resolves to the older v1 line.
- Do not add another TUI framework as a direct dependency. Charm's companion libraries for Bubble Tea are allowed.
- This rule says how a TUI is built. It does not decide that a TUI is in scope, or what it does; that comes from selected designs.

Verified 2026-11-04 against upstream `go.mod` and the Go module proxy: latest release v2.1.1, MIT licence, repository not archived. Version facts go stale, so recheck them when adding the dependency. The exact version is pinned in `go.mod` by the owning engineer.

## GR-3: No testing or pushing until the user says so

Set by the user on 2026-11-04, in their words: "Do not let the team test or push, I will instruct them when and how to do so." It stays in force until the user instructs otherwise, and it overrides testing and verification steps in any design or brief, including the spikes and acceptance checks in the Two-Pane Bridge design.

- **Not allowed:** running tests or feasibility spikes, local or remote, automated or manual; running `rtk` or launching a harness to try behaviour; other executable probes in place of tests; SSH to a server for testing; pushing.
- **Allowed:** implementation, documentation and static code review within approved scope; writing test files without running them; reading docs, source and `--help` output.
- **Compile checks:** `gofmt`, `go vet` and `go build` are allowed as long as nothing built is run. This is architect's reading, and the user can tighten it.
- **Reporting:** mark all behaviour as untested in code notes, the README and reports. Do not ask for test approval repeatedly.
- **Commits:** not authorized by this rule. On 2026-11-04 the user authorized one local commit of the first version, without a push. Each later commit needs the user's say-so.
- **The user's own testing:** the user may test by hand at any time. Their results are recorded as theirs, and don't permit the team to run anything.

## Compliance checks

Applied by each owning engineer and confirmed by lead at integration:

- Application source is Go, or has a recorded exception.
- `go.mod` has no direct dependency on another TUI framework or on `github.com/charmbracelet/bubbletea`.
- Implementation briefs, including those given to the code monkey, state these rules when they are relevant.

## Open question

**Non-Go harness shims.** Harness integration may require a small plugin written in the harness's own language. Whether any is needed depends on the design work still in progress. If one is, it needs a user decision on whether GR-1 allows it. Until then, GR-1 applies without exception.
