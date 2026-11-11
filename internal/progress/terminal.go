package progress

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

type terminalSink struct {
	mu sync.Mutex
	w  io.Writer
}

// NewTerminalSink returns a Sink that prints events as readable lines to w.
func NewTerminalSink(w io.Writer) Sink {
	return &terminalSink{w: w}
}

func (s *terminalSink) Emit(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var err error

	switch ev.Kind {
	case EventStepStarted:
		_, err = fmt.Fprintf(s.w, "==> %s\n", ev.Step)
	case EventStepProgress:
		if ev.Total > 0 {
			_, err = fmt.Fprintf(s.w, "    %s: %d/%d %s\n", ev.Step, ev.Current, ev.Total, ev.Message)
		} else {
			_, err = fmt.Fprintf(s.w, "    %s: %s\n", ev.Step, ev.Message)
		}
	case EventStepFinished:
		_, err = fmt.Fprintf(s.w, "<== %s %s\n", ev.Step, ev.Outcome)
	case EventWarning:
		_, err = fmt.Fprintf(s.w, "warning: %s\n", ev.Message)
	case EventArtifactProduced:
		if ev.Artifact != nil {
			_, err = fmt.Fprintf(s.w, "    %s: wrote %s (%s)\n", ev.Step, ev.Artifact.Path, ev.Artifact.Name)
		}
	default:
		_, err = fmt.Fprintf(s.w, "%s %s %s\n", ev.Kind, ev.Step, ev.Message)
	}

	return err
}

// maxAnswerLine is the longest answer line, in bytes, that the terminal
// prompter accepts.
const maxAnswerLine = 64 << 10

// ErrAnswerTooLong is returned when an answer line passes the length limit.
var ErrAnswerTooLong = errors.New("progress: answer line is too long")

type terminalPrompter struct {
	in          *bufio.Reader
	out         io.Writer
	interactive bool
}

// NewTerminalPrompter returns a Prompter that writes questions to out and
// reads answers from in. When interactive is false it never touches either,
// answers from the default, and fails when there is none. A sensitive
// question's default is never printed. The reader is used as given, so a
// caller that wants hidden input passes a reader that provides it. A
// terminal prompter is for one goroutine at a time.
func NewTerminalPrompter(in io.Reader, out io.Writer, interactive bool) Prompter {
	return &terminalPrompter{in: bufio.NewReader(in), out: out, interactive: interactive}
}

func (p *terminalPrompter) Ask(q Question) (string, error) {
	if !p.interactive {
		return nonInteractive(q)
	}

	switch q.Kind {
	case QuestionConfirm:
		return p.confirm(q)
	case QuestionChoose:
		return p.choose(q)
	case QuestionText:
		return p.text(q)
	default:
		return "", fmt.Errorf("question %q: unknown kind %q", q.ID, q.Kind)
	}
}

func (p *terminalPrompter) line() (string, error) {
	var buf []byte

	for {
		chunk, err := p.in.ReadSlice('\n')
		buf = append(buf, chunk...)

		if len(buf) > maxAnswerLine {
			return "", fmt.Errorf("read answer: %w", ErrAnswerTooLong)
		}

		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}

		if err != nil && (!errors.Is(err, io.EOF) || len(buf) == 0) {
			return "", fmt.Errorf("read answer: %w", err)
		}

		return strings.TrimSpace(string(buf)), nil
	}
}

func (p *terminalPrompter) confirm(q Question) (string, error) {
	hint := "[y/n]"

	if q.Default != nil && !q.Sensitive {
		if *q.Default == "yes" {
			hint = "[Y/n]"
		} else {
			hint = "[y/N]"
		}
	}

	for {
		if _, err := fmt.Fprintf(p.out, "%s %s ", q.Text, hint); err != nil {
			return "", err
		}

		ans, err := p.line()
		if err != nil {
			return "", err
		}

		switch strings.ToLower(ans) {
		case "y", "yes":
			return "yes", nil
		case "n", "no":
			return "no", nil
		case "":
			if q.Default != nil {
				return *q.Default, nil
			}
		}

		if _, err := fmt.Fprintln(p.out, "Please answer y or n."); err != nil {
			return "", err
		}
	}
}

func (p *terminalPrompter) choose(q Question) (string, error) {
	if len(q.Choices) == 0 {
		return "", fmt.Errorf("question %q: no choices", q.ID)
	}

	if _, err := fmt.Fprintln(p.out, q.Text); err != nil {
		return "", err
	}

	for i, c := range q.Choices {
		if _, err := fmt.Fprintf(p.out, "  %d) %s\n", i+1, c); err != nil {
			return "", err
		}
	}

	for {
		if _, err := fmt.Fprintf(p.out, "Choose 1-%d: ", len(q.Choices)); err != nil {
			return "", err
		}

		ans, err := p.line()
		if err != nil {
			return "", err
		}

		if ans == "" && q.Default != nil {
			return *q.Default, nil
		}

		if n, convErr := strconv.Atoi(ans); convErr == nil && n >= 1 && n <= len(q.Choices) {
			return q.Choices[n-1], nil
		}

		if _, err := fmt.Fprintln(p.out, "Please enter one of the numbers shown."); err != nil {
			return "", err
		}
	}
}

func (p *terminalPrompter) text(q Question) (string, error) {
	prompt := q.Text
	if q.Default != nil && !q.Sensitive {
		prompt += " [" + *q.Default + "]"
	}

	if _, err := fmt.Fprintf(p.out, "%s: ", prompt); err != nil {
		return "", err
	}

	ans, err := p.line()
	if err != nil {
		return "", err
	}

	if ans == "" && q.Default != nil {
		return *q.Default, nil
	}

	return ans, nil
}
