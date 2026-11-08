// Package deploy runs the commands that change or inspect a server:
// deploy, rollback, status, logs, history and settings.
package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/amohammedimran/shipit/internal/archive"
	"github.com/amohammedimran/shipit/internal/commit"
	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/detect"
	"github.com/amohammedimran/shipit/internal/remote"
	"github.com/amohammedimran/shipit/internal/transport"
	"github.com/amohammedimran/shipit/internal/ui"
)

// Options are the switches a command can carry.
type Options struct {
	Yes, DryRun, AllowDirty, WorkingTree, SkipChecks, Verbose bool
	Commit                                                    bool   // commit uncommitted changes first, with a written message
	Message                                                   string // use this commit message
	Tag                                                       string // deploy this git tag
	To                                                        string // roll back to this version
	Lines                                                     int
}

// Deployer works on one environment of one project.
type Deployer struct {
	Dir  string
	Env  *config.Env
	Conn transport.Conn
	Opt  Options
	Out  io.Writer
}

// ErrStopped means the person chose not to continue. Nothing was changed.
var ErrStopped = errors.New("stopped")

func (d *Deployer) out() io.Writer {
	if d.Out != nil {
		return d.Out
	}
	return os.Stdout
}

func (d *Deployer) tty() bool { return d.Out == nil && ui.Interactive() }

// ---------- git ----------

func git(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(e.String()), err
	}
	return strings.TrimSpace(o.String()), nil
}

func isGitRepo(dir string) bool {
	out, err := git(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil && out == "true"
}

var tagUnsafe = regexp.MustCompile(`[^A-Za-z0-9_.-]+`)

// SafeTag makes a version label usable as an image tag and folder name.
func SafeTag(label string) string {
	t := strings.Trim(tagUnsafe.ReplaceAllString(label, "-"), "-.")
	if len(t) > 80 {
		t = t[:80]
	}
	if t == "" {
		t = "release"
	}
	return t
}

// ---------- the plan ----------

type source struct {
	ref, label, describe string
}

func (d *Deployer) chooseSource() (source, error) {
	opt := d.Opt
	e := d.Env
	if opt.WorkingTree || !isGitRepo(d.Dir) {
		label := "worktree-" + time.Now().Format("150405")
		if isGitRepo(d.Dir) {
			if l, err := git(d.Dir, "describe", "--tags", "--always", "--dirty"); err == nil {
				label = l
			}
		}
		return source{"", label, "the working folder, as it is now"}, nil
	}
	if opt.Tag != "" {
		if _, err := git(d.Dir, "rev-parse", "--verify", "--quiet", "refs/tags/"+opt.Tag+"^{commit}"); err != nil {
			return source{}, fmt.Errorf("there is no git tag called %s. List them with: git tag", opt.Tag)
		}
		return source{opt.Tag, opt.Tag, "git tag " + opt.Tag}, nil
	}
	cur, _ := git(d.Dir, "rev-parse", "--abbrev-ref", "HEAD")
	if cur != e.Branch {
		return source{}, fmt.Errorf("%s deploys from the %s branch, but you are on %s. Switch with: git checkout %s", e.Name, e.Branch, cur, e.Branch)
	}
	if st, _ := git(d.Dir, "status", "--porcelain", "--untracked-files=no"); st != "" && !opt.AllowDirty {
		return source{}, fmt.Errorf("you have changes that are not committed. shipit deploys a git commit, so commit first, or add --commit to commit them with a written message, or --allow-dirty to deploy the last commit anyway")
	}
	sha, _ := git(d.Dir, "rev-parse", "--short", "HEAD")
	label, err := git(d.Dir, "describe", "--tags", "--always")
	if err != nil || label == "" {
		label = sha
	}
	return source{"HEAD", label, fmt.Sprintf("branch %s at %s", e.Branch, sha)}, nil
}

// readSourceFile reads a file the way it will be uploaded: from the commit
// when deploying a commit, from the folder otherwise.
func (d *Deployer) readSourceFile(ref, rel string) ([]byte, error) {
	if ref == "" {
		return os.ReadFile(filepath.Join(d.Dir, rel))
	}
	cmd := exec.Command("git", "-C", d.Dir, "show", ref+":"+rel)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%s is not in %s: %s", rel, ref, strings.TrimSpace(e.String()))
	}
	return o.Bytes(), nil
}

