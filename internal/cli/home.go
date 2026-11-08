package cli

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/deploy"
	"github.com/amohammedimran/shipit/internal/detect"
	"github.com/amohammedimran/shipit/internal/doctor"
	"github.com/amohammedimran/shipit/internal/ui"
)

// home is what plain "shipit" shows.
func (a *App) home(ctx context.Context) error {
	envs := config.ListEnvs(a.Dir)
	if len(envs) == 0 {
		if !ui.Interactive() {
			return a.help()
		}
		ui.Say("")
		ui.Say("%s", ui.Bold("shipit"))
		ui.Say("  This folder is not set up yet.")
		i, err := ui.Select("What do you want to do?", []ui.Option{
			{Label: "Set up this project", Hint: "shipit init"},
			{Label: "Add a server only", Hint: "shipit server add"},
			{Label: "Help", Hint: "shipit help"},
		}, 0, "Setting up asks a few questions and saves the answers in .shipit/ inside this folder.")
		if err != nil {
			return err
		}
		switch i {
		case 0:
			return a.init(ctx)
		case 1:
			a.a.pos = []string{"add"}
			return a.server(ctx)
		}
		return a.help()
	}
	if !ui.Interactive() {
		return a.help()
	}
	name, err := a.pickEnv()
	if err != nil {
		return err
	}
	a.a.pos = []string{name}
	e, err := config.LoadEnv(a.Dir, name)
	if err != nil {
		return err
	}
	ui.Say("")
	ui.Say("%s · %s", ui.Bold(e.Project), name)
	for {
		i, err := ui.Select("What do you want to do?", []ui.Option{
			{Label: "Deploy a new version", Hint: "shipit deploy " + name},
			{Label: "Go back to the last version", Hint: "shipit rollback " + name},
			{Label: "See what is live", Hint: "shipit status " + name},
			{Label: "See the logs", Hint: "shipit logs " + name},
			{Label: "Send keys and settings to the server", Hint: "shipit secrets push " + name},
			{Label: "Set up another environment", Hint: "shipit init"},
			{Label: "Help", Hint: "shipit help"},
			{Label: "Quit"},
		}, 2, "Each choice shows the command it runs, so next time you can type it directly.")
		if err != nil {
			return err
		}
		switch i {
		case 0:
			return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.Deploy(ctx) })
		case 1:
			return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.Rollback(ctx) })
		case 2:
			if err := a.withEnv(ctx, func(d *deploy.Deployer) error { return d.Status(ctx) }); err != nil && !errors.Is(err, deploy.ErrStopped) {
				return err
			}
		case 3:
			return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.Logs(ctx) })
		case 4:
			return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.PushEnv(ctx) })
		case 5:
			return a.init(ctx)
		case 6:
			return a.help()
		default:
			return nil
		}
	}
}

const mainHelp = `shipit puts your project on your own server, safely, from your own computer.

The things people do:
  shipit                    start here: it asks what you want to do
  shipit init               set up this project (asks a few questions)
  shipit commit             commit your changes with a message written for you
  shipit deploy             send the latest version live (offers to commit first)
  shipit rollback           go back to the last version that worked
  shipit status             see what is live and whether it works
  shipit logs               see recent logs
  shipit secrets push       send your keys and settings to the server

When something is wrong:
  shipit doctor             check the server and say how to fix problems

Learn more:
  shipit help <topic>       topics: start, commit, versions, keys, servers, rollback, windows

Press ? at any question to learn what it means.`

var topics = map[string]string{
	"start": `First time:
  1. Run shipit in your project folder.
  2. Answer the questions. Press ? at any of them for help.
  3. Choose to send your keys and deploy at the end.
After that, shipit deploy is all you need.`,

	"versions": `A version is a git tag, such as v1.2.0.
  git tag v1.2.0
  git push origin main --tags
  shipit deploy --tag v1.2.0
Without --tag, shipit deploys the latest commit and names it from the nearest tag.
Every version stays on the server (the last 3 by default), so you can go back:
  shipit history        see what was deployed
  shipit rollback       go back one version
  shipit rollback --to v1.1.0`,

	"commit": `shipit can commit your changes for you, with a message written from them.
  shipit commit                  show what changed, suggest a message, commit after your yes
  shipit commit -m "Fix login"   use your own message
  shipit deploy                  if you have uncommitted changes, it offers the same thing first
  shipit deploy --commit         commit with a written message, then deploy, without asking
It commits only the files it lists. Anything that looks like a key or a settings file
(.env files, *.pem, *.key) is always left out.
The message comes from file names and change counts. For a message written from what the
code does, set a command in .shipit/<env>.conf that reads the changes on standard input
and prints a message, for example:
  commit_message_cmd = claude -p "Reply with only the commit message"
The changes are sent to that command, so choose one you trust with your code.`,

	"keys": `Your keys and settings (database address, payment keys, secrets) live in a file such as
.env.production. They are kept apart from the code:
  - Deploys never upload .env files with the code.
  - shipit secrets push sends the production file to the server, on request only.
  - The server keeps it in shared/, so every version uses the same file.
  - shipit secrets check tells you if a key is missing or wrong. It never shows values.
Add checks in .shipit/<env>.conf, for example:
  [rules]
  RAZORPAY_KEY_ID = ^rzp_live_
Then a test key can never be sent to production.`,

	"servers": `A server is saved once on your computer and shared by all projects.
  shipit server add       add one
  shipit server list      see them
  shipit server test <n>  check the connection and see what the server has
  shipit server remove <n>
shipit needs SSH access with a key. It installs nothing on the server.`,

	"rollback": `shipit rollback goes back to the last version that went live and worked.
  shipit rollback              the one before this
  shipit rollback --to v1.1.0  a specific version still on the server
The code and containers go back. Changes already made to your database are not undone.
If a new version does not work after a deploy, shipit puts the old one back by itself.`,

	"windows": `Windows servers are planned for a later version.
For now shipit works with Linux and macOS servers, from a Linux, macOS or Windows computer.
If your server runs Windows and you only have Remote Desktop, you can turn on OpenSSH
(Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0) so SSH is ready when it arrives.`,
}

