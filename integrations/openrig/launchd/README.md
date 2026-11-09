# macOS service setup

Written for the OpenRig bridge on `feat/openrig-bridge`. **Untested: PENDING USER EXECUTION.** The team has not loaded this plist, started the bridge, installed a skill, or contacted a VM. GR-3 remains in force. The commands below are for the user to run when they choose.

This is one foreground `ratatoskr-openrig serve --config /absolute/config.json` supervised by launchd, not a new OpenRig seat. It neither starts nor stops OpenRig. The existing kernel operator and daemon must already be available under the same macOS account. This user agent runs in that account's GUI login domain; logout stops it. systemd and unattended system-wide startup are outside v1.

## Primary path: setup wizard (user only)

Run the installed binary from an ordinary Mac terminal, with terminal input and output:

```sh
ratatoskr-openrig setup
# Or choose an explicit absolute configuration path:
ratatoskr-openrig setup --config /absolute/path/config.json
```

This is a line-oriented wizard. It guides config and pairing, then offers service preparation, installation and startup. Paste the VM-A pairing output only at its hidden-input prompt: it selects the **certificate SHA-256** line, not the SPKI `key pin`. See the [bridge README](../README.md) and scenario 8 for the full three-host setup and remaining user checks. An existing credential is preserved rather than silently redeemed again.

On macOS the service step uses the embedded version of `dev.ratatoskr.openrig.plist.template`, XML-escapes the five absolute paths, prepares the state/log directories, creates logs without truncating them, and lints a private temporary plist before installing it. Its prompts separate these actions:

- **Prepare the log directory and render the plist:** defaults Yes; loads nothing.
- **Install the plist:** defaults Yes for a new file. A differing existing file shows its changes and **Replace it?** defaults No; accepted replacement keeps a `.bak-<UTC>` copy and uses an atomic rename.
- **Start the service now?** defaults No. The action is `launchctl bootstrap gui/<uid> <plist>`.
- If a job is loaded and settings changed, **Stop the running job and start it again?** defaults No. The action is exact-service `bootout` followed by `bootstrap`, never removing the whole GUI domain. Declining leaves the running job on its old settings, even when new files were saved.

`launchctl print` exit 0 means the job is loaded, not necessarily running or healthy. Missing GUI domain, timeout or an unknown inspection failure requires manual review; do not infer that the job is absent or retry with elevated privileges. Inspect the final checklist to distinguish files saved, service loaded and bridge ready. A bootstrap error after bootout is a partial failure: the old service has been stopped, **nothing was rolled back**, and setup prints the log and recovery commands. Do not repeat restart actions blindly after an uncertain command result.

The implementation checks the GUI domain first and treats only service-query exit **113** as not found; that exit-code interpretation is **unverified** under GR-3. Other nonzero results are unknown. Before writing/replacing the installed plist, it refuses unknown state or a loaded label with no existing plist at the expected path. Private directory/log preparation and temporary linting can already have occurred; this guard protects the installed plist and service, not every earlier setup file operation.

The wizard waits about 20 seconds for a sidecar status answer when startup was expected and explains gateway, registration and operator-seat readiness separately. Neither that status nor successful bootstrap proves the round trip; **Mac operator → VM-A → VM-B Claude → same operator remains PENDING USER EXECUTION**.

Service installation is skipped on Linux (foreground `serve` is printed; no systemd), when running inside an OpenRig seat (print-only), or for explicitly plain HTTP (the supplied plist has no `--insecure-http`). A temporary `go run` binary is refused. A binary beneath a source tree prompts before use; prefer an installed copy outside the checkout. Existing log permissions wider than 0600 are warned about, not silently repaired: fix them deliberately before service use.

The wizard does not load a persistent skill into OpenRig. Its separate skill step prints/offers a default-No `rig walk` to tell the running operator; that instruction must be repeated after conversation replacement or compaction. No OpenRig seat is started/stopped, and no MCP/hook configuration is installed.