func (d *Deployer) settings() map[string]string { return remote.Settings(d.Env) }

func (d *Deployer) runQuiet(ctx context.Context, mode string, extra map[string]string) (string, error) {
	env := d.settings()
	for k, v := range extra {
		env[k] = v
	}
	return remote.Capture(ctx, d.Conn, mode, env)
}

// explain prints the server's ERROR lines as a three-part message.
func explain(what string, raw string, next string) {
	var why []string
	for _, l := range strings.Split(raw, "\n") {
		if strings.HasPrefix(l, "ERROR: ") {
			why = append(why, strings.TrimPrefix(l, "ERROR: "))
		}
	}
	reason := strings.Join(why, " ")
	if reason == "" {
		reason = lastLines(raw, 4)
	}
	ui.Fail(what, reason, next)
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, " ")
}

// PrintChecks shows only what needs attention, plus one summary line.
func PrintChecks(cs []remote.Check) (problems int) {
	ok := 0
	for _, c := range cs {
		switch c.Status {
		case "ok":
			ok++
		case "warn":
			ui.Warn("%s: %s", c.Name, c.Message)
		case "problem":
			problems++
			fmt.Fprintf(os.Stderr, "%s %s %s\n", ui.Red("✗"), c.Name, c.Message)
		}
	}
	if problems == 0 && ok > 0 {
		ui.OK("%d settings check%s passed", ok, map[bool]string{true: "", false: "s"}[ok == 1])
	}
	return problems
}

