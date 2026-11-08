package gateway

// `rtk gateway participant ...`: the owner enrolling external A2A participants from the shell.
//
// This file is only a CLI over internal/a2a's Admin: it parses, calls one method and prints. Every rule -
// the name shape, the URL rules, the plain-HTTP opt-in, duplicates, revoked participants, task deletion -
// belongs to that package, so nothing here can drift from what the gateway believes.
//
// The one rule of this file's own is about the credential: it is printed exactly once, on stdout, on a
// line of its own, by printCredential, and it is never written to errOut, never repeated in an error, and
// never stored anywhere by this code.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No participant has been enrolled and no task deleted.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"text/tabwriter"
	"time"

	"github.com/mngeow/ratatoskr/internal/a2a"
)

// runParticipant dispatches one `rtk gateway participant` command.
//
// UNTESTED.
func runParticipant(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	if len(args) == 0 {
		return fmt.Errorf("gateway: participant needs enrol, register, list, revoke or delete-tasks\n%s", adminUsage)
	}

	switch args[0] {
	case "enrol":
		return runParticipantEnrol(ctx, store, args[1:], out, errOut, now)
	case "register":
		return runParticipantRegister(ctx, store, args[1:], out, errOut, now)
	case "list":
		return runParticipantList(ctx, store, args[1:], out, errOut)
	case "revoke":
		return runParticipantRevoke(ctx, store, args[1:], out, errOut, now)
	case "delete-tasks":
		return runParticipantDeleteTasks(ctx, store, args[1:], out, errOut)
	}

	return fmt.Errorf("gateway: unknown participant command %q\n%s", args[0], adminUsage)
}

// runParticipantEnrol creates a participant and prints its credential once.
func runParticipantEnrol(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant enrol", errOut)
	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: participant enrol: %w\n%s", err, adminUsage)
	}
	if len(positional) != 1 {
		return fmt.Errorf("gateway: participant enrol needs NAME\n%s", adminUsage)
	}

	name := a2a.NormalizeName(positional[0])

	credential, err := newA2AAdmin(store, func() time.Time { return now }).Enrol(ctx, name)
	switch {
	case errors.Is(err, a2a.ErrBadName):
		return fmt.Errorf("gateway: %s is not a valid participant name (lowercase letters, digits, - and _, starts with a letter, at most 32)", name)
	case errors.Is(err, a2a.ErrExists):
		return fmt.Errorf("gateway: %s is already enrolled", name)
	case err != nil:
		return fmt.Errorf("gateway: participant enrol: %w", err)
	}

	// The credential goes out first and alone: if a later write fails, the caller still has it, and it is
	// never repeated.
	if err := printCredential(out, credential); err != nil {
		return err
	}
	fmt.Fprintf(out, "participant: %s\n", name)
	fmt.Fprintln(out, "this credential is shown once and cannot be recovered; store it where the participant can read it")
	return nil
}

// printCredential writes the one secret this command produces.
//
// It is the only place in this file that the credential value reaches, so a future edit cannot quietly
// print it again somewhere else.
func printCredential(out io.Writer, credential string) error {
	if _, err := fmt.Fprintln(out, credential); err != nil {
		// The error deliberately names no value: a failed write must not repeat the secret.
		return errors.New("gateway: the participant credential could not be written")
	}
	return nil
}

// runParticipantRegister sets where a participant's card is fetched from.
func runParticipantRegister(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant register", errOut)
	rawURL := flags.String("url", "", "")
	fingerprint := flags.String("fingerprint", "", "")
	insecureHTTP := flags.Bool("insecure-http", false, "")
	noAuth := flags.Bool("no-auth", false, "")

	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: participant register: %w\n%s", err, adminUsage)
	}
	if len(positional) != 1 || *rawURL == "" {
		return fmt.Errorf("gateway: participant register needs NAME and --url URL\n%s", adminUsage)
	}
	name := a2a.NormalizeName(positional[0])

	result, err := newA2AAdmin(store, func() time.Time { return now }).Register(ctx, name, *rawURL, *fingerprint, *insecureHTTP, *noAuth)
	switch {
	case errors.Is(err, a2a.ErrNotFound):
		return fmt.Errorf("gateway: %s is not enrolled (run: rtk gateway participant enrol %s)", name, name)
	case errors.Is(err, a2a.ErrRevoked):
		return fmt.Errorf("gateway: %s is revoked", name)
	case errors.Is(err, a2a.ErrNeedsOptIn):
		return errors.New("gateway: a plain http LAN address needs --insecure-http")
	case errors.Is(err, a2a.ErrNoAuthDeclared):
		return errors.New("gateway: that service's card declares no authentication. A2A requires a service to authenticate its callers; fix the service, or register it with --no-auth for a demonstration only")
	case errors.Is(err, a2a.ErrUnsupportedAuth):
		return errors.New("gateway: that service's card requires an authentication scheme this gateway cannot use; it can send HTTP Bearer only")
	case errors.Is(err, a2a.ErrAuthMismatch):
		return errors.New("gateway: that service's card declares Bearer authentication; register it without --no-auth")
	case err != nil:
		return fmt.Errorf("gateway: participant register: %w", err)
	}

	fmt.Fprintf(out, "registered %s at %s\n", name, *rawURL)
	if result.Secret != "" {
		// The secret goes out first among the sensitive lines, once, on its own line.
		if err := printSecret(out, result.Secret); err != nil {
			return err
		}
		fmt.Fprintln(out, "this outbound secret is shown once and cannot be recovered; put it in the service's token configuration")
		if result.Rotated {
			fmt.Fprintln(out, "an earlier registration existed: its secret no longer works")
		}
	} else {
		fmt.Fprintln(out, "WARNING: --no-auth registers this service WITHOUT authentication. The gateway sends no credential and anyone who can reach the service can call it. This is a demonstration mode, not the authenticated path.")
	}
	if result.PlainHTTPLAN {
		fmt.Fprintln(out, "WARNING: this registration uses plain http on a LAN address: the secret (if any) and every task cross that network in the clear.")
	}
	return nil
}

