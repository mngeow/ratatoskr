package setup

// Step 8: the skill file and the `rig walk` offer.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

func (e *Engine) skillPath() string {
	return filepath.Join(e.st.cfg.StateDir, "skill", "ratatoskr-openrig", "SKILL.md")
}

func (e *Engine) walkCommand() string {
	return "rig walk " + shellQuote(e.st.cfg.OperatorSeat) + " --through " + shellQuote(e.skillPath())
}

func (e *Engine) planSkill() Plan {
	p := Plan{Step: StepSkill}
	if e.st.cfg == nil {
		p.Auto = true
		p.Notices = []Notice{{Warn, "There is no configuration yet."}}
		return p
	}
	path := e.skillPath()
	p.Facts = []Fact{{"skill file", Printable(path)}}
	if current, err := os.ReadFile(path); err == nil && string(current) != e.env.Assets.Skill {
		p.Questions = append(p.Questions, Question{ID: "replace", DefaultYes: false,
			Prompt: "The skill file differs from the one built into this program (an older or edited copy). Replace it? (the old file is kept as a .bak-<time> copy)"})
	}
	p.Instructions = []string{"To give the operator the skill, run this from an ordinary terminal:", "    " + e.walkCommand(),
		"It is NOT a persistent install: the operator forgets it when its conversation is replaced or compacted.",
		"Run the command again after every relaunch or compaction. (A persistent install is a documented gap.)"}
	if e.st.inSeat {
		p.Notices = append(p.Notices, Notice{Warn, "OPENRIG_SESSION_NAME is set: setup will not run rig walk from inside a seat."})
	} else {
		p.Questions = append(p.Questions, Question{ID: "run", Prompt: "Run it now?", DefaultYes: false, Detail: []string{e.walkCommand()}})
	}
	return p
}

func (e *Engine) applySkill(ctx context.Context, a Answers) Result {
	st := &e.st
	if st.cfg == nil {
		return e.stop(StepSkill, StatusNotDone, ExitFailed, "there is no configuration yet")
	}
	path := e.skillPath()
	for _, dir := range []string{filepath.Join(st.cfg.StateDir, "skill"), filepath.Dir(path)} {
		if err := CheckPrivateDir(dir, true); err != nil {
			return e.stop(StepSkill, StatusNotDone, ExitFailed, "%v", err)
		}
	}
	var lines []string
	switch _, err := os.Lstat(path); {
	case err == nil:
		if _, err := OwnedRegular(path); err != nil {
			return e.stop(StepSkill, StatusNotDone, ExitFailed, "%v", err)
		}
		current, err := os.ReadFile(path)
		if err != nil {
			return e.stop(StepSkill, StatusNotDone, ExitFailed, "cannot read the installed skill file")
		}
		switch {
		case string(current) == e.env.Assets.Skill:
			lines = append(lines, "The skill file is already in place and current: "+Printable(path))
		case !yes(a, "replace", false):
			lines = append(lines, "The skill file differs and was left as it is.")
		default:
			backup, err := CopyAside(path, e.env.Now())
			if err != nil {
				return e.stop(StepSkill, StatusNotDone, ExitFailed, "%v", err)
			}
			if err := WriteFileAtomic(path, []byte(e.env.Assets.Skill), 0o600); err != nil {
				return e.stop(StepSkill, StatusNotDone, ExitFailed, "%v", err)
			}
			lines = append(lines, "replaced; the old file is kept as "+Printable(backup))
		}
	case errors.Is(err, os.ErrNotExist):
		if err := WriteFileAtomic(path, []byte(e.env.Assets.Skill), 0o600); err != nil {
			return e.stop(StepSkill, StatusNotDone, ExitFailed, "%v", err)
		}
		lines = append(lines, "Wrote the skill file: "+Printable(path))
	default:
		return e.stop(StepSkill, StatusNotDone, ExitFailed, "cannot inspect the skill file")
	}
	lines = append(lines, "Activate it with:  "+e.walkCommand(), "Not persistent: run it again after every relaunch or compaction.")

	if st.inSeat || !yes(a, "run", false) {
		return Result{Status: StatusYourTurn, Detail: "run: " + e.walkCommand(), Lines: lines}
	}
	out, code, err := e.runTimed(ctx, 60*time.Second, st.cfg.RigPath, "walk", st.cfg.OperatorSeat, "--through", path)
	if err != nil || code != 0 {
		lines = append(lines, line("rig walk did not succeed (exit %d).", code))
		lines = append(lines, outputLines(out, 8)...)
		return Result{Status: StatusNotDone, Detail: "rig walk failed: run it yourself", Lines: lines}
	}
	lines = append(lines, "rig walk finished (exit 0):")
	lines = append(lines, outputLines(out, 8)...)
	return Result{Status: StatusDone, Detail: "walked once; not persistent", Lines: lines}
}