// Deploy sends the project to the server and goes live.
func (d *Deployer) Deploy(ctx context.Context) error {
	e := d.Env
	w := d.out()
	fmt.Fprintf(w, "%s  →  %s  %s\n", ui.Bold(e.Name), d.Conn.Describe(), e.AppDir)

	if err := d.prepareCommit(); err != nil {
		return err
	}

	prog := NewProgress(w, d.tty())
	prog.Verbose = d.Opt.Verbose
	prog.Begin("Checking your project")

	src, err := d.chooseSource()
	if err != nil {
		prog.Fail()
		ui.Fail("Nothing was deployed.", err.Error(), "")
		return ErrStopped
	}
	if e.PushGit && src.ref == "HEAD" {
		if out, perr := git(d.Dir, "push", "-q", "origin", e.Branch); perr != nil {
			prog.Fail()
			ui.Fail("Could not push to git.", out, "Fix the push, or turn off push_git in .shipit/"+e.Name+".conf")
			return ErrStopped
		}
	}
	if e.LocalCheck != "" && !d.Opt.SkipChecks {
		if err := runLocal(d.Dir, e.LocalCheck); err != nil {
			prog.Fail()
			ui.Fail("Your own check failed, so nothing was deployed.", e.LocalCheck, "Fix it, or skip it once with --skip-checks")
			return ErrStopped
		}
	}

	// compose: read the file the way it will be uploaded
	var override string
	hasBuild := false
	tag := SafeTag(src.label)
	if e.Runtime == "compose" {
		raw, rerr := d.readSourceFile(src.ref, e.ComposeFile)
		if rerr != nil {
			prog.Fail()
			ui.Fail("Cannot read the compose file.", rerr.Error(), "Check compose_file in .shipit/"+e.Name+".conf")
			return ErrStopped
		}
		cf, perr := detect.ParseComposeData(e.ComposeFile, raw)
		if perr != nil {
			prog.Fail()
			ui.Fail("Cannot read the compose file.", perr.Error(), "")
			return ErrStopped
		}
		hasBuild = cf.AnyBuild()
		override = cf.Override(e.Project, tag)
	}
	prog.Done("Checked your project")

	if d.Opt.DryRun {
		d.printPlan(src, tag, false)
		fmt.Fprintln(w, "\nThis was a dry run. Nothing was changed on the server.")
		return nil
	}

	prog.Begin("Checking the server")
	if raw, err := d.runQuiet(ctx, "prepare", nil); err != nil {
		prog.Fail()
		explain("The server is not ready.", raw, "Run: shipit doctor "+e.Name)
		return ErrStopped
	}
	if e.EnvLinkAs != "" {
		raw, err := d.runQuiet(ctx, "check-env", nil)
		cs := remote.Checks(raw)
		if err != nil || hasProblems(cs) {
			prog.Fail()
			if len(cs) > 0 && cs[0].Name == "file" {
				ui.Fail("The server has no settings file for "+e.Name+".", "Your app reads its keys from "+remote.EnvPath(e)+", and nothing is there yet.", "run shipit secrets push "+e.Name)
			} else {
				PrintChecks(cs)
				ui.Fail("The settings on the server have problems, so nothing was deployed.", "", "Fix them, then run: shipit secrets push "+e.Name)
			}
			return ErrStopped
		}
	}
	prog.Done("Checked the server")

	d.printPlan(src, tag, true)
	if err := d.confirm(); err != nil {
		return err
	}

	rel := time.Now().UTC().Format("20060102-150405") + "-" + tag
	relDir := path.Join(e.AppDir, "releases", rel)
	extra := map[string][]byte{"VERSION": []byte(fmt.Sprintf("%s %s %s env=%s\n", src.label, rel, time.Now().UTC().Format(time.RFC3339), e.Name))}
	if override != "" {
		extra["compose.shipit.yml"] = []byte(override)
	}

	prog.Begin("Sending the code")
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(archive.Pack(ctx, archive.Source{Dir: d.Dir, Ref: src.ref}, extra, pw)) }()
	if err := d.Conn.Upload(ctx, relDir, pr); err != nil {
		prog.Fail()
		ui.Fail("Could not send the code.", err.Error(), "Run: shipit doctor "+e.Name)
		return ErrStopped
	}
	prog.Done("Sent the code")

	env := d.settings()
	env["SHIPIT_REL"] = rel
	env["SHIPIT_VERSION_TAG"] = tag
	env["SHIPIT_VERSION_LABEL"] = src.label
	if hasBuild {
		env["SHIPIT_HAS_BUILD"] = "1"
	}
	if err := remote.Run(ctx, d.Conn, "deploy", env, prog); err != nil {
		prog.Failed()
		fmt.Fprintln(os.Stderr)
		why := prog.Reason()
		if why == "" {
			why = "The previous version keeps running, or was put back."
		}
		ui.Fail("Deploy stopped. "+src.label+" was not made live.", why, "shipit logs "+e.Name+", or shipit history "+e.Name+" to see older versions")
		return ErrStopped
	}
	fmt.Fprintln(w)
	msg := "Done. " + src.label + " is live"
	if e.Domain != "" {
		msg += " at https://" + e.Domain
	}
	fmt.Fprintln(w, ui.Green(msg+"."))
	fmt.Fprintln(w, ui.Dim("To undo: shipit rollback "+e.Name))
	return nil
}

func hasProblems(cs []remote.Check) bool {
	for _, c := range cs {
		if c.Status == "problem" {
			return true
		}
	}
	return false
}

func (d *Deployer) printPlan(src source, tag string, server bool) {
	e := d.Env
	w := d.out()
	how := e.Runtime
	switch e.Runtime {
	case "compose":
		how = e.Engine + " compose (" + e.ComposeFile + ")"
	case "container":
		how = e.Engine + ", image built from " + e.Build.Dockerfile
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, ui.Bold("Plan"))
	row := func(k, v string) { fmt.Fprintf(w, "  %-9s %s\n", k, v) }
	row("Version", src.label)
	row("Where", e.Name+" on "+d.Conn.Describe()+":"+e.AppDir)
	row("Source", src.describe)
	row("Runs as", how)
	if e.EnvLinkAs != "" {
		row("Settings", "the server's own file, linked as "+e.EnvLinkAs+" (not changed by this deploy)")
	}
	if e.Health != "" {
		row("Safety", "goes back by itself if "+e.Health+" does not answer")
	} else {
		row("Safety", "goes back by itself if it does not start")
	}
	if e.Runtime == "compose" || e.Runtime == "container" {
		row("Image tag", tag)
	}
}

