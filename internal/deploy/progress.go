package deploy

import (
	"io"
	"strings"

	"github.com/amohammedimran/shipit/internal/ui"
)

// Progress shows a short checklist and keeps the noisy detail out of sight
// until something fails. The server marks its steps with @@begin, @@done and
// @@fail lines, and everything else it prints is kept as detail.
type Progress struct {
	w       io.Writer
	tty     bool
	cur     string // label of the running step, "" when none
	detail  []string
	partial string
	Verbose bool
	errs    []string // lines the server marked ERROR
	Result  string   // what the server reported at the end, such as a version
}

// Reason is what the server said went wrong, in one line.
func (p *Progress) Reason() string { return strings.Join(p.errs, " ") }

// labels turns the server's step names into what the person sees.
var labels = map[string][2]string{
	"build":   {"Building it", "Built it"},
	"migrate": {"Running the step before going live", "Ran the step before going live"},
	"live":    {"Going live", "It is live"},
	"health":  {"Checking it works", "It works"},
}

// NewProgress writes to w. tty turns on in-place updates.
func NewProgress(w io.Writer, tty bool) *Progress { return &Progress{w: w, tty: tty} }

func (p *Progress) print(s string) { _, _ = io.WriteString(p.w, s) }

// Begin starts a step.
func (p *Progress) Begin(doing string) {
	p.cur = doing
	p.detail = nil
	if p.tty {
		p.print("  " + ui.Yellow("●") + " " + doing)
	} else {
		p.print("  … " + doing + "\n")
	}
}

// Done marks the running step as finished.
func (p *Progress) Done(done string) {
	if p.tty {
		p.print("\r\x1b[K")
	}
	p.print("  " + ui.Green("✓") + " " + done + "\n")
	p.cur = ""
	p.detail = nil
}

// Fail marks the running step as failed and shows what it printed.
func (p *Progress) Fail() {
	if p.tty {
		p.print("\r\x1b[K")
	}
	label := p.cur
	if label == "" {
		label = "The last step"
	}
	p.print("  " + ui.Red("✗") + " " + label + " failed\n")
	p.Flush()
	p.cur = ""
}

// Flush prints the detail kept for the current step.
func (p *Progress) Flush() {
	for _, l := range p.detail {
		p.print("    " + ui.Dim(l) + "\n")
	}
	p.detail = nil
}

// Write receives the server's output.
func (p *Progress) Write(b []byte) (int, error) {
	p.partial += string(b)
	for {
		i := strings.IndexByte(p.partial, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(p.partial[:i], "\r")
		p.partial = p.partial[i+1:]
		p.line(line)
	}
	return len(b), nil
}

func (p *Progress) line(l string) {
	switch {
	case strings.HasPrefix(l, "@@begin "):
		id := strings.TrimPrefix(l, "@@begin ")
		if lb, ok := labels[id]; ok {
			p.Begin(lb[0])
		}
	case strings.HasPrefix(l, "@@done "):
		id := strings.TrimPrefix(l, "@@done ")
		if lb, ok := labels[id]; ok {
			p.Done(lb[1])
		}
	case strings.HasPrefix(l, "@@fail "):
		p.Fail()
	case strings.HasPrefix(l, "@@result "):
		p.Result = strings.TrimPrefix(l, "@@result ")
	case strings.HasPrefix(l, "ERROR: "):
		p.errs = append(p.errs, strings.TrimPrefix(l, "ERROR: "))
	case strings.TrimSpace(l) == "":
	default:
		if p.Verbose {
			if p.tty {
				p.print("\r\x1b[K")
			}
			p.print("    " + ui.Dim(l) + "\n")
			if p.tty && p.cur != "" {
				p.print("  " + ui.Yellow("●") + " " + p.cur)
			}
			return
		}
		p.detail = append(p.detail, l)
		if len(p.detail) > 200 {
			p.detail = p.detail[len(p.detail)-200:]
		}
	}
}

// Failed prints leftover detail when the server stopped without a @@fail marker.
func (p *Progress) Failed() {
	if p.cur != "" {
		p.Fail()
		return
	}
	p.Flush()
}
