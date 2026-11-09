package gateway

// The admin commands, as one entry point so cmd/rtk only dispatches.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No command has been invoked.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"
)

// followInterval is how often `log --follow` re-reads the ledger.
const followInterval = time.Second // UNTESTED guess

// adminUsage is what a usage error prints.
const adminUsage = `usage:
  rtk gateway pair HOST
  rtk gateway unpair HOST
  rtk gateway hosts
  rtk gateway card-target set HOST URL [--fingerprint sha256:...]
  rtk gateway card-target show [HOST]
  rtk gateway card-target clear HOST
  rtk gateway participant pair ID [--insecure-http]
  rtk gateway participant list
  rtk gateway participant revoke ID
  rtk gateway log [--follow] [--limit N]
  rtk gateway show ID`

// RunAdmin runs one admin command against the gateway database.
//
// It opens the store without the gateway lock, so it works while
// `rtk gateway --listen` is running. Bodies are printed only by log and show;
// nothing here writes a body, a code or a credential to a log.
//
// UNTESTED.
func RunAdmin(ctx context.Context, stateDir string, args []string, out, errOut io.Writer, now func() time.Time) error {
	if now == nil {
		now = time.Now
	}
	if len(args) == 0 {
		return fmt.Errorf("%s", adminUsage)
	}

	command, rest := args[0], args[1:]
	switch command {
	case "pair", "unpair", "hosts", "log", "show", "card-target", "participant":
	default:
		return fmt.Errorf("gateway: unknown command %q\n%s", command, adminUsage)
	}

	store, err := Open(ctx, stateDir, false)
	if err != nil {
		return err
	}
	defer func() {
		_ = store.Close()
	}()

	switch command {
	case "pair":
		return runPair(ctx, store, rest, out, errOut, now())
	case "unpair":
		return runUnpair(ctx, store, rest, out, errOut, now())
	case "hosts":
		return runHosts(ctx, store, rest, out, errOut, now())
	case "log":
		return runLog(ctx, store, rest, out, errOut)
	case "card-target":
		return runCardTarget(ctx, store, rest, out, errOut, now())
	case "participant":
		return runParticipant(ctx, store, rest, out, errOut, now())
	default:
		return runShow(ctx, store, rest, out, errOut)
	}
}

// flagSet is the shared flag setup: errors go to errOut and never exit the
// process.
func flagSet(name string, errOut io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(errOut)
	return flags
}

func runPair(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("pair", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: pair: %w\n%s", err, adminUsage)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("gateway: pair needs exactly one HOST\n%s", adminUsage)
	}
	host := flags.Arg(0)

	code, err := store.CreatePairingCode(ctx, host, now)
	if err != nil {
		return err
	}

	fmt.Fprintln(out, code)
	fmt.Fprintf(out, "host: %s\n", host)
	fmt.Fprintln(out, "expires in 10 minutes; single use")
	fmt.Fprintln(out, readyLine(store.dir, code))
	return nil
}

// readyLine is the command to run on the paired machine.
//
// When the gateway recorded its endpoint, the line carries the real address and the
// transport flag the runtime must type: --fingerprint for HTTPS, --insecure-http for a
// plain-HTTP LAN gateway, and nothing for Tailscale or loopback. Without a usable
// endpoint - this gateway process may not be running, or the file may be old - the
// address stays the literal placeholder ADDR, because guessing would be worse.
//
// UNTESTED.
func readyLine(dir, code string) string {
	const prefix = "on that machine: rtk runtime pair --gateway "

	endpoint, ok := LoadEndpoint(dir)
	if !ok {
		return prefix + "ADDR --code " + code
	}

	// An IPv6 literal has brackets, which zsh (the macOS default) treats as a glob and
	// refuses with "no matches found", so the address is single-quoted. IPv4 is not.
	address := endpoint.Listen
	if strings.Contains(address, "[") {
		address = "'" + address + "'"
	}
	line := prefix + address + " --code " + code
	switch {
	case endpoint.Scheme == schemeHTTPS:
		line += " --fingerprint " + endpoint.Fingerprint
	case endpoint.LAN:
		line += " --insecure-http"
	}
	return line
}

