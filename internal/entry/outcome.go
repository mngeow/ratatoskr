package entry

// What an ssh attempt's result means to the person who asked for it.
//
// The remote window's exit code is the only signal: rtk's own codes (3, 4, 2) are exact, 255 is
// ssh's own catch-all and cannot be told apart from an unreachable host, and anything else is the
// remote command's own failure.

import "fmt"

// Outcome kinds.
const (
	OutcomeOK          = "ok"
	OutcomeNotRunning  = "not_running"
	OutcomeNoDaemon    = "no_daemon"
	OutcomeTooOld      = "too_old"
	OutcomeSSHFailed   = "ssh_failed"
	OutcomeOther       = "other"
	OutcomeCannotStart = "cannot_start"
)

// Outcome is what the caller should say, and whether to refresh the Network tab.
type Outcome struct {
	Kind       string
	Message    string
	RefreshTab bool
}

// ClassifySSHExit maps one ssh exit code to what happened.
//
// UNTESTED.
func ClassifySSHExit(code int, host string) Outcome {
	host = cleanOrDefault(host, "that host")

	switch code {
	case 0:
		// The remote window ended normally, by F10 or because the session ended.
		return Outcome{Kind: OutcomeOK, RefreshTab: true}
	case 3:
		return Outcome{
			Kind:       OutcomeNotRunning,
			Message:    "ended or replaced; not opened. Choose again.",
			RefreshTab: true,
		}
	case 4:
		return Outcome{
			Kind:    OutcomeNoDaemon,
			Message: "no rtk daemon on " + host,
		}
	case 2:
		return Outcome{
			Kind:    OutcomeTooOld,
			Message: "the rtk on " + host + " is too old for exact-session entry; upgrade it. Not attached.",
		}
	case 255:
		return Outcome{
			Kind:    OutcomeSSHFailed,
			Message: "ssh couldn't connect or the connection ended",
		}
	default:
		return Outcome{
			Kind:    OutcomeOther,
			Message: fmt.Sprintf("the remote command exited %d.", code),
		}
	}
}

// ClassifyStart maps a failure to start ssh at all.
//
// It covers the two cases the caller can act on: ssh is not installed, or the terminal could not be
// given to it. Both leave the Network tab as it was.
//
// UNTESTED.
func ClassifyStart(err error, host string) Outcome {
	host = cleanOrDefault(host, "that host")

	if err == nil {
		return Outcome{Kind: OutcomeOK, RefreshTab: true}
	}
	return Outcome{
		Kind:    OutcomeCannotStart,
		Message: "ssh could not be started on this machine; nothing was run on " + host,
	}
}
