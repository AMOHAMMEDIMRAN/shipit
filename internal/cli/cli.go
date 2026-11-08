// Package cli is the shipit command line: it parses arguments, finds the
// project and server, and hands the work to the other packages.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"

	"github.com/amohammedimran/shipit/internal/commit"
	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/deploy"
	"github.com/amohammedimran/shipit/internal/transport"
	"github.com/amohammedimran/shipit/internal/ui"
)

// Version is set at build time with -ldflags.
var Version = "0.2.0-dev"

// args is a parsed command line.
type args struct {
	pos   []string
	flags map[string]string
	bools map[string]bool
}

var valueFlags = map[string]bool{"message": true, "tag": true, "to": true, "lines": true, "server": true, "env": true}

func parse(argv []string) (*args, error) {
	a := &args{flags: map[string]string{}, bools: map[string]bool{}}
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		if s == "--" {
			a.pos = append(a.pos, argv[i+1:]...)
			break
		}
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			continue
		}
		name := strings.TrimLeft(s, "-")
		val := ""
		hasVal := false
		if k, v, ok := strings.Cut(name, "="); ok {
			name, val, hasVal = k, v, true
		}
		switch name {
		case "y":
			name = "yes"
		case "h":
			name = "help"
		case "v":
			name = "verbose"
		case "m":
			name = "message"
		}
		if valueFlags[name] {
			if !hasVal {
				if i+1 >= len(argv) {
					return nil, fmt.Errorf("--%s needs a value", name)
				}
				i++
				val = argv[i]
			}
			a.flags[name] = val
			continue
		}
		switch name {
		case "yes", "commit", "dry-run", "allow-dirty", "working-tree", "skip-checks", "verbose", "help", "version":
			a.bools[name] = true
		default:
			return nil, fmt.Errorf("unknown option --%s. See: shipit help", name)
		}
	}
	return a, nil
}

// App holds what commands share.
type App struct {
	Dir string
	a   *args
}

func (a *App) opts() deploy.Options {
	o := deploy.Options{
		Yes: a.a.bools["yes"], DryRun: a.a.bools["dry-run"], AllowDirty: a.a.bools["allow-dirty"],
		WorkingTree: a.a.bools["working-tree"], SkipChecks: a.a.bools["skip-checks"], Verbose: a.a.bools["verbose"],
		Commit: a.a.bools["commit"], Message: a.a.flags["message"],
		Tag: a.a.flags["tag"], To: a.a.flags["to"],
	}
	if v := a.a.flags["lines"]; v != "" {
		o.Lines, _ = strconv.Atoi(v)
	}
	return o
}

// Run is the program. It returns the exit code.
func Run(argv []string) int {
	a, err := parse(argv)
	if err != nil {
		ui.Fail(err.Error(), "", "shipit help")
		return 2
	}
	if a.bools["yes"] {
		ui.Assume = true
	}
	dir, _ := os.Getwd()
	app := &App{Dir: dir, a: a}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cmd := ""
	if len(a.pos) > 0 {
		cmd, a.pos = a.pos[0], a.pos[1:]
	}
	if a.bools["version"] {
		cmd = "version"
	}
	if a.bools["help"] && cmd != "" && cmd != "help" {
		a.pos = []string{cmd}
		cmd = "help"
	}

	var rerr error
	switch cmd {
	case "":
		rerr = app.home(ctx)
	case "help":
		rerr = app.help()
	case "version":
		fmt.Println("shipit " + Version)
	case "server", "servers":
		rerr = app.server(ctx)
	case "init":
		rerr = app.init(ctx)
	case "detect":
		rerr = app.detect()
	case "envs":
		rerr = app.envs()
	case "doctor":
		rerr = app.doctor(ctx)
	case "commit":
		rerr = app.commit()
	case "deploy":
		rerr = app.withEnv(ctx, func(d *deploy.Deployer) error { return d.Deploy(ctx) })
	case "rollback":
		rerr = app.withEnv(ctx, func(d *deploy.Deployer) error { return d.Rollback(ctx) })
	case "status":
		rerr = app.withEnv(ctx, func(d *deploy.Deployer) error { return d.Status(ctx) })
	case "logs":
		rerr = app.withEnv(ctx, func(d *deploy.Deployer) error { return d.Logs(ctx) })
	case "history":
		rerr = app.withEnv(ctx, func(d *deploy.Deployer) error { return d.History(ctx) })
	case "secrets":
		rerr = app.secrets(ctx)
	default:
		ui.Fail("shipit does not know the command \""+cmd+"\".", "", "shipit help")
		return 2
	}
	switch {
	case rerr == nil:
		return 0
	case errors.Is(rerr, deploy.ErrStopped):
		return 1
	case errors.Is(rerr, ui.ErrCancelled):
		fmt.Println("\nStopped. Nothing was changed.")
		return 130
	default:
		ui.Fail(rerr.Error(), "", "")
		return 1
	}
}

