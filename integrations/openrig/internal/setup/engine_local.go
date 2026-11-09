package setup

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/rigcli"
	"github.com/mngeow/ratatoskr/integrations/openrig/internal/sidecar"
)

// ---- step 1: preflight ----

func (e *Engine) planPreflight(ctx context.Context) Plan {
	p := Plan{Step: StepPreflight, Auto: true}
	st := &e.st
	if st.inSeat {
		p.Notices = append(p.Notices,
			Notice{Warn, "OPENRIG_SESSION_NAME is set: this terminal looks like an OpenRig seat. Run setup from an ordinary terminal."},
			Notice{Info, "The service step and the skill step will only print commands, because anything started from a seat would carry that seat's identity."})
	}
	switch {
	case !st.cfgThere:
		p.Facts = append(p.Facts, Fact{"configuration file", line("%s (does not exist yet)", e.env.ConfigPath)})
	case st.oldErr != nil:
		p.Facts = append(p.Facts, Fact{"configuration file", line("%s (exists, does not load: %v)", e.env.ConfigPath, st.oldErr)})
		p.Notices = append(p.Notices, Notice{Info, "The file is left alone until you agree to replace it, and a backup is kept."})
	default:
		p.Facts = append(p.Facts, Fact{"configuration file", line("%s (exists and loads)", e.env.ConfigPath)})
	}
	p.Facts = append(p.Facts, Fact{"state directory", Printable(st.stateDir)})
	if st.credHave {
		p.Facts = append(p.Facts, Fact{"credential", "present"})
	} else {
		p.Facts = append(p.Facts, Fact{"credential", "not yet"})
	}
	if rec, ok := sidecar.ReadStopped(filepath.Join(st.stateDir, "stopped.json")); ok {
		p.Facts = append(p.Facts, Fact{"stopped marker", line("present (%s at %s); pairing again or `serve --resume` clears it", rec.Reason, rec.At)})
	}
	if e.env.GOOS == "darwin" {
		if _, err := os.Lstat(e.plistPath()); err == nil {
			p.Facts = append(p.Facts, Fact{"launchd plist", "present"})
		} else {
			p.Facts = append(p.Facts, Fact{"launchd plist", "not yet"})
		}
		switch state, detail := e.jobState(ctx); state {
		case jobLoaded:
			p.Facts = append(p.Facts, Fact{"launchd job", "loaded"})
		case jobNone:
			p.Facts = append(p.Facts, Fact{"launchd job", "not loaded"})
		default:
			p.Facts = append(p.Facts, Fact{"launchd job", "unknown (" + Printable(detail) + ")"})
		}
	}
	return p
}

// applyPreflight creates the configuration directory and the state directory (0700) or checks them.
func (e *Engine) applyPreflight() Result {
	if err := os.MkdirAll(filepath.Dir(e.env.ConfigPath), 0o700); err != nil {
		return e.stop(StepPreflight, StatusNotDone, ExitUsage, "cannot create the directory of the configuration file")
	}
	probe := &config.Config{StateDir: e.st.stateDir}
	if err := probe.CheckStateDir(true); err != nil {
		return e.stop(StepPreflight, StatusNotDone, ExitUsage, "%v", err)
	}
	return Result{Status: StatusDone, Detail: "state directory " + Printable(e.st.stateDir) + " is 0700 and yours"}
}

// ---- step 2: local OpenRig ----

func (e *Engine) planLocal() Plan {
	c := e.st.cand
	p := Plan{Step: StepLocalOpenRig}

	rigDefault := c.RigPath
	if rigDefault == "" {
		if found, err := e.env.LookPath("rig"); err == nil {
			rigDefault, _ = filepath.Abs(found)
		}
	}
	envDefault := c.RigEnvPath
	if envDefault == "" {
		var missing []string
		envDefault, missing = BuildEnvPath(e.env.LookPath, filepath.Abs)
		for _, tool := range missing {
			p.Notices = append(p.Notices, Notice{Info, tool + " was not found on your PATH"})
		}
	}
	homeDefault := c.OpenRigHome
	if homeDefault == "" {
		homeDefault = e.env.Getenv("OPENRIG_HOME")
	}
	if homeDefault == "" {
		homeDefault = filepath.Join(e.env.Home, ".openrig")
	}
	seatDefault := c.OperatorSeat
	if seatDefault == "" {
		seatDefault = "operator-agent@kernel"
	}
	if env := e.env.Getenv("OPENRIG_URL"); env != "" && c.OpenRigURL == "" {
		p.Notices = append(p.Notices, Notice{Info, line("OPENRIG_URL is set in this shell (%s) but is NOT used unless you enter it here.", env)})
	}
	p.Notices = append(p.Notices, Notice{Info, line("participant %q and session %q are fixed names (edit the file to change them).", c.Participant, c.Session)})
	p.Fields = []Field{
		{ID: "rig_path", Label: "Path of the rig command", Default: rigDefault},
		{ID: "rig_env_path", Label: "Directories for rig's PATH (colon-separated, absolute)", Default: envDefault,
			Help: "The bridge gives rig a PATH built from this list only. It never inherits yours or the service's."},
		{ID: "openrig_home", Label: "OpenRig home (the instance root)", Default: homeDefault},
		{ID: "operator_seat", Label: "Operator seat (seat@rig)", Default: seatDefault},
		{ID: "openrig_url", Label: "OpenRig daemon URL (optional)", Default: c.OpenRigURL, Optional: true,
			Help: "Leave blank to let rig find the daemon from the OpenRig home."},
	}
	p.Questions = []Question{{ID: "check", Prompt: "Check now that the operator seat is ready? (read-only: rig --version and rig ps)", DefaultYes: true}}
	return p
}

