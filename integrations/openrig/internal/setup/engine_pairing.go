package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/mngeow/ratatoskr/integrations/openrig/internal/config"
)

// ---- step 3: VM-A pairing material ----

var vmAInstructions = []string{
	"Run this on VM-A, in a terminal of your own (setup does not use SSH):",
	"",
	`    export XDG_STATE_HOME="<the gateway's state directory>"     # only if the gateway uses a non-default one`,
	"    rtk gateway participant pair openrig-main",
	"",
	"It prints the pairing code alone on the first line, then \"participant: openrig-main\", the expiry,",
	"\"participant API: https://HOST:PORT\", a \"certificate SHA-256\" line, a \"key pin\" line and a note.",
	"The code is single use and expires in 10 minutes. If a credential for openrig-main already exists, running",
	"this again is a ROTATION: redeeming the new code replaces the credential and ends this participant's open sessions.",
	"",
	"Which pin: the line that starts \"certificate SHA-256\" (its value looks like sha256:<64 hex digits>).",
	"NOT the \"key pin\" line (sha256//...): that is a different rule and the bridge refuses it.",
}

func (e *Engine) planPairingMaterial() Plan {
	st := &e.st
	p := Plan{Step: StepPairingMaterial, PasteBlock: true, NeedCode: !st.credHave}
	if st.oldCfg != nil && st.oldCfg.ParticipantURL != "" {
		p.Facts = append(p.Facts, Fact{"current gateway", line("%s   pin %s", st.oldCfg.ParticipantURL, pinSummary(st.oldCfg))})
		if st.credHave {
			p.Questions = append(p.Questions, Question{ID: "keep", Prompt: "A credential exists. Keep this participant URL and pin?", DefaultYes: true})
		}
	}
	if p.NeedCode {
		p.Instructions = vmAInstructions
	} else {
		p.Instructions = append([]string{
			"A credential exists, so no pairing code is used here. Run `rtk gateway participant pair openrig-main` on VM-A for the",
			"new URL and pin only.", ""}, vmAInstructions[len(vmAInstructions)-3:]...)
		p.Notices = append(p.Notices, Notice{Info, "Only the URL and the pin will be replaced; the existing credential is never replaced by this program."})
	}
	if e.env.InsecureHTTP {
		p.Notices = append(p.Notices, Notice{Warn, "--insecure-http is on: a plain http:// participant API will be accepted. Anything on the network path can read the credential and every message."})
	}
	p.Fields = []Field{
		{ID: "url", Label: "participant API URL (the \"participant API:\" line)"},
		{ID: "pin", Label: "certificate SHA-256 (sha256:<64 hex digits>)", Help: "Leave empty only for a plain http:// URL."},
	}
	if p.NeedCode {
		p.Fields = append(p.Fields, Field{ID: "code", Label: "pairing code", Secret: true})
	}
	p.Questions = append(p.Questions, Question{ID: "use", Prompt: "Use these values?", DefaultYes: true})
	return p
}

func summaryOf(p Pasted) PasteSummary {
	return PasteSummary{Participant: p.Participant, URL: p.URL, Pin: p.Pin, Plain: p.Plain, KeyPinSeen: p.KeyPinSeen,
		Rotation: p.Rotation, StaleNote: p.StaleNote, CodeReceived: len(p.Code) > 0}
}

func (e *Engine) stageNew(s *staged, sum PasteSummary) PasteSummary {
	e.st.staged.wipe()
	e.st.staged, e.st.summary = s, sum
	return sum
}

func (e *Engine) parsePaste(block []byte) (PasteSummary, error) {
	defer Wipe(block)
	if err := e.detect(context.Background()); err != nil {
		return PasteSummary{}, err
	}
	p, err := ParsePaste(block)
	if err != nil {
		return PasteSummary{}, err
	}
	if err := p.CheckAgainst(e.st.cand.Participant, e.env.InsecureHTTP); err != nil {
		p.Wipe()
		return PasteSummary{}, err
	}
	sum := summaryOf(p)
	s := &staged{URL: p.URL, Pin: p.Pin}
	if e.st.credHave {
		p.Wipe() // no code is used when a credential exists
	} else {
		s.Code = p.Code
	}
	return e.stageNew(s, sum), nil
}

