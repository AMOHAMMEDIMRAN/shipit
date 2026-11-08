package commit

import (
	"errors"
	"strings"

	"github.com/amohammedimran/shipit/internal/ui"
)

// Results the caller can act on.
var (
	ErrStop  = errors.New("stopped")                // the person chose to stop
	ErrLeave = errors.New("deploy the last commit") // the person chose to leave the changes out
)

// FlowOptions says how to behave.
type FlowOptions struct {
	Yes         bool   // accept the message without asking (scripts)
	Auto        bool   // same, set by --commit
	Message     string // use this message instead of writing one
	Cmd         string // external command that writes the message, may be empty
	OfferDeploy bool   // add "deploy the last commit without these changes"
}

func status(f File) string {
	if f.Status == "?" {
		return "A"
	}
	return f.Status
}

// Flow shows the changes, proposes a message, and commits after a yes.
// It returns whether a commit was made.
func Flow(dir string, o FlowOptions) (bool, error) {
	set, err := Scan(dir)
	if err != nil {
		return false, err
	}
	files := set.Committable()
	if len(files) == 0 {
		return false, nil
	}
	unattended := o.Yes || o.Auto
	if !unattended && !ui.Interactive() {
		return false, errors.New("committing needs a person to confirm. Run in a terminal, or add --yes")
	}

	ui.Say("")
	ui.Say("You have changes that are not committed (%d file%s):", len(files), plural(len(files)))
	for i, f := range files {
		if i == 12 {
			ui.Say("  … and %d more", len(files)-12)
			break
		}
		ui.Say("  %s  %-44s %s", status(f), f.Path, ui.Dim(strings.TrimSpace(counts(f))))
	}
	for _, f := range set.Skipped() {
		ui.Say("  %s  %-44s %s", ui.Yellow("-"), f.Path, ui.Dim("left out: looks like a key or settings file"))
	}

	msg := strings.TrimSpace(o.Message)
	if msg == "" {
		if o.Cmd != "" {
			ui.Note("Writing the message with your commit_message_cmd")
			m, gerr := Generate(dir, files, o.Cmd)
			if gerr != nil {
				ui.Warn("That command did not give a message (%v). Using a plain one instead.", gerr)
			} else {
				msg = m
			}
		}
		if msg == "" {
			msg = Suggest(files)
		}
	}

	if !unattended {
		for {
			ui.Say("")
			ui.Say("Suggested message:")
			for _, l := range strings.Split(msg, "\n") {
				ui.Say("  %s", ui.Bold(l))
			}
			opts := []ui.Option{
				{Label: "Commit with this message"},
				{Label: "Change the message"},
			}
			if o.OfferDeploy {
				opts = append(opts, ui.Option{Label: "Deploy the last commit without these changes"})
			}
			opts = append(opts, ui.Option{Label: "Stop"})
			i, serr := ui.Select("", opts, 0, "shipit commits exactly the files listed above, with this message.\nFiles that look like keys or settings are never included.\nChoose 'Change the message' to type your own first line.")
			if serr != nil {
				return false, ErrStop
			}
			choice := opts[i].Label
			switch {
			case strings.HasPrefix(choice, "Commit"):
				goto commit
			case strings.HasPrefix(choice, "Change"):
				first, rest, _ := strings.Cut(msg, "\n")
				nf, ierr := ui.Input("Commit message (first line)", first, "A short summary of what changed, in the imperative: 'Add retry to uploads'.", nil)
				if ierr != nil {
					return false, ErrStop
				}
				msg = strings.TrimSpace(nf + "\n" + rest)
			case strings.HasPrefix(choice, "Deploy"):
				return false, ErrLeave
			default:
				return false, ErrStop
			}
		}
	}
commit:
	if err := Commit(dir, files, msg); err != nil {
		return false, err
	}
	first, _, _ := strings.Cut(msg, "\n")
	ui.OK("Committed: %s", first)
	return true, nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}
