package main

// The reference agent: it counts the lines, words and characters of a text.
//
// It is deliberately model-free. Its only cleverness is one clarifying question: a caller who has not
// said whether they want JSON or plain text is asked, the task waits in INPUT_REQUIRED, and the answer
// arrives as a follow-up message on the same task. Everything else - task storage, GetTask, ListTasks,
// CancelTask, streaming and push refusals, the A2A-Version check, error codes - is the SDK's job.
//
// The functions that decide things are pure, so the behaviour can be tested without a port, a model or
// a network.

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"unicode/utf8"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
)

const (
	// maxBodyBytes is how much text will be counted. Larger bodies are refused rather than counted,
	// because a reference adapter is a demonstration, not a service.
	maxBodyBytes = 64 << 10

	// questionText is what the caller is asked when the format is not known yet.
	questionText = `Which format do you want: json or text? Reply with the line "format: json" or "format: text".`

	// formatJSON and formatText are the only answers the question accepts.
	formatJSON = "json"
	formatText = "text"

	// formatPrefix is the line a caller writes to answer the question.
	formatPrefix = "format:"

	// artifactID names the one artifact a finished task carries.
	artifactID = "counts"
)

// counts is what the adapter reports.
type counts struct {
	lines      int
	words      int
	characters int
}

// countExecutor is the AgentExecutor: it decides between asking, refusing and counting.
type countExecutor struct{}

// Execute runs one message.
//
// The order is: reject a non-text part (a protocol-level refusal, not a task), find the format line in
// the CURRENT message, choose the body (this message's other lines, or the last earlier user message on
// the task), then ask, refuse or count.
func (e *countExecutor) Execute(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		text, err := messageText(ec.Message)
		if err != nil {
			// A non-text part is refused at the protocol level: nothing is created for it.
			yield(nil, err)
			return
		}

		// A new task is announced once. A follow-up on an existing task (the SDK loads it into
		// StoredTask) must not yield a fresh task, which would replace its history.
		if ec.StoredTask == nil {
			if !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
		}

		format, currentBody := splitFormat(text)
		if format == "" {
			// Still no answer to the question: ask again and leave the task waiting.
			question := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(questionText))
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateInputRequired, question), nil)
			return
		}

		body, ok := chooseBody(currentBody, ec)
		if !ok {
			yield(failedStatus(ec, "Nothing to count."), nil)
			return
		}
		if len(body) > maxBodyBytes {
			yield(failedStatus(ec, "Too large."), nil)
			return
		}

		result := countText(body)
		var part *a2a.Part
		if format == formatJSON {
			part = jsonCountsPart(result)
		} else {
			part = textCountsPart(result)
		}

		if !yield(a2a.NewArtifactEvent(ec, part), nil) {
			return
		}
		yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
	}
}

// Cancel refuses every cancellation.
//
// This adapter finishes immediately or waits for a caller, so there is nothing to interrupt; the
// contract chooses the SDK's own "not cancelable" answer rather than inventing a CANCELED state.
func (e *countExecutor) Cancel(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
	return func(yield func(a2a.Event, error) bool) {
		yield(nil, a2a.ErrTaskNotCancelable)
	}
}

// failedStatus is a terminal FAILED update carrying our sentence.
func failedStatus(ec *a2asrv.ExecutorContext, text string) a2a.Event {
	message := a2a.NewMessageForTask(a2a.MessageRoleAgent, ec, a2a.NewTextPart(text))
	return a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, message)
}

// messageText joins a message's text parts with newlines, and refuses a message with any other kind of
// part: this agent counts text.
func messageText(message *a2a.Message) (string, error) {
	if message == nil {
		return "", nil
	}

	texts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part == nil {
			continue
		}
		if _, ok := part.Content.(a2a.Text); !ok {
			return "", a2a.ErrUnsupportedContentType
		}
		texts = append(texts, part.Text())
	}
	return strings.Join(texts, "\n"), nil
}

// splitFormat finds the format line in one message and returns the remaining body.
//
// The first line whose trimmed, lower-cased text is exactly "format: json" or "format: text" wins, and
// it is removed from the body. Any other line - including "format:yaml" - is body.
func splitFormat(text string) (string, string) {
	lines := strings.Split(text, "\n")
	body := make([]string, 0, len(lines))

	for index, line := range lines {
		if format, ok := parseFormatLine(line); ok {
			// Everything except this line is the body, in order.
			body = append(body, lines[:index]...)
			body = append(body, lines[index+1:]...)
			return format, strings.Join(body, "\n")
		}
	}
	return "", text
}

// parseFormatLine reports the format a single line names.
func parseFormatLine(line string) (string, bool) {
	normalised := strings.ToLower(strings.TrimSpace(line))
	switch normalised {
	case "format: json":
		return formatJSON, true
	case "format: text":
		return formatText, true
	}
	return "", false
}

// chooseBody decides what to count.
//
// The current message's own non-format lines win when any of them has content; otherwise the last
// EARLIER user message on the task supplies it, which is the text the question was about. The current
// message is skipped by id, because the SDK appends it to the task's history before the executor runs.
func chooseBody(current string, ec *a2asrv.ExecutorContext) (string, bool) {
	if strings.TrimSpace(current) != "" {
		return current, true
	}

	if ec == nil || ec.StoredTask == nil {
		return "", false
	}

	currentID := ""
	if ec.Message != nil {
		currentID = ec.Message.ID
	}

	for index := len(ec.StoredTask.History) - 1; index >= 0; index-- {
		message := ec.StoredTask.History[index]
		if message == nil || message.Role != a2a.MessageRoleUser {
			continue
		}
		if currentID != "" && message.ID == currentID {
			// The current message, already in history: not the earlier one we want.
			continue
		}
		text, err := messageText(message)
		if err != nil {
			continue
		}
		if _, body := splitFormat(text); strings.TrimSpace(body) != "" {
			return body, true
		}
	}
	return "", false
}

// countText counts lines, words and characters.
//
// One trailing newline does not start a new line: "a" and "a\n" are both one line. CRLF is not
// normalised - the "\r" stays and counts as a character - because this adapter reports what it was
// given, not what an editor would have written.
func countText(body string) counts {
	trimmed := strings.TrimSuffix(body, "\n")
	return counts{
		lines:      1 + strings.Count(trimmed, "\n"),
		words:      len(strings.Fields(body)),
		characters: utf8.RuneCountInString(body),
	}
}

// jsonCountsPart is the JSON answer: one data part with three numbers.
func jsonCountsPart(result counts) *a2a.Part {
	return a2a.NewDataPart(map[string]any{
		"lines":      result.lines,
		"words":      result.words,
		"characters": result.characters,
	})
}

// textCountsPart is the plain-text answer.
func textCountsPart(result counts) *a2a.Part {
	return a2a.NewTextPart(fmt.Sprintf("lines: %d\nwords: %d\ncharacters: %d",
		result.lines, result.words, result.characters))
}