func runUnpair(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("unpair", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: unpair: %w\n%s", err, adminUsage)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("gateway: unpair needs exactly one HOST\n%s", adminUsage)
	}
	host := flags.Arg(0)

	if err := store.Unpair(ctx, host, now); err != nil {
		return err
	}
	fmt.Fprintf(out, "unpaired %s; its requests are refused now and its open stream closes within one ping interval (5 s)\n", host)
	return nil
}

func runHosts(ctx context.Context, store *Store, args []string, out, errOut io.Writer, now time.Time) error {
	flags := flagSet("hosts", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: hosts: %w\n%s", err, adminUsage)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gateway: hosts takes no arguments\n%s", adminUsage)
	}

	hosts, err := store.Hosts(ctx)
	if err != nil {
		return err
	}
	codes, err := store.PendingCodes(ctx, now)
	if err != nil {
		return err
	}

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "NAME\tSTATE\tPAIRED")
	for _, host := range hosts {
		fmt.Fprintf(table, "%s\t%s\t%s\n", host.Name, hostState(host, now), host.PairedAt.Local().Format(time.RFC3339))
	}
	if err := table.Flush(); err != nil {
		return fmt.Errorf("gateway: write hosts: %w", err)
	}

	for _, code := range codes {
		fmt.Fprintf(out, "pending  %s  expires in %s\n", code.Host, code.ExpiresAt.Sub(now).Round(time.Second))
	}
	return nil
}

// hostState renders the freshness rule for one host. It asks HeartbeatFresh
// rather than re-implementing the window.
func hostState(host HostRow, now time.Time) string {
	if HeartbeatFresh(host.LastHeartbeat, now) {
		return "live"
	}
	if host.LastHeartbeat.IsZero() {
		return "never heard"
	}
	if host.LastHeartbeat.After(now) {
		return "last heard in the future (clock?)"
	}
	return "last heard " + now.Sub(host.LastHeartbeat).Round(time.Second).String() + " ago"
}

func runLog(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("log", errOut)
	follow := flags.Bool("follow", false, "print new rows as they arrive")
	limit := flags.Int("limit", 20, "how many rows to show first")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: log: %w\n%s", err, adminUsage)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("gateway: log takes no arguments\n%s", adminUsage)
	}

	messages, err := store.Tail(ctx, *limit)
	if err != nil {
		return err
	}
	var lastSeq int64
	for _, message := range messages {
		if err := printLogRow(out, message); err != nil {
			return err
		}
		lastSeq = message.Seq
	}

	if !*follow {
		return nil
	}

	ticker := time.NewTicker(followInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			newMessages, err := store.Log(ctx, lastSeq, defaultLogLimit)
			if err != nil {
				return err
			}
			for _, message := range newMessages {
				if err := printLogRow(out, message); err != nil {
					return err
				}
				lastSeq = message.Seq
			}
		}
	}
}