func (d *Deployer) confirm() error {
	e := d.Env
	if e.Protected && !d.Opt.Yes {
		if !ui.Interactive() {
			ui.Fail("This is a protected environment.", "It needs a person to confirm.", "Run in a terminal, or add --yes")
			return ErrStopped
		}
		fmt.Fprintln(d.out())
		if !ui.TypeToConfirm("Type '"+e.Name+"' to continue:", e.Name) {
			fmt.Fprintln(d.out(), "Cancelled. Nothing was changed.")
			return ErrStopped
		}
		return nil
	}
	if !ui.Interactive() && !d.Opt.Yes {
		ui.Fail("There is nobody to ask.", "shipit needs a yes before it changes a server.", "Run in a terminal, or add --yes")
		return ErrStopped
	}
	ok, err := ui.Confirm("Go ahead?", true, "shipit sends the code, builds it, and only then switches the live version. If the new version does not work, the old one is put back.")
	if err != nil || !ok {
		fmt.Fprintln(d.out(), "Cancelled. Nothing was changed.")
		return ErrStopped
	}
	return nil
}

func runLocal(dir, command string) error {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("bash", "-c", command)
	}
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

// Rollback goes back to the last version that worked.
func (d *Deployer) Rollback(ctx context.Context) error {
	e := d.Env
	w := d.out()
	fmt.Fprintf(w, "%s  →  %s  %s\n", ui.Bold(e.Name), d.Conn.Describe(), e.AppDir)
	if !d.Opt.Yes {
		if !ui.Interactive() {
			ui.Fail("There is nobody to ask.", "", "Run in a terminal, or add --yes")
			return ErrStopped
		}
		q := "Go back to the last version that worked?"
		if d.Opt.To != "" {
			q = "Go back to " + d.Opt.To + "?"
		}
		ok, err := ui.Confirm(q, false, "The code and containers go back. Changes already made to your database are not undone.")
		if err != nil || !ok {
			fmt.Fprintln(w, "Cancelled. Nothing was changed.")
			return ErrStopped
		}
	}
	prog := NewProgress(w, d.tty())
	prog.Verbose = d.Opt.Verbose
	env := d.settings()
	env["SHIPIT_TO"] = d.Opt.To
	if err := remote.Run(ctx, d.Conn, "rollback", env, prog); err != nil {
		prog.Failed()
		ui.Fail("Could not go back.", prog.Reason(), "shipit history "+e.Name+" shows what is on the server")
		return ErrStopped
	}
	if prog.Result != "" {
		fmt.Fprintln(w, ui.Green("Done. "+prog.Result+" is live."))
	} else {
		fmt.Fprintln(w, ui.Green("Done. The earlier version is live."))
	}
	fmt.Fprintln(w, ui.Dim("Database changes are not undone."))
	return nil
}

// versionOf pulls the version out of a release folder name.
func versionOf(rel string) string {
	p := strings.SplitN(rel, "-", 3)
	if len(p) == 3 {
		return p[2]
	}
	return rel
}