func (e *Engine) stagePair(rawURL, pin string, code []byte) (PasteSummary, error) {
	if err := e.detect(context.Background()); err != nil {
		Wipe(code)
		return PasteSummary{}, err
	}
	if msg := e.validateParticipantURL(rawURL); msg != "" {
		Wipe(code)
		return PasteSummary{}, errors.New("URL: " + msg)
	}
	plain := strings.HasPrefix(rawURL, "http://")
	normalised := ""
	if !plain {
		if msg := validatePinText(pin); msg != "" {
			Wipe(code)
			return PasteSummary{}, errors.New("pin: " + msg)
		}
		parsed, _ := config.ParsePin(pin)
		normalised = fmt.Sprintf("sha256:%x", parsed[:])
	} else if pin != "" {
		Wipe(code)
		return PasteSummary{}, errors.New("pin: a plain http:// URL has no certificate pin: leave it empty")
	}
	needCode := !e.st.credHave
	if needCode && (len(code) == 0 || strings.ContainsAny(string(code), " \t\r\n") || len(code) > MaxCodeBytes) {
		Wipe(code)
		return PasteSummary{}, errors.New("code: a pairing code is one word")
	}
	s := &staged{URL: rawURL, Pin: normalised}
	if needCode {
		s.Code = code
	} else {
		Wipe(code)
	}
	sum := PasteSummary{Participant: e.st.cand.Participant, URL: rawURL, Pin: normalised, Plain: plain, CodeReceived: needCode}
	return e.stageNew(s, sum), nil
}

func (e *Engine) applyPairingMaterial(a Answers) Result {
	st := &e.st
	if st.oldCfg != nil && st.credHave && st.oldCfg.ParticipantURL != "" && yes(a, "keep", true) && st.staged == nil {
		st.cand.ParticipantURL, st.cand.CertificateSHA256 = st.oldCfg.ParticipantURL, st.oldCfg.CertificateSHA256
		return Result{Status: StatusAlready, Detail: "credential and gateway address kept"}
	}
	if st.staged == nil {
		return Result{Lines: []string{"nothing is staged: paste the block, or fill the fields, before confirming"}}
	}
	if !yes(a, "use", true) {
		st.staged.wipe()
		st.staged = nil
		return Result{Lines: []string{"discarded: paste or enter the values again"}}
	}
	st.cand.ParticipantURL, st.cand.CertificateSHA256 = st.staged.URL, st.staged.Pin
	st.chosen.wipe()
	st.chosen, st.staged = st.staged, nil
	return Result{Status: StatusDone, Detail: line("gateway %s", st.chosen.URL)}
}

// ---- step 4: configuration file ----

func (e *Engine) planConfig() Plan {
	st := &e.st
	p := Plan{Step: StepConfig}
	c := st.cand
	c.Home = e.env.Home
	if err := c.Check(e.env.InsecureHTTP); err != nil {
		p.Notices = append(p.Notices, Notice{Warn, line("the configuration would not be accepted: %v (nothing will be written)", err)})
		return p
	}
	switch {
	case st.oldCfg != nil:
		p.Changes = DiffConfig(st.oldCfg, c)
		if len(p.Changes) == 0 {
			p.Auto = true
			p.Facts = append(p.Facts, Fact{"configuration file", line("%s already holds these values", e.env.ConfigPath)})
			return p
		}
		p.Questions = []Question{{ID: "write", Prompt: "Replace it? (the old file is kept as a .bak-<time> copy)", DefaultYes: true}}
	case st.cfgThere:
		p.Notices = append(p.Notices, Notice{Info, line("The existing file does not load (%v); it will be replaced and a backup kept.", st.oldErr)})
		p.Changes = DiffConfig(nil, c)
		p.Questions = []Question{{ID: "write", Prompt: "Replace it?", DefaultYes: true}}
	default:
		p.Changes = DiffConfig(nil, c)
		p.Questions = []Question{{ID: "write", Prompt: line("Write %s?", e.env.ConfigPath), DefaultYes: true}}
	}
	p.Facts = append(p.Facts, Fact{"configuration file", Printable(e.env.ConfigPath)})
	return p
}