func (a *App) help() error {
	if len(a.a.pos) > 0 {
		t := strings.ToLower(a.a.pos[0])
		if txt, ok := topics[t]; ok {
			fmt.Println(txt)
			return nil
		}
		if t != "help" {
			var names []string
			for k := range topics {
				names = append(names, k)
			}
			sort.Strings(names)
			ui.Fail("There is no help topic called \""+t+"\".", "", "Topics: "+strings.Join(names, ", "))
			return deploy.ErrStopped
		}
	}
	fmt.Println(mainHelp)
	return nil
}

// detect shows what shipit recognises in this folder.
func (a *App) detect() error {
	cands := detect.Scan(a.Dir)
	if len(cands) == 0 {
		fmt.Println("shipit did not recognise anything here. You can still deploy with your own commands: shipit init")
	}
	for _, c := range cands {
		line := fmt.Sprintf("  %-28s %s", c.File, c.Evidence)
		fmt.Println(line)
		if c.Note != "" {
			fmt.Printf("  %-28s %s\n", "", ui.Dim(c.Note))
		}
	}
	files := detect.EnvFiles(a.Dir)
	if len(files) > 0 {
		fmt.Println()
		for _, f := range files {
			note := ""
			switch {
			case f.Local:
				note = "looks local"
			case f.Plain:
				note = "check before using for production"
			}
			fmt.Printf("  %-28s %d keys  %s\n", f.Name, f.Keys, ui.Dim(note))
		}
	}
	return nil
}

// doctor checks a server and an environment.
func (a *App) doctor(ctx context.Context) error {
	name, err := a.pickEnv()
	if err != nil {
		if len(config.ListEnvs(a.Dir)) == 0 && len(a.a.pos) == 0 {
			ui.Fail("This folder is not set up yet.", "doctor checks a server for a project.", "Run: shipit init   (or: shipit server test <name> to look at a server only)")
			return deploy.ErrStopped
		}
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
	ui.OK("Connected to %s.", conn.Describe())
	var ports []string
	if e.Runtime == "compose" {
		if raw, rerr := deploy.ReadComposeFile(a.Dir, e.ComposeFile); rerr == nil {
			if cf, perr := detect.ParseComposeData(e.ComposeFile, raw); perr == nil {
				ports = cf.HostPorts()
			}
		}
	}
	facts, err := doctor.Facts(ctx, conn, e.AppDir, ports)
	if err != nil {
		return err
	}
	bad := 0
	for _, it := range doctor.Check(facts, e, ports) {
		switch it.Status {
		case "ok":
			ui.OK("%s", it.Title)
		case "warn":
			ui.Warn("%s", it.Title)
			if it.Fix != "" {
				ui.Note("%s", it.Fix)
			}
		default:
			bad++
			ui.Fail(it.Title, "", it.Fix)
		}
	}
	if e.EnvLinkAs != "" {
		d := &deploy.Deployer{Dir: a.Dir, Env: e, Conn: conn}
		if err := d.CheckEnv(ctx); err != nil {
			bad++
		}
	}
	if bad > 0 {
		return deploy.ErrStopped
	}
	ui.Say("")
	ui.Say("Everything looks ready.")
	return nil
}

func (a *App) secrets(ctx context.Context) error {
	sub := ""
	if len(a.a.pos) > 0 {
		sub, a.a.pos = a.a.pos[0], a.a.pos[1:]
	}
	switch sub {
	case "push":
		return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.PushEnv(ctx) })
	case "check":
		return a.withEnv(ctx, func(d *deploy.Deployer) error { return d.CheckEnv(ctx) })
	case "":
		fmt.Println("shipit secrets push [env]    send your production keys and settings to the server")
		fmt.Println("shipit secrets check [env]   check the file on the server (names only, never values)")
		return nil
	}
	return fmt.Errorf("shipit secrets does not have a %q command. Try: push, check", sub)
}
