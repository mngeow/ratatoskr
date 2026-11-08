package gateway

// `rtk gateway card-target set|show|clear`: the owner registers where the gateway polls a host's card.
//
// The owner types the target here, on the gateway host. It is never read from a card, a message, a
// report or a skill, so a host cannot change where it is polled. Changing a target clears the host's
// stored card (it came from a different endpoint); setting the identical target again resumes a host
// paused on a certificate mismatch.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/mngeow/ratatoskr/internal/protocol"
)

func runCardTarget(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	if len(args) == 0 {
		return fmt.Errorf("gateway: card-target needs set, show or clear\n%s", adminUsage)
	}
	switch args[0] {
	case "set":
		return runCardTargetSet(ctx, store, args[1:], out, errOut, now)
	case "show":
		return runCardTargetShow(ctx, store, args[1:], out, errOut)
	case "clear":
		return runCardTargetClear(ctx, store, args[1:], out, errOut)
	}
	return fmt.Errorf("gateway: unknown card-target command %q\n%s", args[0], adminUsage)
}

func runCardTargetSet(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("card-target set", errOut)
	fingerprint := flags.String("fingerprint", "", "")
	insecureHTTP := flags.Bool("insecure-http", false, "")

	// The flags may follow the two positional arguments, as in the usage text.
	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: card-target set: %w\n%s", err, adminUsage)
	}
	if len(positional) != 2 {
		return fmt.Errorf("gateway: card-target set needs HOST and URL\n%s", adminUsage)
	}
	host, rawURL := positional[0], positional[1]

	target, err := ParseCardTarget(rawURL, *fingerprint, *insecureHTTP)
	if err != nil {
		return err
	}
	target.Host = host

	changed, err := store.SetCardTarget(ctx, target, now)
	switch {
	case errors.Is(err, ErrNoSuchHost):
		return fmt.Errorf("gateway: %s is not a paired host", host)
	case err != nil:
		return err
	}

	switch {
	case changed:
		fmt.Fprintf(out, "card target for %s set to %s; its stored card was cleared; polling picks this up within a second\n", host, target.URL())
	default:
		fmt.Fprintf(out, "card target for %s is unchanged (%s); polling resumes if it was paused\n", host, target.URL())
	}
	if target.Scheme == "http" && target.Class == ClassLAN {
		fmt.Fprintln(out, PlainHTTPWarning)
	}
	return nil
}

func runCardTargetShow(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("card-target show", errOut)
	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: card-target show: %w\n%s", err, adminUsage)
	}
	if len(positional) > 1 {
		return fmt.Errorf("gateway: card-target show takes at most one HOST\n%s", adminUsage)
	}

	targets, unreadable, err := store.CardTargets(ctx)
	if err != nil {
		return err
	}
	rows, err := store.CardRows(ctx)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "HOST\tURL\tPIN\tLAST RESULT\tLAST OK")
	shown := 0
	for _, target := range targets {
		if len(positional) == 1 && target.Host != positional[0] {
			continue
		}
		shown++
		pin := "-"
		if target.Fingerprint != "" {
			pin = target.Fingerprint
		}
		row := rows[target.Host]
		result := row.LastResult
		if result == "" {
			result = "not polled yet"
		}
		lastOK := "never"
		if !row.LastOKAt.IsZero() {
			lastOK = row.LastOKAt.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(table, "%s\t%s\t%s\t%s\t%s\n", target.Host, target.URL(), pin, result, lastOK)
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("gateway: write card targets: %w", err)
	}
	for _, host := range unreadable {
		fmt.Fprintf(out, "unreadable target row for %s (skipped by the poller)\n", cleanLog(host))
	}
	if shown == 0 && len(positional) == 1 {
		return fmt.Errorf("gateway: %s has no card target", positional[0])
	}
	fmt.Fprintf(out, "a poll result says the card endpoint answered; it says nothing about sessions (%s)\n", protocol.CardPath)
	return nil
}

func runCardTargetClear(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("card-target clear", errOut)
	positional, err := splitPositionals(flags, args)
	if err != nil {
		return fmt.Errorf("gateway: card-target clear: %w\n%s", err, adminUsage)
	}
	if len(positional) != 1 {
		return fmt.Errorf("gateway: card-target clear needs exactly one HOST\n%s", adminUsage)
	}

	switch err := store.ClearCardTarget(ctx, positional[0]); {
	case errors.Is(err, ErrNoSuchCardTarget):
		return fmt.Errorf("gateway: %s has no card target", positional[0])
	case err != nil:
		return err
	}
	fmt.Fprintf(out, "card target for %s cleared, with its stored card, revisions and poll history\n", positional[0])
	return nil
}

// splitPositionals parses flags that may come before or after the positional arguments, which the
// standard flag package does not do by itself: it stops at the first positional.
func splitPositionals(flags interface {
	Parse([]string) error
	Args() []string
}, args []string) ([]string, error) {
	var positional []string
	remaining := args
	for {
		if err := flags.Parse(remaining); err != nil {
			return nil, err
		}
		rest := flags.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		positional = append(positional, rest[0])
		remaining = rest[1:]
	}
}
