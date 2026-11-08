package daemon

// `rtk daemon card ...`: the owner's command-line door to the host card, over the daemon's Unix socket.
//
// It is a thin wrapper: every operation is the same CardOp the window sends, served by the same
// handler, recorded with the caller kind "cli". There is no way to change a card on another host and no
// way to reach the daemon but its own socket.
//
// UNTESTED. Under GR-3 (docs/design/ground-rules.md) nothing here has been run:
// only gofmt, go vet and go build have been applied. No daemon has been asked.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/mngeow/ratatoskr/internal/card"
	"github.com/mngeow/ratatoskr/internal/daemon/wire"
	"github.com/mngeow/ratatoskr/internal/protocol"
)

// cardUsage is what a usage error prints.
const cardUsage = `usage:
  rtk daemon card show [--json]
  rtk daemon card validate
  rtk daemon card publish [--name NAME] [--description-file PATH|-]
  rtk daemon card history [N] [--json]
  rtk daemon card rollback N`

// maxDescriptionFile bounds the file a description is read from.
const maxDescriptionFile = 8 << 10

// CardOp sends one card operation to a running daemon over its control socket.
//
// The view is returned even with a refusal, so a caller can show the current card after a conflict.
//
// UNTESTED.
func CardOp(ctx context.Context, stateDir string, op wire.CardOp) (protocol.CardView, *wire.Error, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeout+controlTimeout)
	defer cancel()

	session, err := dialControl(ctx, stateDir)
	if err != nil {
		return protocol.CardView{}, nil, err
	}
	defer func() {
		_ = session.Close()
	}()

	envelope, err := session.request(wire.TypeCardOp, op, queryTimeout+controlTimeout)
	if err != nil {
		return protocol.CardView{}, nil, err
	}

	var result wire.Result
	if envelope.Type == wire.TypeAck {
		var ack wire.Ack
		if err := wire.Decode(envelope, &ack); err == nil && ack.Error != nil {
			return protocol.CardView{}, ack.Error, nil
		}
		return protocol.CardView{}, nil, errors.New("daemon: the daemon did not answer the card request")
	}
	if envelope.Type != wire.TypeResult {
		return protocol.CardView{}, nil, errors.New("daemon: the daemon did not answer the card request")
	}
	if err := wire.Decode(envelope, &result); err != nil {
		return protocol.CardView{}, nil, err
	}

	var view protocol.CardView
	if result.Card != nil {
		view = *result.Card
	}
	return view, result.Error, nil
}

// RunCard runs one `rtk daemon card` command.
//
// UNTESTED.
func RunCard(ctx context.Context, stateDir string, args []string, stdin io.Reader, out, errOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("rtk: daemon card needs a command\n%s", cardUsage)
	}
	command, rest := args[0], args[1:]

	var err error
	switch command {
	case "show":
		err = runCardShow(ctx, stateDir, rest, out, errOut)
	case "validate":
		err = runCardValidate(ctx, stateDir, rest, out)
	case "publish":
		err = runCardPublish(ctx, stateDir, rest, stdin, out, errOut)
	case "history":
		err = runCardHistory(ctx, stateDir, rest, out, errOut)
	case "rollback":
		err = runCardRollback(ctx, stateDir, rest, out)
	default:
		return fmt.Errorf("rtk: daemon card: unknown command %q\n%s", command, cardUsage)
	}

	if errors.Is(err, ErrNotRunning) {
		return &ExitError{Code: 4, Err: errors.New(noDaemonText)}
	}
	return err
}

// fetchCard reads the current view.
func fetchCard(ctx context.Context, stateDir string) (protocol.CardView, error) {
	view, refusal, err := CardOp(ctx, stateDir, wire.CardOp{Op: wire.CardOpGet})
	if err != nil {
		return protocol.CardView{}, err
	}
	if refusal != nil {
		return protocol.CardView{}, fmt.Errorf("daemon: %s", refusal.Error())
	}
	return view, nil
}