// pickEnv chooses the environment from the command line or the project.
func (a *App) pickEnv() (string, error) {
	if len(a.a.pos) > 0 {
		return a.a.pos[0], nil
	}
	if v := a.a.flags["env"]; v != "" {
		return v, nil
	}
	envs := config.ListEnvs(a.Dir)
	switch len(envs) {
	case 0:
		return "", errors.New("this project is not set up yet. Run: shipit init")
	case 1:
		return envs[0], nil
	}
	if ui.Interactive() {
		opts := make([]ui.Option, len(envs))
		for i, e := range envs {
			opts[i] = ui.Option{Label: e, Value: e}
		}
		i, err := ui.Select("Which environment?", opts, 0, "")
		if err != nil {
			return "", err
		}
		return envs[i], nil
	}
	return "", fmt.Errorf("this project has several environments (%s). Say which one, for example: shipit deploy %s", strings.Join(envs, ", "), envs[0])
}

// trust asks about a server seen for the first time.
func trust(host, fingerprint string) bool {
	if !ui.Interactive() {
		return false
	}
	ui.Say("")
	ui.Say("  This is the first time shipit connects to %s.", host)
	ui.Say("  Its fingerprint is %s", ui.Bold(fingerprint))
	ok, err := ui.Confirm("Is this your server?", false,
		"A fingerprint identifies a server, like a signature.\nTo check it, log in to the server another way and run:\n  ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub\nIf the two match, answer y. If you are unsure, answer n.")
	return err == nil && ok
}

// connect opens the connection for an environment.
func (a *App) connect(e *config.Env) (transport.Conn, error) {
	servers, err := config.LoadServers()
	if err != nil {
		return nil, err
	}
	s, ok := servers[e.Server]
	if !ok {
		return nil, fmt.Errorf("this project uses the server %q, which is not set up on this computer. Add it with: shipit server add", e.Server)
	}
	return openServer(s)
}

func openServer(s *config.Server) (transport.Conn, error) {
	c, err := transport.Open(s, trust)
	if err != nil {
		switch {
		case errors.Is(err, transport.ErrUnknownHost):
			if ui.Interactive() {
				return nil, errors.New("you chose not to trust this server, so shipit did not connect. Nothing was sent")
			}
			return nil, errors.New("this server is not trusted yet, and there is nobody to ask. Run once in a terminal: shipit server test " + s.Name)
		}
		return nil, err
	}
	return c, nil
}

// withEnv loads the environment, connects, and runs fn.
func (a *App) withEnv(ctx context.Context, fn func(*deploy.Deployer) error) error {
	name, err := a.pickEnv()
	if err != nil {
		return err
	}
	e, err := config.LoadEnv(a.Dir, name)
	if err != nil {
		return err
	}
	conn, err := a.connect(e)
	if err != nil {
		ui.Fail("Could not connect to the server.", err.Error(), "Check it with: shipit server test "+e.Server)
		return deploy.ErrStopped
	}
	defer conn.Close()
	return fn(&deploy.Deployer{Dir: a.Dir, Env: e, Conn: conn, Opt: a.opts()})
}

func (a *App) envs() error {
	envs := config.ListEnvs(a.Dir)
	if len(envs) == 0 {
		fmt.Println("No environments yet. Run: shipit init")
		return nil
	}
	for _, n := range envs {
		e, err := config.LoadEnv(a.Dir, n)
		if err != nil {
			fmt.Printf("  %s  %s\n", n, ui.Red("has a problem: "+err.Error()))
			continue
		}
		fmt.Printf("  %-12s %s on %s  %s\n", n, e.Runtime, e.Server, ui.Dim(e.AppDir))
	}
	return nil
}

// commit commits uncommitted changes with a written message, without deploying.
func (a *App) commit() error {
	cmd := ""
	if envs := config.ListEnvs(a.Dir); len(envs) > 0 {
		if e, err := config.LoadEnv(a.Dir, envs[0]); err == nil {
			cmd = e.CommitCmd
		}
	}
	o := a.opts()
	done, err := commit.Flow(a.Dir, commit.FlowOptions{Yes: o.Yes, Auto: o.Commit, Message: o.Message, Cmd: cmd})
	switch {
	case errors.Is(err, commit.ErrStop):
		fmt.Println("Cancelled. Nothing was committed.")
		return deploy.ErrStopped
	case err != nil:
		ui.Fail("Could not commit.", err.Error(), "")
		return deploy.ErrStopped
	case !done:
		fmt.Println("Nothing to commit.")
	}
	return nil
}