// printSecret writes the outbound secret. It is the only place in this file that the value reaches, like
// printCredential for the enrolment credential.
func printSecret(out io.Writer, secret string) error {
	if _, err := fmt.Fprintln(out, secret); err != nil {
		return errors.New("gateway: the outbound secret could not be written")
	}
	return nil
}

// urlScheme reads a URL's scheme, or "" when it cannot be read.
//
// It decides one thing here: whether to print the plain-HTTP warning. The address rules are the
// package's.
func urlScheme(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return parsed.Scheme
}

// runParticipantList prints every enrolled participant.
func runParticipantList(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("participant list", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: participant list: %w\n%s", err, adminUsage)
	}
	if rest := flags.Args(); len(rest) > 0 {
		return fmt.Errorf("gateway: participant list takes no arguments\n%s", adminUsage)
	}

	participants, err := newA2AAdmin(store, time.Now).List(ctx)
	if err != nil {
		return fmt.Errorf("gateway: participant list: %w", err)
	}
	if len(participants) == 0 {
		fmt.Fprintln(out, "no participants enrolled")
		return nil
	}

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSTATE\tAUTH\tURL\tSINCE")
	for _, participant := range participants {
		state := "active"
		if !participant.RevokedAt.IsZero() {
			state = "revoked"
		}
		address := participant.URL
		if address == "" {
			address = "-"
		}
		auth := "-"
		switch participant.Auth {
		case a2a.AuthBearer:
			auth = "bearer"
		case a2a.AuthNone:
			auth = "NONE"
		}
		if participant.URL == "" {
			auth = "-"
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n",
			participant.ID, state, auth, address, participant.CreatedAt.UTC().Format(time.RFC3339))
	}
	return table.Flush()
}

// runParticipantRevoke disables a participant and ends the tasks it is party to.
func runParticipantRevoke(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("participant revoke", errOut)
	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: participant revoke: %w\n%s", err, adminUsage)
	}
	if len(positional) != 1 {
		return fmt.Errorf("gateway: participant revoke needs NAME\n%s", adminUsage)
	}
	name := a2a.NormalizeName(positional[0])

	// The finisher is what makes the count true: without it, Revoke would report tasks as ended that were
	// not. Both use the same clock the command was given.
	clock := func() time.Time { return now }
	ended, err := newA2AAdmin(store, clock).Revoke(ctx, name, a2aFinisher(store, clock))
	switch {
	case errors.Is(err, a2a.ErrNotFound):
		return fmt.Errorf("gateway: %s is not an active participant", name)
	case err != nil:
		return fmt.Errorf("gateway: participant revoke: %w", err)
	}

	fmt.Fprintf(out, "revoked %s; open tasks were ended: %d\n", name, ended)
	return nil
}

// runParticipantDeleteTasks removes tasks from the store.
//
// There is no prompt and no --yes: the flags are the owner's explicit choice, and the flags never default
// to deleting everything.
func runParticipantDeleteTasks(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("participant delete-tasks", errOut)
	taskID := flags.String("task", "", "")
	allFinal := flags.Bool("all-final", false, "")
	participant := flags.String("participant", "", "")

	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: participant delete-tasks: %w\n%s", err, adminUsage)
	}
	if rest := flags.Args(); len(rest) > 0 {
		return fmt.Errorf("gateway: participant delete-tasks takes flags only\n%s", adminUsage)
	}

	chosen := 0
	if *taskID != "" {
		chosen++
	}
	if *allFinal {
		chosen++
	}
	if *participant != "" {
		chosen++
	}
	if chosen != 1 {
		return fmt.Errorf("gateway: participant delete-tasks needs exactly one of --task ID, --all-final or --participant NAME\n%s", adminUsage)
	}

	deleted, err := newA2AAdmin(store, time.Now).DeleteTasks(ctx, a2a.DeleteFilter{
		TaskID:      *taskID,
		AllFinal:    *allFinal,
		Participant: a2a.NormalizeName(*participant),
	})
	if err != nil {
		return fmt.Errorf("gateway: participant delete-tasks: %w", err)
	}

	fmt.Fprintf(out, "deleted %d task(s)\n", deleted)
	return nil
}