func (e *Engine) applyConfig(a Answers) Result {
	st := &e.st
	c := st.cand
	c.Home = e.env.Home
	if err := c.Check(e.env.InsecureHTTP); err != nil {
		return e.stop(StepConfig, StatusNotDone, ExitUsage, "the configuration would not be accepted: %v (nothing was written)", err)
	}
	if st.oldCfg != nil && len(DiffConfig(st.oldCfg, c)) == 0 {
		st.cfg = st.oldCfg
		return Result{Status: StatusAlready, Detail: "the file already holds these values"}
	}
	if !yes(a, "write", true) {
		if st.oldErr == nil && st.oldCfg != nil {
			st.cfg = st.oldCfg
			return Result{Status: StatusSkipped, Detail: "kept the existing file"}
		}
		return e.stop(StepConfig, StatusNotDone, ExitFailed, "the configuration was not written and there is no usable one")
	}
	backup, err := WriteConfig(e.env.ConfigPath, c, e.env.InsecureHTTP, e.env.Now())
	if err != nil {
		return e.stop(StepConfig, StatusNotDone, ExitFailed, "%v", err)
	}
	loaded, err := config.Load(e.env.ConfigPath, e.env.InsecureHTTP)
	if err != nil {
		return e.stop(StepConfig, StatusNotDone, ExitFailed, "the file was written but does not load: %v", err)
	}
	st.cfg, st.configChanged = loaded, true
	r := Result{Status: StatusDone, Detail: Printable(e.env.ConfigPath), Lines: []string{"written (mode 0600), and it loads."}}
	if backup != "" {
		r.Lines = append(r.Lines, line("previous file kept as %s", backup))
	}
	return r
}

// ---- step 5: pair ----

func (e *Engine) planPair() Plan {
	st := &e.st
	p := Plan{Step: StepPair, Auto: true}
	if st.credHave {
		p.Facts = append(p.Facts, Fact{"credential", "already present: pairing is skipped"})
		p.Notices = append(p.Notices, Notice{Info, "This program never moves or deletes a credential. To re-pair on purpose, move it aside yourself, then run setup again."})
		p.Instructions = []string{"mv " + shellQuote(st.credPath) + " " + shellQuote(st.credPath+".old")}
	} else if e.st.cand != nil && strings.HasPrefix(e.st.cand.ParticipantURL, "http://") {
		p.Notices = append(p.Notices, Notice{Warn, "plain HTTP: the pairing code and the credential are readable by anything on the network path."})
	}
	return p
}

func (e *Engine) applyPair() Result {
	st := &e.st
	if st.cfg == nil {
		return e.stop(StepPair, StatusNotDone, ExitFailed, "there is no configuration to pair with")
	}
	if info, err := os.Lstat(st.cfg.CredentialPath); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
		return Result{Status: StatusAlready, Detail: "credential exists",
			Lines: []string{"A credential already exists: not pairing again.", "To re-pair on purpose, move it aside yourself: mv " +
				shellQuote(st.cfg.CredentialPath) + " " + shellQuote(st.cfg.CredentialPath+".old")}}
	}
	if st.chosen == nil || len(st.chosen.Code) == 0 {
		return e.stop(StepPair, StatusNotDone, ExitFailed, "there is no pairing code and no credential: run setup again")
	}
	if err := st.cfg.CheckStateDir(true); err != nil {
		return e.stop(StepPair, StatusNotDone, ExitUsage, "%v", err)
	}
	res := Redeem(st.cfg, string(st.chosen.Code))
	st.chosen.wipe()
	switch res.Outcome {
	case Paired:
		st.credNew = true
		return Result{Status: StatusDone, Detail: "paired as " + st.cfg.Participant, Lines: []string{Printable(res.Message)}}
	case RedeemUncertain:
		r := e.stop(StepPair, StatusNotDone, ExitUncertain, "%s", Printable(res.Message))
		r.Detail = "uncertain: the code may or may not have been redeemed"
		return r
	case RedeemUsage:
		return e.stop(StepPair, StatusNotDone, ExitUsage, "%s", Printable(res.Message))
	}
	return e.stop(StepPair, StatusNotDone, ExitFailed, "%s", Printable(res.Message))
}

func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func pinSummary(cfg *config.Config) string {
	if cfg.Scheme != "https" {
		return "none (plain HTTP)"
	}
	return fmt.Sprintf("sha256:%x...", cfg.Pin[:4])
}
