package gateway

// `rtk gateway participant pair|list|revoke`: the owner's commands for participants (contract section 11),
// mirroring host pairing. They run in a separate process from the gateway, like `unpair`, so anything that
// is not a direct database write reaches the running gateway only at its next check, and the commands say
// so. A code and a credential are never written to a log or a file by these commands.
//
// UNTESTED. Under GR-3 nothing here has been run.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

// participantUsage lists the three commands; adminUsage includes the same lines.
const participantUsage = `usage:
  rtk gateway participant pair ID [--insecure-http]
  rtk gateway participant list
  rtk gateway participant revoke ID`

// runParticipant dispatches the participant subcommands.
func runParticipant(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	if len(args) == 0 {
		return fmt.Errorf("gateway: participant needs pair, list or revoke\n%s", participantUsage)
	}
	command, rest := args[0], args[1:]
	switch command {
	case "pair":
		return runParticipantPair(ctx, store, rest, out, errOut, now)
	case "list":
		return runParticipantList(ctx, store, rest, out, errOut, now)
	case "revoke":
		return runParticipantRevoke(ctx, store, rest, out, errOut, now)
	}
	return fmt.Errorf("gateway: unknown participant command %q\n%s", command, participantUsage)
}

// runParticipantPair mints a single-use code for a participant id and prints it once, with the API address
// the adapter should use and, when that is HTTPS, both pins.
//
// The participant listener is checked FIRST: if no running gateway has recorded one, the command says so and
// mints nothing, because a code with no address to use it at is not usable. The endpoint file is written by
// the gateway when it starts and is advice; if the gateway has since stopped the address is stale, which the
// output says.
//
// --insecure-http is the owner's explicit request for a plain-HTTP block. It does not change the listener: it
// only checks that the recorded one is plain HTTP on a tailnet or LAN address, and refuses BEFORE a code is
// minted otherwise. Without it nothing changes, so an HTTPS listener still gives the HTTPS block and an
// already-plain listener still gives the plain one.
func runParticipantPair(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant pair", errOut)
	plainHTTP := flags.Bool("insecure-http", false, "")
	// The flag package stops at the first non-flag, so the ID may come before the flag
	// (`pair openrig-main --insecure-http`): parse again after taking the ID.
	var id string
	rest := args
	for {
		if err := flags.Parse(rest); err != nil {
			return fmt.Errorf("gateway: participant pair: %w\n%s", err, participantUsage)
		}
		if flags.NArg() == 0 {
			break
		}
		if id != "" {
			return fmt.Errorf("gateway: participant pair needs exactly one ID\n%s", participantUsage)
		}
		id, rest = flags.Arg(0), flags.Args()[1:]
		if len(rest) == 0 {
			break
		}
	}
	if id == "" {
		return fmt.Errorf("gateway: participant pair needs exactly one ID\n%s", participantUsage)
	}

	endpoint, running := LoadParticipantEndpoint(store.dir)
	if !running {
		return fmt.Errorf("gateway: the participant listener is not running: no readable %s in %s. Start the gateway with %s ADDR, then run this again. No code was made",
			participantEndpointName, store.dir, FlagParticipantListen)
	}

	if *plainHTTP {
		// Checked before CreateParticipantCode: a refusal mints nothing.
		switch {
		case endpoint.Scheme == schemeHTTPS:
			return fmt.Errorf("gateway: the participant listener at %s is HTTPS. For plain HTTP, restart the gateway with %s (the host's --insecure-http does not apply to the participant API), then run this again. Or pair without --insecure-http and use the HTTPS block. No code was made",
				endpoint.Listen, FlagParticipantInsecureHTTP)
		case endpoint.Class == "loopback":
			return fmt.Errorf("gateway: --insecure-http does not apply: the participant listener at %s is loopback, which is plain HTTP already. Pair without --insecure-http. No code was made", endpoint.Listen)
		}
	}

	existing, found, err := store.GetParticipant(ctx, id)
	if err != nil {
		return err
	}

	code, err := store.CreateParticipantCode(ctx, id, now)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, code)
	fmt.Fprintf(out, "participant: %s\n", id)
	fmt.Fprintln(out, "expires in 10 minutes; single use")
	if found && !existing.Revoked() {
		fmt.Fprintln(out, "this is a rotation: redeeming the code replaces the credential and ends this participant's open sessions")
	}
	// The recorded address is what was BOUND. A wildcard bind is never printed as a dial target: the line
	// carries a suggested local address (or a visible placeholder), and the advice comes after the pin lines.
	address, others := DialAddresses(endpoint.Listen)
	hints := dialHintLines(endpoint.Listen, address, others)
	if address == "" {
		address = dialPlaceholder(endpoint.Listen)
	}
	fmt.Fprintf(out, "participant API: %s://%s\n", endpoint.Scheme, address)
	if endpoint.Scheme == schemeHTTPS {
		fmt.Fprintf(out, "certificate SHA-256 (the pin to use when your TLS library can pin a certificate): %s\n", endpoint.Fingerprint)
		fmt.Fprintf(out, "key pin (a different rule, for clients that can pin only a public key): %s\n", endpoint.SPKIPin)
	} else if endpoint.Class != "loopback" {
		fmt.Fprintln(out, ParticipantPlainHTTPWarning)
	}
	for _, hint := range hints {
		fmt.Fprintln(out, hint)
	}
	fmt.Fprintf(out, "the address above was recorded by the running gateway at its start; if the gateway has stopped or moved, it is stale (checked: %s)\n", participantEndpointName)
	return nil
}

// runParticipantList shows every participant. The command cannot see a parked poll (that is in the gateway's
// memory), so "live" means a poll began or ended within the 15 s window, the same rule the gateway applies.
func runParticipantList(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant list", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: participant list: %w\n%s", err, participantUsage)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gateway: participant list takes no arguments\n%s", participantUsage)
	}

	rows, err := store.Participants(ctx)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Fprintln(out, "no participants")
		return nil
	}

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ID\tSTATE\tPAIRED\tLAST POLL\tLIVE\tSESSIONS")
	for _, row := range rows {
		state, live := "enrolled", "no"
		if row.Revoked() {
			state = "revoked"
		} else if HeartbeatFresh(row.LastPoll, now) {
			live = "yes"
		}
		lastPoll := "never"
		if !row.LastPoll.IsZero() {
			lastPoll = row.LastPoll.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\t%d\n", row.ID, state, row.PairedAt.Local().Format(time.RFC3339), lastPoll, live, row.OpenSessions)
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("gateway: write participants: %w", err)
	}
	return nil
}

// runParticipantRevoke revokes a participant and says exactly what it ended.
func runParticipantRevoke(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant revoke", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: participant revoke: %w\n%s", err, participantUsage)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("gateway: participant revoke needs exactly one ID\n%s", participantUsage)
	}
	id := flags.Arg(0)

	revocation, err := store.RevokeParticipant(ctx, id, now)
	switch {
	case errors.Is(err, ErrNoSuchParticipant):
		return fmt.Errorf("gateway: %s is not an active participant", id)
	case err != nil:
		return err
	}

	fmt.Fprintf(out, "revoked %s: ended %d open session(s), removed %d unused pairing code(s); the name stays reserved\n",
		id, revocation.SessionsEnded, revocation.CodesRemoved)
	fmt.Fprintf(out, "its credential is refused from the next request; a poll parked in the running gateway notices within about %s and is answered 401, and offers it had not yet taken are dropped\n",
		participantRevokeCheck)
	fmt.Fprintln(out, "hosts learn of the change at the gateway's next directory push (this command runs in another process and cannot trigger one)")
	return nil
}