func (e *Engine) validate(step StepID, id, value string) string {
	switch id {
	case "rig_path":
		if info, err := os.Stat(value); err != nil || !filepath.IsAbs(value) || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
			return "not an absolute path of an executable file"
		}
	case "rig_env_path":
		if value == "" || strings.ContainsAny(value, "\x00\n") {
			return "at least one directory is required"
		}
		for _, dir := range strings.Split(value, ":") {
			if !filepath.IsAbs(dir) {
				return "every entry must be an absolute directory"
			}
		}
	case "openrig_home":
		if info, err := os.Stat(value); err != nil || !filepath.IsAbs(value) || !info.IsDir() {
			return "not an existing absolute directory"
		}
	case "operator_seat":
		if !config.ValidSeat(value) {
			return "does not look like seat@rig"
		}
	case "openrig_url":
		if value == "" {
			return ""
		}
		if u, err := url.Parse(value); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "must be an http or https URL"
		}
	case "url":
		return e.validateParticipantURL(value)
	case "pin":
		return validatePinText(value)
	}
	return ""
}

// NormalizeURL is the one place a typed or pasted participant URL is cleaned: surrounding whitespace (a trailing
// newline included), a leading "participant API:" label and one trailing "/" are dropped. Validate, StagePair and
// ParsePaste all use it, so what the owner confirms is what is written.
func NormalizeURL(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= len("participant API:") && strings.EqualFold(value[:len("participant API:")], "participant API:") {
		value = strings.TrimSpace(value[len("participant API:"):])
	}
	return strings.TrimSuffix(value, "/")
}

func (e *Engine) validateParticipantURL(value string) string {
	u, err := url.Parse(NormalizeURL(value))
	switch {
	case err != nil || u.Host == "" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "":
		return "must be scheme://host:port"
	case u.Scheme != "http" && u.Scheme != "https":
		return "must start with https:// or http://"
	}
	return ""
}

func validatePinText(value string) string {
	if _, err := config.ParsePin(value); err != nil {
		if strings.HasPrefix(value, "sha256//") {
			return "that is the key pin: use the value of the line that starts \"certificate SHA-256\""
		}
		return "expected 64 hex digits, optionally prefixed sha256:"
	}
	return ""
}

func (e *Engine) applyLocal(ctx context.Context, a Answers) Result {
	text := func(id string) string { return strings.TrimSpace(a.Text[id]) }
	errs := map[string]string{}
	for _, id := range []string{"rig_path", "rig_env_path", "openrig_home", "operator_seat", "openrig_url"} {
		if msg := e.validate(StepLocalOpenRig, id, text(id)); msg != "" {
			errs[id] = msg
		}
	}
	if len(errs) > 0 {
		return Result{FieldErrors: errs}
	}
	c := e.st.cand
	c.RigPath, c.RigEnvPath, c.OpenRigHome = text("rig_path"), text("rig_env_path"), text("openrig_home")
	c.OperatorSeat, c.OpenRigURL = text("operator_seat"), text("openrig_url")

	var lines []string
	if yes(a, "check", true) {
		rig := &rigcli.Rig{
			Path: c.RigPath, EnvPath: c.RigEnvPath, Home: e.env.Home, OpenRigHome: c.OpenRigHome, OpenRigURL: c.OpenRigURL,
			Seat: c.OperatorSeat, ReadyTimeout: 5 * time.Second, Runner: rigcli.ExecRunner{},
		}
		vctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		version, err := rig.Version(vctx)
		cancel()
		if err != nil {
			lines = append(lines, line("rig --version failed: %v", err))
		} else {
			lines = append(lines, line("rig: %s", version))
		}
		rctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		ready, word := rig.Ready(rctx)
		cancel()
		if ready {
			lines = append(lines, line("operator seat %s: ready", c.OperatorSeat))
		} else {
			lines = append(lines, line("operator seat %s: not ready (%s). Setup can continue; the bridge refuses messages until it is.", c.OperatorSeat, word))
		}
	}
	return Result{Status: StatusDone, Detail: fmt.Sprintf("rig %s, seat %s", filepath.Base(c.RigPath), c.OperatorSeat), Lines: lines}
}