If automated setup cannot establish ownership or launchd state, stop at the checklist and use the manual fallback after reviewing the existing service. Implementation sources are `internal/cli/setup_service.go` and `internal/setup/{plist,dirs,configfile}.go`. The plist is embedded at build time: rebuild/reinstall the executable after template edits; changing the checkout template alone cannot change the installed wizard.

## Manual fallback: prepare and render

Build/install the bridge using the main integration guide first, pair it as `openrig-main`, and create its config/credential privately. The config schema and pairing commands belong to backend's implementation, not this plist. The serve argument order was confirmed with backend. Do not put a credential, pairing code or token in the plist, environment or command line.

Before bootstrap, the user creates the working/log directories (launchd opens log files and changes directory before the executable can create anything):

```sh
umask 077
mkdir -p "$HOME/.local/state/ratatoskr-openrig" "$HOME/Library/Logs/ratatoskr-openrig" "$HOME/Library/LaunchAgents"
chmod 700 "$HOME/.local/state/ratatoskr-openrig" "$HOME/Library/Logs/ratatoskr-openrig"
touch "$HOME/Library/Logs/ratatoskr-openrig/serve.out.log" "$HOME/Library/Logs/ratatoskr-openrig/serve.err.log"
chmod 600 "$HOME/Library/Logs/ratatoskr-openrig/serve.out.log" "$HOME/Library/Logs/ratatoskr-openrig/serve.err.log"
```

Use owner-owned real directories/files, not symlinks. Preserve existing configuration, credentials and journal. Inspect an existing `dev.ratatoskr.openrig` job/plist before replacing anything; if it is someone else's service, choose a different label/path consistently.

Copy `dev.ratatoskr.openrig.plist.template` to a reviewable temporary file and replace **all** placeholders in an editor. Values are literal absolute paths: launchd does not expand `~`, `$HOME`, shell substitutions or quotes inside XML strings. XML-escape `&` as `&amp;` and `<` as `&lt;`.

| Placeholder | Owner-supplied value |
| --- | --- |
| `__BINARY__` | Absolute installed `ratatoskr-openrig` executable, outside the checkout |
| `__CONFIG__` | Absolute bridge JSON config path, mode 0600 |
| `__STATE_DIR__` | Absolute path to the private state directory created above |
| `__LOG_DIR__` | Absolute path to this user's `Library/Logs/ratatoskr-openrig` directory |
| `__HOME__` | This user's absolute home directory |

The job environment contains only explicit HOME and minimal PATH `/usr/bin:/bin`. **No OPENRIG_* variable belongs in the plist.** Set config `rig_path` to the absolute installed `rig`, `rig_env_path` to the absolute directories containing Node/rig and needed tmux/Herdr helpers plus system tools, and `openrig_home` to the owner's existing instance root. Optional `openrig_url` deliberately selects the configured daemon. The sidecar constructs every rig child environment from scratch from these fields and HOME, per contract section 10.1; it must not inherit its own launchd environment or seat identity. Version-manager shell initialization is not run: use actual installed directories and revisit config after upgrades. Do not change the shared launchd environment or start another OpenRig daemon to work around discovery problems.

The template's umask is decimal **63**, meaning octal 077. `KeepAlive` uses `SuccessfulExit: false`: nonzero exits trigger restart, a zero exit does not. `ThrottleInterval` is **30 seconds**, and `ProcessType` is Background, per contract section 14. **bootout** is the normal stop. NetworkState is intentionally absent: it is not a dependable readiness gate. Network/OpenRig outages are handled by the sidecar; no login-shell readiness scripts or daemon auto-start.

Contract sections 7/8 supersede the earlier idle-on-revocation proposal: revoked/replaced writes private `stopped.json`, then exits 5/4. Launchd restarts after that nonzero exit; the next start finds the marker, prints its reason and exits 0, so no further restart is requested. Successful `pair` or an explicit `serve --resume` clears the marker. Configuration errors exit 2 and can repeat at the 30-second throttle. These behaviors remain untested. Correct the config or bootout rather than leaving a repeating error unattended. Do not remove the stop marker or resume automatically: it prevents a revoked/replaced bridge from fighting another poller.

