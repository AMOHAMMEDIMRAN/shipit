// Package ui is everything shipit shows and asks. The rules it follows:
// one question per screen, the likeliest answer pre-selected (Enter accepts),
// "?" explains the question, plain words, and errors that say what happened,
// why, and what to do next.
package ui

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"
)

// Assume answers every question with its default. It is set by --yes and used
// automatically when nothing is attached to type into.
var Assume bool

var (
	in            = bufio.NewReader(os.Stdin)
	out io.Writer = os.Stdout
	err io.Writer = os.Stderr
)

// ErrCancelled means the person stopped (Ctrl-D or typed q).
var ErrCancelled = errors.New("cancelled")

// Interactive is true when a person can answer questions.
func Interactive() bool { return !Assume && term.IsTerminal(int(os.Stdin.Fd())) }

func color() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	return term.IsTerminal(int(os.Stdout.Fd()))
}

func paint(code, s string) string {
	if !color() {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// Bold, Dim, Green, Yellow and Red keep colour use in one place.
func Bold(s string) string   { return paint("1", s) }
func Dim(s string) string    { return paint("2", s) }
func Green(s string) string  { return paint("32", s) }
func Yellow(s string) string { return paint("33", s) }
func Red(s string) string    { return paint("31", s) }

// SetOutput redirects output, for tests.
func SetOutput(o, e io.Writer) { out, err = o, e }

// SetInput replaces input, for tests.
func SetInput(r io.Reader) { in = bufio.NewReader(r) }

// Say prints a plain line.
func Say(format string, a ...any) { fmt.Fprintf(out, format+"\n", a...) }

// OK, Warn and Note print status lines. The words carry the meaning, so a
// terminal without colour loses nothing.
func OK(format string, a ...any)   { Say("%s %s", Green("✓"), fmt.Sprintf(format, a...)) }
func Warn(format string, a ...any) { Say("%s %s", Yellow("!"), fmt.Sprintf(format, a...)) }
func Note(format string, a ...any) { Say("  %s", Dim(fmt.Sprintf(format, a...))) }

// Fail prints an error in three parts: what happened, why, what to do next.
func Fail(what, why, next string) {
	fmt.Fprintf(err, "%s %s\n", Red("✗"), what)
	if why != "" {
		fmt.Fprintf(err, "  %s\n", why)
	}
	if next != "" {
		next = strings.TrimPrefix(next, "Run: ")
		fmt.Fprintf(err, "  Next: %s\n", next)
	}
}

// Step prints the header of a question: where the person is.
func Step(n, total int, title string) {
	Say("")
	Say("%s %s", Dim(fmt.Sprintf("Step %d of %d ·", n, total)), Bold(title))
}

func readLine() (string, error) {
	s, e := in.ReadString('\n')
	if e != nil && s == "" {
		return "", ErrCancelled
	}
	return strings.TrimSpace(s), nil
}

func helpLine(hasHelp bool) string {
	if hasHelp {
		return Dim("Enter to accept · ? for help · q to stop")
	}
	return Dim("Enter to accept · q to stop")
}

func showHelp(help string) {
	Say("")
	for _, l := range strings.Split(strings.TrimSpace(help), "\n") {
		Say("  %s", strings.TrimSpace(l))
	}
	Say("")
}

// Option is one choice in Select.
type Option struct {
	Label string
	Hint  string
	Value string
}

// Select asks the person to pick one. def is the index chosen by Enter.
func Select(title string, opts []Option, def int, help string) (int, error) {
	if len(opts) == 0 {
		return 0, errors.New("nothing to choose from")
	}
	if Assume || !Interactive() {
		return def, nil
	}
	for {
		Say("")
		if title != "" {
			Say("  %s", title)
		}
		w := 0
		for _, o := range opts {
			if len(o.Label) > w {
				w = len(o.Label)
			}
		}
		for i, o := range opts {
			mark := " "
			if i == def {
				mark = "›"
			}
			hint := ""
			if o.Hint != "" {
				hint = "  " + Dim(o.Hint)
			}
			Say("  %s %d  %-*s%s", mark, i+1, w, o.Label, hint)
		}
		Say("  %s", helpLine(help != ""))
		fmt.Fprint(out, "> ")
		s, e := readLine()
		if e != nil {
			return 0, e
		}
		switch {
		case s == "":
			return def, nil
		case s == "?" && help != "":
			showHelp(help)
		case strings.EqualFold(s, "q"):
			return 0, ErrCancelled
		default:
			n, perr := strconv.Atoi(s)
			if perr == nil && n >= 1 && n <= len(opts) {
				return n - 1, nil
			}
			Warn("Type a number from 1 to %d, or press Enter for %d.", len(opts), def+1)
		}
	}
}

// Input asks for a line of text. validate may be nil.
func Input(title, def, help string, validate func(string) error) (string, error) {
	if Assume || !Interactive() {
		if def == "" {
			return "", fmt.Errorf("%q needs an answer, but there is nobody to ask. Pass it as a flag or run in a terminal", title)
		}
		return def, nil
	}
	for {
		Say("")
		if title != "" {
			Say("  %s", title)
		}
		if def != "" {
			Say("  %s", Bold(def))
		}
		Say("  %s", helpLine(help != ""))
		fmt.Fprint(out, "> ")
		s, e := readLine()
		if e != nil {
			return "", e
		}
		switch {
		case s == "?" && help != "":
			showHelp(help)
			continue
		case strings.EqualFold(s, "q"):
			return "", ErrCancelled
		case s == "":
			s = def
		}
		if s == "" {
			Warn("This one needs an answer.")
			continue
		}
		if validate != nil {
			if verr := validate(s); verr != nil {
				Warn("%v", verr)
				continue
			}
		}
		return s, nil
	}
}

// Confirm asks a yes or no question. Enter picks def.
func Confirm(title string, def bool, help string) (bool, error) {
	if Assume {
		return true, nil
	}
	if !Interactive() {
		return def, nil
	}
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	for {
		Say("")
		fmt.Fprintf(out, "  %s %s ", title, Dim("["+hint+"]"))
		if help != "" {
			fmt.Fprintf(out, "%s ", Dim("(? for help)"))
		}
		s, e := readLine()
		if e != nil {
			return false, e
		}
		switch strings.ToLower(s) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "?":
			if help != "" {
				showHelp(help)
				continue
			}
		case "q":
			return false, ErrCancelled
		}
		Warn("Type y or n.")
	}
}

// Secret reads a value without showing it.
func Secret(title string) (string, error) {
	if !Interactive() {
		return "", errors.New("a secret cannot be asked for without a terminal")
	}
	fmt.Fprintf(out, "  %s ", title)
	b, e := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(out)
	if e != nil {
		return "", e
	}
	return string(b), nil
}

// TypeToConfirm makes people type a word before something important happens.
func TypeToConfirm(prompt, word string) bool {
	if Assume {
		return true
	}
	if !Interactive() {
		return false
	}
	fmt.Fprintf(out, "  %s ", prompt)
	s, e := readLine()
	return e == nil && s == word
}