func runCardShow(ctx context.Context, stateDir string, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("card show", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("rtk: daemon card show takes only --json\n%s", cardUsage)
	}

	view, err := fetchCard(ctx, stateDir)
	if err != nil {
		return err
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(view, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}
	return printCard(out, view)
}

// printCard writes the view as text. Fixed words, never a default value for a missing fact.
func printCard(out io.Writer, view protocol.CardView) error {
	fmt.Fprintf(out, "host:        %s\n", orBlank(safeText(view.Host.Name)))
	if view.Published == nil {
		fmt.Fprintln(out, "published:   nothing yet")
	} else {
		fmt.Fprintf(out, "published:   revision %d at %s (by %s)\n", view.Published.N, safeText(view.Published.At), safeText(view.Published.Caller))
	}
	state := "same as published"
	if view.Working.Dirty {
		state = "has unpublished changes"
	}
	fmt.Fprintf(out, "working:     %s\n", state)
	// The working copy can hold text that publish would refuse, written by any process of the owner's,
	// so nothing from it reaches the terminal raw: an escape sequence in a name must not run.
	fmt.Fprintf(out, "name:        %s\n", safeText(view.Working.Card.Name))
	fmt.Fprintf(out, "description: %s\n", safeText(view.Working.Card.Description))
	fmt.Fprintf(out, "declared:    %d skill(s)\n", len(view.Working.Card.Skills))
	fmt.Fprintf(out, "found:       %d skill file(s) (a found skill is evidence of a file, not permission or ability)\n", len(view.Skills.Found))
	if len(view.Skills.NotScanned) > 0 {
		fmt.Fprintf(out, "not scanned: %s\n", strings.Join(view.Skills.NotScanned, ", "))
	}
	if view.Listener.State == protocol.CardListenerOn {
		fmt.Fprintf(out, "listener:    on, %s\n", safeText(view.Listener.URL))
	} else {
		fmt.Fprintln(out, "listener:    off (start the daemon with --card-listen ADDR to serve the card; that ends running sessions if a daemon is already up)")
	}
	fmt.Fprintf(out, "drafting:    %s: %s\n", view.Drafting.State, view.Drafting.Message)
	for _, problem := range view.Working.Problems {
		fmt.Fprintf(out, "problem:     %s: %s\n", safeText(problem.Field), safeText(problem.Message))
	}
	return nil
}

// safeText makes working-copy text safe for a terminal: control characters (including escape and the
// C1 range), line separators and invalid UTF-8 are replaced with a visible "?" so the line stays one
// line and nothing in it can move the cursor, set a title or run a terminal command.
func safeText(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for _, r := range value {
		switch {
		case r == utf8.RuneError, r < 0x20, r == 0x7f, r >= 0x80 && r <= 0x9f, r == 0x2028, r == 0x2029:
			builder.WriteByte('?')
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

func orBlank(value string) string {
	if value == "" {
		return "(none)"
	}
	return value
}

func runCardValidate(ctx context.Context, stateDir string, args []string, out io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("rtk: daemon card validate takes no arguments\n%s", cardUsage)
	}
	view, err := fetchCard(ctx, stateDir)
	if err != nil {
		return err
	}
	if len(view.Working.Problems) == 0 {
		fmt.Fprintln(out, "the working copy is valid")
		return nil
	}
	for _, problem := range view.Working.Problems {
		fmt.Fprintf(out, "%s: %s\n", problem.Field, problem.Message)
	}
	return fmt.Errorf("the working copy has %d problem(s)", len(view.Working.Problems))
}

func runCardPublish(ctx context.Context, stateDir string, args []string, stdin io.Reader, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("card publish", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	name := flags.String("name", "", "")
	descriptionFile := flags.String("description-file", "", "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return fmt.Errorf("rtk: daemon card publish takes --name and --description-file\n%s", cardUsage)
	}

	view, err := fetchCard(ctx, stateDir)
	if err != nil {
		return err
	}

	// Anything the owner gave is applied to the working copy first, against the token just read.
	edit := wire.CardEdit{}
	changed := false
	if *name != "" {
		edit.Name = name
		changed = true
	}
	if *descriptionFile != "" {
		text, err := readDescription(*descriptionFile, stdin)
		if err != nil {
			return err
		}
		edit.Description = &text
		changed = true
	}
	if changed {
		edited, refusal, err := CardOp(ctx, stateDir, wire.CardOp{Op: wire.CardOpEdit, BaseToken: view.Token, Edit: &edit})
		if err != nil {
			return err
		}
		if refusal != nil {
			return fmt.Errorf("daemon: %s", refusal.Error())
		}
		view = edited
	}

	// The card needs a description to be published. Say how to give one before asking the daemon, so the
	// owner is not left with a bare "has problems".
	if view.Working.Card.Description == "" {
		return errors.New("a description is required to publish: give one with --description-file PATH (or - to read it from standard input)")
	}

	published, refusal, err := CardOp(ctx, stateDir, wire.CardOp{Op: wire.CardOpPublish, BaseToken: view.Token})
	if err != nil {
		return err
	}
	if refusal != nil {
		for _, problem := range published.Working.Problems {
			fmt.Fprintf(errOut, "%s: %s\n", problem.Field, problem.Message)
		}
		return fmt.Errorf("daemon: %s", refusal.Error())
	}
	fmt.Fprintf(out, "published revision %d\n", published.Published.N)
	if published.Listener.State != protocol.CardListenerOn {
		fmt.Fprintln(out, "the card listener is off, so nothing serves it yet")
	}
	return nil
}

// readDescription reads the description text from a file, or from standard input for "-". The size is
// bounded, the text must be valid UTF-8, and one trailing newline is dropped.
func readDescription(path string, stdin io.Reader) (string, error) {
	var reader io.Reader
	if path == "-" {
		reader = stdin
	} else {
		file, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("rtk: the description file could not be opened")
		}
		defer func() {
			_ = file.Close()
		}()
		reader = file
	}

	data, err := io.ReadAll(bufio.NewReader(io.LimitReader(reader, maxDescriptionFile+1)))
	if err != nil {
		return "", errors.New("rtk: the description could not be read")
	}
	if len(data) > maxDescriptionFile {
		return "", errors.New("rtk: the description is larger than 8 KiB")
	}
	if !utf8.Valid(data) {
		return "", errors.New("rtk: the description is not valid UTF-8")
	}
	return strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r"), nil
}

func runCardHistory(ctx context.Context, stateDir string, args []string, out, errOut io.Writer) error {
	flags := flag.NewFlagSet("card history", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	asJSON := flags.Bool("json", false, "")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 {
		return fmt.Errorf("rtk: daemon card history takes an optional revision number and --json\n%s", cardUsage)
	}

	op := wire.CardOp{Op: wire.CardOpHistory}
	if flags.NArg() == 1 {
		n, err := strconv.Atoi(flags.Arg(0))
		if err != nil || n < 1 {
			return fmt.Errorf("rtk: %q is not a revision number\n%s", flags.Arg(0), cardUsage)
		}
		op.Revision = n
	}

	view, refusal, err := CardOp(ctx, stateDir, op)
	if err != nil {
		return err
	}
	if refusal != nil {
		return fmt.Errorf("daemon: %s", refusal.Error())
	}

	if view.Preview != nil {
		encoded, err := card.Canonical(view.Preview.Card)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "revision %d, %s, by %s\n%s\n", view.Preview.N, safeText(view.Preview.At), safeText(view.Preview.Caller), string(encoded))
		return nil
	}
	if *asJSON {
		encoded, err := json.MarshalIndent(view.Revisions, "", "  ")
		if err != nil {
			return err
		}
		_, err = fmt.Fprintln(out, string(encoded))
		return err
	}

	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "REVISION\tPUBLISHED\tBY")
	for _, revision := range view.Revisions {
		published := safeText(revision.At)
		if parsed, ok := protocol.ParseTime(revision.At); ok {
			published = parsed.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(table, "%d\t%s\t%s\n", revision.N, published, safeText(revision.Caller))
	}
	return table.Flush()
}

func runCardRollback(ctx context.Context, stateDir string, args []string, out io.Writer) error {
	if len(args) != 1 {
		return fmt.Errorf("rtk: daemon card rollback needs one revision number\n%s", cardUsage)
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n < 1 {
		return fmt.Errorf("rtk: %q is not a revision number\n%s", args[0], cardUsage)
	}

	view, err := fetchCard(ctx, stateDir)
	if err != nil {
		return err
	}
	rolled, refusal, err := CardOp(ctx, stateDir, wire.CardOp{Op: wire.CardOpRollback, BaseToken: view.Token, Revision: n})
	if err != nil {
		return err
	}
	if refusal != nil {
		return fmt.Errorf("daemon: %s", refusal.Error())
	}
	fmt.Fprintf(out, "revision %d was published again as revision %d\n", n, rolled.Published.N)
	return nil
}