The state directory also holds `bridge.sock`, the journal and credential. `serve` creates/checks it at 0700, but launchd needs it before applying `WorkingDirectory`; the commands above create it separately from the log directory. Keep the absolute state path short: macOS pathname Unix sockets have a 104-byte address field, leaving at most 103 bytes for a NUL-terminated socket pathname, including `/bridge.sock`. Count UTF-8 bytes, not displayed characters. A long home or custom state path can exceed this; use a backend-supported shorter private state path consistently in config and plist, without moving existing state silently.

## Manual fallback: install and observe (user only)

After all placeholders are replaced, save the rendered file as `/absolute/path/rendered.plist`. Then:

```sh
plutil -lint /absolute/path/rendered.plist
launchctl print "gui/$(id -u)/dev.ratatoskr.openrig"
```

If the label already exists, inspect it and use the uninstall/stop command below for this job before replacement. A missing job is expected for a first install. Install without overwriting an existing plist silently:

```sh
cp -i /absolute/path/rendered.plist "$HOME/Library/LaunchAgents/dev.ratatoskr.openrig.plist"
chmod 600 "$HOME/Library/LaunchAgents/dev.ratatoskr.openrig.plist"
launchctl bootstrap "gui/$(id -u)" "$HOME/Library/LaunchAgents/dev.ratatoskr.openrig.plist"
launchctl print "gui/$(id -u)/dev.ratatoskr.openrig"
tail -n 50 "$HOME/Library/Logs/ratatoskr-openrig/serve.err.log"
```

No `sudo`, `launchctl load`, shared-domain mutations, `kickstart -k`, or OpenRig session reset is needed. A loaded job or PID is not proof of a working bridge. Use the integration guide's sidecar status command and the user-observed Mac → VM-A → VM-B → operator round trip. If GUI bootstrap domain is absent (for example a headless-only login), this template does not claim to work; do not substitute a root daemon.

After changing a rendered plist/config or executable path, bootout just this service and bootstrap its reviewed plist again. Replacing a running executable alone does not reload the process.

## Stop and uninstall (user only)

```sh
launchctl bootout "gui/$(id -u)/dev.ratatoskr.openrig"
launchctl print "gui/$(id -u)/dev.ratatoskr.openrig"
```

The second command should report no such service. Inspect errors rather than treating every failure as success. Once stopped, remove **only** the user's rendered plist if desired:

```sh
rm -i "$HOME/Library/LaunchAgents/dev.ratatoskr.openrig.plist"
```

Do not delete the credential, journal, socket, OpenRig files or logs as a side effect. Gateway participant revocation is a separate owner action described in the guide. Stopping this sidecar does not stop the kernel operator or the VM-B Claude session.

## Logs and lifecycle limits

Both log paths must exist beneath a private directory before startup. Logs must never contain bodies, terminal contents, credentials or pairing codes. Inspect permissions after installation; existing file modes are not repaired by launchd's umask. Logs are not rotated by this template: repeated failures can grow them. Document/monitor free space; to archive, bootout this job first, move its logs privately, recreate them at 0600, then bootstrap again. Renaming an active log does not move an open descriptor. SIGKILL after ExitTimeOut remains possible; backend must journal unknown outcomes before attempting delivery and never retype after restart.

Sources read, not executed: installed macOS SDK `usr/share/man/man5/launchd.plist.5` (ProgramArguments, EnvironmentVariables, KeepAlive, ThrottleInterval, ExitTimeOut, Umask, StandardOutPath/StandardErrorPath); `usr/share/man/man1/launchctl.1` (gui domain, bootstrap, bootout, print). Apple's [Creating Launch Daemons and Agents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html) documents per-user agents and logout lifetime. OpenRig 0.7.7 `daemon/docs/reference/instance-layout.md` describes OPENRIG_HOME; `getting-started.md`, “Optional Mac local-user check”, distinguishes deliberate URL routing from local discovery. Actual daemon resolution from this launchd environment remains PENDING USER EXECUTION.