func runShow(ctx context.Context, store *Store, args []string, out, errOut io.Writer) error {
	flags := flagSet("show", errOut)
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("gateway: show: %w\n%s", err, adminUsage)
	}
	if flags.NArg() != 1 {
		return fmt.Errorf("gateway: show needs exactly one ID\n%s", adminUsage)
	}

	message, events, err := store.Message(ctx, flags.Arg(0))
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "ID: %s\n", oneLine(message.ID))
	fmt.Fprintf(out, "from: %s\n", oneLine(message.From))
	fmt.Fprintf(out, "to: %s\n", oneLine(message.To))
	if message.ReplyTo != "" {
		fmt.Fprintf(out, "reply-to: %s\n", oneLine(message.ReplyTo))
	}
	fmt.Fprintf(out, "result: %s\n", oneLine(message.Result))
	if message.Report != "" {
		report := oneLine(message.Report)
		if message.ToSessionEnded {
			report += " (session ended)"
		}
		fmt.Fprintf(out, "report: %s\n", report)
	}
	if message.ReportReason != "" {
		fmt.Fprintf(out, "report reason: %s\n", oneLine(message.ReportReason))
	}
	fmt.Fprintf(out, "created: %s\n", message.CreatedAt.Local().Format(time.RFC3339))
	fmt.Fprintf(out, "updated: %s\n", message.UpdatedAt.Local().Format(time.RFC3339))

	fmt.Fprintln(out)
	fmt.Fprint(out, bodyForDisplay(message.Body))

	fmt.Fprintln(out)
	fmt.Fprintln(out, "events:")
	for _, event := range events {
		// One line per event: a value or reason that carried a newline could
		// otherwise forge an extra event line (a host's report text is not trusted).
		line := fmt.Sprintf("  %s %s %s", event.At.Local().Format(time.RFC3339), oneLine(event.Kind), oneLine(event.Value))
		if event.Reason != "" {
			line += " " + oneLine(event.Reason)
		}
		fmt.Fprintln(out, strings.TrimRight(line, " "))
	}
	return nil
}

// printLogRow writes one ledger line: id, time, addresses, result, an optional
// report and a short preview.
func printLogRow(out io.Writer, message MessageRow) error {
	line := fmt.Sprintf("%s %s %s -> %s %s",
		oneLine(message.ID), message.CreatedAt.Local().Format(time.RFC3339),
		oneLine(message.From), oneLine(message.To), oneLine(message.Result))
	if message.Report != "" {
		report := oneLine(message.Report)
		if message.ToSessionEnded {
			report += " (session ended)"
		}
		line += " [report " + report + "]"
	}
	line += " " + preview(message.Body)
	_, err := fmt.Fprintln(out, line)
	if err != nil {
		return fmt.Errorf("gateway: write log: %w", err)
	}
	return nil
}

// preview renders one body as a single short line for the log.
//
// It is a deliberate copy of the runtime view's rule rather than a second policy:
// this package must not import the router, and two different previews of the same
// message would be a bug of its own.
func preview(body string) string {
	var builder strings.Builder
	builder.Grow(len(body))

	space := false
	for _, r := range body {
		if r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f) {
			r = ' '
		}
		if r == ' ' {
			if builder.Len() == 0 || space {
				continue
			}
			space = true
			builder.WriteRune(r)
			continue
		}
		space = false
		builder.WriteRune(r)
	}

	trimmed := strings.TrimSpace(builder.String())
	if utf8.RuneCountInString(trimmed) <= 80 {
		return trimmed
	}

	count := 0
	for index := range trimmed {
		if count == 80 {
			return trimmed[:index] + "…"
		}
		count++
	}
	return trimmed
}

// bodyForDisplay keeps a body readable: control characters are removed, newlines
// and tabs are kept because a message may be several lines.
func bodyForDisplay(body string) string {
	var builder strings.Builder
	builder.Grow(len(body))

	for _, r := range body {
		switch {
		case r == '\n' || r == '\t':
			builder.WriteRune(r)
		case r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f):
			// Dropped: a body must not be able to move a terminal cursor.
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// oneLine makes any stored field safe to print on one line: every control
// character, newline and tab included, becomes a space and runs of spaces
// collapse. Header fields, reasons and reports come from a sender or a peer host
// and are not trusted, so none of them is printed raw.
func oneLine(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))

	space := false
	for _, r := range value {
		if r == 0x7f || r < 0x20 || (r >= 0x80 && r <= 0x9f) {
			r = ' '
		}
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		builder.WriteRune(r)
	}
	return strings.TrimSpace(builder.String())
}