// Status shows what is live and whether it works.
func (d *Deployer) Status(ctx context.Context) error {
	e := d.Env
	raw, err := d.runQuiet(ctx, "status", nil)
	if err != nil {
		explain("Could not read the status.", raw, "Run: shipit doctor "+e.Name)
		return ErrStopped
	}
	kv := remote.KV(raw)
	w := d.out()
	live := kv["live"]
	state := ui.Red("not running")
	switch {
	case kv["health"] == "ok":
		state = ui.Green("healthy")
	case kv["health"] == "failing":
		state = ui.Red("not answering at " + e.Health)
	case kv["running"] == "yes":
		state = ui.Green("running")
	}
	if live == "" {
		fmt.Fprintf(w, "%s · nothing deployed yet\n", ui.Bold(e.Name))
		fmt.Fprintln(w, ui.Dim("Deploy with: shipit deploy "+e.Name))
		return nil
	}
	fmt.Fprintf(w, "%s · %s is live · %s\n", ui.Bold(e.Name), ui.Bold(versionOf(live)), state)
	var vs []string
	for _, r := range strings.Fields(kv["versions"]) {
		v := versionOf(r)
		if r == live {
			v += " (live)"
		}
		vs = append(vs, v)
	}
	fmt.Fprintf(w, "  Versions on the server: %s\n", strings.Join(vs, ", "))
	if last := kv["last"]; last != "" {
		p := strings.Split(last, "|")
		if len(p) == 4 {
			fmt.Fprintf(w, "  Last deploy: %s  %s\n", p[0], p[3])
		}
	}
	return nil
}

// History lists recent deploys, newest first.
func (d *Deployer) History(ctx context.Context) error {
	raw, err := d.runQuiet(ctx, "history", map[string]string{"SHIPIT_LINES": "20"})
	if err != nil {
		explain("Could not read the history.", raw, "")
		return ErrStopped
	}
	if strings.TrimSpace(raw) == "" {
		fmt.Fprintln(d.out(), "No deploys yet.")
		return nil
	}
	for _, l := range strings.Split(strings.TrimSpace(raw), "\n") {
		p := strings.Split(l, "|")
		if len(p) != 4 {
			continue
		}
		mark := ui.Green("✓")
		if p[3] != "ok" {
			mark = ui.Red("✗")
		}
		fmt.Fprintf(d.out(), "  %s %-14s %s  %s\n", mark, p[2], p[0], p[3])
	}
	return nil
}

// Logs prints recent log lines.
func (d *Deployer) Logs(ctx context.Context) error {
	n := d.Opt.Lines
	if n == 0 {
		n = 100
	}
	env := d.settings()
	env["SHIPIT_LINES"] = fmt.Sprint(n)
	if err := remote.Run(ctx, d.Conn, "logs", env, d.out()); err != nil {
		ui.Fail("Could not read the logs.", "", "Run: shipit doctor "+d.Env.Name)
		return ErrStopped
	}
	return nil
}

// ReadComposeFile reads a compose file from the working folder.
func ReadComposeFile(dir, rel string) ([]byte, error) {
	return os.ReadFile(filepath.Join(dir, rel))
}

// prepareCommit offers to commit uncommitted work before a deploy, so what
// goes live is what you just wrote. It stays quiet when nothing has changed.
func (d *Deployer) prepareCommit() error {
	o := d.Opt
	if o.WorkingTree || o.AllowDirty || o.Tag != "" || !isGitRepo(d.Dir) {
		return nil
	}
	if cur, _ := git(d.Dir, "rev-parse", "--abbrev-ref", "HEAD"); cur != d.Env.Branch {
		return nil // chooseSource explains the branch problem
	}
	if !ui.Interactive() && !o.Commit {
		return nil // chooseSource explains, and mentions --commit
	}
	_, err := commit.Flow(d.Dir, commit.FlowOptions{
		Yes: o.Yes, Auto: o.Commit, Message: o.Message, Cmd: d.Env.CommitCmd, OfferDeploy: true,
	})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, commit.ErrLeave):
		d.Opt.AllowDirty = true
		return nil
	case errors.Is(err, commit.ErrStop):
		fmt.Fprintln(d.out(), "Cancelled. Nothing was committed or deployed.")
		return ErrStopped
	}
	ui.Fail("Could not commit.", err.Error(), "fix that, or commit yourself and run the deploy again")
	return ErrStopped
}
