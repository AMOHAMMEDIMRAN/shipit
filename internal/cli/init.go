package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/deploy"
	"github.com/amohammedimran/shipit/internal/detect"
	"github.com/amohammedimran/shipit/internal/doctor"
	"github.com/amohammedimran/shipit/internal/transport"
	"github.com/amohammedimran/shipit/internal/ui"
)

const totalSteps = 7

var portRe = regexp.MustCompile(`^(\d{1,3}(\.\d{1,3}){3}:)?\d{1,5}:\d{1,5}$`)

// init is the setup wizard.
func (a *App) init(ctx context.Context) error {
	if !ui.Interactive() {
		return errors.New("setup asks questions, so it needs a terminal")
	}
	ui.Say("")
	ui.Say("%s", ui.Bold("Let's set up shipit for this project."))
	ui.Note("Press ? at any question to learn what it means. Press q to stop. Nothing changes on your server until the end.")

	// ---- 1. server ----
	servers, err := config.LoadServers()
	if err != nil {
		return err
	}
	var srv *config.Server
	var conn transport.Conn
	var facts map[string]string
	if len(servers) == 0 {
		srv, conn, facts, err = serverFlow(ctx, 1, totalSteps)
	} else {
		srv, conn, facts, err = a.chooseServer(ctx, servers)
	}
	if err != nil {
		return err
	}
	defer conn.Close()

	// ---- 2. names ----
	ui.Step(2, totalSteps, "What is this project called?")
	abs, _ := filepath.Abs(a.Dir)
	project, err := ui.Input("", config.Slug(filepath.Base(abs)), "This name is used for the folder on the server and for container and process names,\nso two projects on one server never clash. Use lowercase letters, numbers and dashes.",
		func(v string) error {
			if !config.ValidEnvName(v) {
				return errors.New("use lowercase letters, numbers and dashes")
			}
			return nil
		})
	if err != nil {
		return err
	}
	have := config.ListEnvs(a.Dir)
	envDef := "production"
	if contains(have, "production") {
		envDef = "staging"
	}
	envName, err := ui.Input("Which environment is this? (production, staging, ...)", envDef,
		"An environment is one place the project is deployed. Most projects have production,\nand some add staging for testing first. Each one has its own folder, keys and settings.",
		func(v string) error {
			if !config.ValidEnvName(v) {
				return errors.New("use lowercase letters, numbers and dashes")
			}
			if contains(have, v) {
				return fmt.Errorf("%s already exists in .shipit/. Edit that file, or choose another name", v)
			}
			return nil
		})
	if err != nil {
		return err
	}
	e := &config.Env{Name: envName, Project: project, Server: srv.Name, Branch: currentBranch(a.Dir), EnvLinkAs: ".env", Keep: 3,
		Rules: map[string]string{}, Build: config.Build{Where: "server"}}

	// ---- 3. how it runs ----
	cand, err := a.chooseHow(e)
	if err != nil {
		return err
	}

	// ---- 4. details ----
	var cf *detect.Compose
	if cand != nil {
		cf = cand.Compose
	}
	if err := a.askDetails(e, cand, cf, facts); err != nil {
		return err
	}

	// ---- 5. where ----
	if err := a.askWhere(ctx, e, conn, facts, cf); err != nil {
		return err
	}

	// ---- 6. settings and keys ----
	if err := a.askEnvFile(e, cf); err != nil {
		return err
	}

	// ---- 7. review ----
	return a.review(ctx, e, conn)
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func currentBranch(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	b := strings.TrimSpace(string(out))
	if err != nil || b == "" || b == "HEAD" {
		return "main"
	}
	return b
}

func (a *App) chooseServer(ctx context.Context, servers map[string]*config.Server) (*config.Server, transport.Conn, map[string]string, error) {
	names := make([]string, 0, len(servers))
	for n := range servers {
		names = append(names, n)
	}
	sort.Strings(names)
	ui.Step(1, totalSteps, "Which server is this for?")
	opts := make([]ui.Option, 0, len(names)+1)
	for _, n := range names {
		s := servers[n]
		hint := "this computer"
		if s.Type == "ssh" {
			hint = s.User + "@" + s.Host
		}
		opts = append(opts, ui.Option{Label: n, Hint: hint, Value: n})
	}
	opts = append(opts, ui.Option{Label: "Add a new server", Value: ""})
	i, err := ui.Select("", opts, 0, "Servers are saved once on this computer and shared by all your projects.")
	if err != nil {
		return nil, nil, nil, err
	}
	if i == len(opts)-1 {
		return serverFlow(ctx, 1, totalSteps)
	}
	s := servers[names[i]]
	conn, err := openServer(s)
	if err != nil {
		ui.Fail("Could not connect to "+s.Name+".", err.Error(), "Check it with: shipit server test "+s.Name)
		return nil, nil, nil, deploy.ErrStopped
	}
	ui.OK("Connected to %s.", conn.Describe())
	facts, err := doctor.Facts(ctx, conn, "", nil)
	if err != nil {
		conn.Close()
		return nil, nil, nil, err
	}
	ui.Note("Found: %s", doctor.Summary(facts))
	return s, conn, facts, nil
}

// chooseHow scans the project and lets the person pick how it is deployed.
// It sets the runtime on e. It returns the chosen candidate, or nil for "my own commands".
func (a *App) chooseHow(e *config.Env) (*detect.Candidate, error) {
	ui.Step(3, totalSteps, "How should this project be deployed?")
	cands := detect.Scan(a.Dir)
	var opts []ui.Option
	var usable []detect.Candidate
	for _, c := range cands {
		switch c.Kind {
		case "compose", "container", "pm2", "node":
			usable = append(usable, c)
			hint := c.Evidence
			if c.Note != "" {
				hint += " · " + c.Note
			}
			opts = append(opts, ui.Option{Label: c.File, Hint: hint})
		}
	}
	if len(usable) == 0 {
		ui.Note("shipit did not recognise a compose file, Dockerfile or Node app here.")
	} else {
		ui.Note("Found in this project:")
	}
	for _, c := range cands {
		if c.Kind == "quadlet" || c.Kind == "static" {
			ui.Note("also found %s, which shipit does not deploy yet", c.File)
		}
	}
	opts = append(opts, ui.Option{Label: "Run my own commands", Hint: "you give the build and start commands"})
	i, err := ui.Select("", opts, 0, "shipit looked at the files in this folder and lists what it recognised.\nPick the one you use in production. If none fits, pick 'Run my own commands'\nand shipit will ask for the commands.")
	if err != nil {
		return nil, err
	}
	if i == len(usable) {
		e.Runtime = "script"
		return nil, nil
	}
	c := usable[i]
	switch c.Kind {
	case "compose":
		e.Runtime, e.ComposeFile = "compose", c.File
	case "container":
		e.Runtime = "container"
		e.Build.Dockerfile, e.Build.Context = c.File, "."
	case "pm2":
		e.Runtime, e.PM2File = "pm2", c.File
	case "node":
		e.Runtime = "pm2"
	}
	return &c, nil
}

func (a *App) askDetails(e *config.Env, c *detect.Candidate, cf *detect.Compose, facts map[string]string) error {
	ui.Step(4, totalSteps, "A few details")
	switch e.Runtime {
	case "compose", "container":
		engines := doctor.Engines(facts)
		if len(engines) == 0 {
			return errors.New("the server has no container engine this login can use. Run: shipit server test " + e.Server + ", or choose another way to deploy")
		}
		e.Engine = engines[0]
		if len(engines) > 1 {
			opts := make([]ui.Option, len(engines))
			for i, n := range engines {
				opts[i] = ui.Option{Label: n}
			}
			i, err := ui.Select("Which one should run it?", opts, 0, "Both are installed on the server. The compose file or Dockerfile works with either.\nPick the one you normally use for this project.")
			if err != nil {
				return err
			}
			e.Engine = engines[i]
		} else {
			ui.Note("Runs with %s, the only container engine on the server.", e.Engine)
		}
	}
	healthDef := ""
	switch e.Runtime {
	case "compose":
		if cf != nil {
			if cf.Name != "" {
				e.ComposeProject = cf.Name
				ui.Note("Keeps the compose project name %q, so existing containers are not duplicated.", cf.Name)
			}
			healthDef = cf.HealthURL()
			for _, s := range cf.Services {
				if len(s.EnvFiles) > 0 {
					e.EnvLinkAs = path.Base(s.EnvFiles[0])
					break
				}
			}
			if cf.FixedNames() {
				ui.Note("The file sets fixed container names, so a new version replaces the old container in place, with a short gap.")
			}
			if cf.AnyBuild() {
				ui.Note("Images are built on the server and tagged with the version, so going back reuses the old image.")
			}
		}
	case "container":
		port, err := ui.Input("Which port does the app use? (server:app)", "3000:3000", "The first number is the port on the server. The second is the port inside the app.\nExample 8080:3000 serves the app's port 3000 on the server's 8080.\nStart with 127.0.0.1: to keep it private behind a web server, for example 127.0.0.1:8080:3000.",
			func(v string) error {
				if !portRe.MatchString(v) {
					return errors.New("write it like 3000:3000 or 127.0.0.1:8080:3000")
				}
				return nil
			})
		if err != nil {
			return err
		}
		e.Port = port
		parts := strings.Split(port, ":")
		host := "127.0.0.1"
		hp := parts[0]
		if len(parts) == 3 {
			host, hp = parts[0], parts[1]
		}
		healthDef = "http://" + host + ":" + hp + "/"
		if facts["arch"] != "" {
			ui.Note("The image is built on the server (%s), so it always matches.", facts["arch"])
		}
	case "pm2":
		install, build := detect.PackageManager(a.Dir)
		_ = install
		def := "npm start"
		if e.PM2File == "" {
			s, err := ui.Input("Command that starts the app", def, "This runs from the project folder on the server, for example 'npm start' or 'node server.js'.", nil)
			if err != nil {
				return err
			}
			e.StartCmd = s
			b, err := ui.Input("Command that builds it (type none for no build)", build, "Run once on the server for each new version, before it goes live.\nshipit guessed this from your lock file.", nil)
			if err != nil {
				return err
			}
			if strings.ToLower(b) != "none" {
				e.BuildCmd = b
			}
		}
	case "script":
		s, err := ui.Input("Command that starts the app", "", "Runs from the new version's folder on the server each time a version goes live,\nfor example './restart.sh'. It should (re)start your app and return.", nil)
		if err != nil {
			return err
		}
		e.StartCmd = s
		b, err := ui.Input("Command that builds it (type none for no build)", "none", "Run once on the server for each new version, before it goes live.", nil)
		if err != nil {
			return err
		}
		if strings.ToLower(b) != "none" {
			e.BuildCmd = b
		}
	}
	hd := healthDef
	if hd == "" {
		hd = "none"
	}
	h, err := ui.Input("Address that answers when the app works (type none to skip)", hd, "After a new version starts, shipit opens this address from the server.\nIf it answers, the version stays. If not, the old version is put back.\nExample: http://127.0.0.1:3000/health",
		func(v string) error {
			if strings.ToLower(v) != "none" && !strings.HasPrefix(v, "http://") && !strings.HasPrefix(v, "https://") {
				return errors.New("start it with http:// or https://, or type none")
			}
			return nil
		})
	if err != nil {
		return err
	}
	if strings.ToLower(h) != "none" {
		e.Health = h
	}
	return nil
}

func (a *App) askWhere(ctx context.Context, e *config.Env, conn transport.Conn, facts map[string]string, cf *detect.Compose) error {
	ui.Step(5, totalSteps, "Where on the server should it live?")
	home := facts["home"]
	if home == "" {
		home = "/home/" + facts["user"]
	}
	def := path.Join(home, "apps", e.Project)
	if e.Name != "production" {
		def += "-" + e.Name
	}
	var ports []string
	if cf != nil {
		ports = cf.HostPorts()
	} else if parts := strings.Split(e.Port, ":"); e.Port != "" {
		ports = []string{parts[len(parts)-2]}
	}
	for {
		dir, err := ui.Input("", def, "This is the folder shipit creates on your server for this project.\nPick any folder your login can write to. Your versions and keys are kept inside it.\nIt does not need to exist yet.",
			func(v string) error {
				if !strings.HasPrefix(v, "/") {
					return errors.New("write the full path, starting with /")
				}
				return nil
			})
		if err != nil {
			return err
		}
		e.AppDir = strings.TrimRight(dir, "/")
		if verr := e.Validate("these answers"); verr != nil && strings.Contains(verr.Error(), "too broad") {
			ui.Warn("%s", strings.TrimPrefix(verr.Error(), "these answers: "))
			def = path.Join(e.AppDir, e.Project)
			continue
		}
		for {
			f, err := doctor.Facts(ctx, conn, e.AppDir, ports)
			if err != nil {
				return err
			}
			bad := 0
			for _, it := range doctor.Check(f, e, ports) {
				switch it.Status {
				case "problem":
					bad++
					ui.Fail(it.Title, "", it.Fix)
				case "warn":
					ui.Warn("%s", it.Title)
					if it.Fix != "" {
						ui.Note("%s", it.Fix)
					}
				}
			}
			if bad == 0 {
				ui.OK("The server is ready for this project.")
				return nil
			}
			again, err := ui.Confirm("Fixed it? Check again?", true, "Run the command shown on the server, then answer y and shipit checks once more.\nAnswer n to pick a different folder instead.")
			if err != nil {
				return err
			}
			if !again {
				break
			}
		}
		def = e.AppDir
	}
}

func (a *App) askEnvFile(e *config.Env, cf *detect.Compose) error {
	ui.Step(6, totalSteps, "Which file holds the keys and settings for "+strings.ToUpper(e.Name)+"?")
	files := detect.EnvFiles(a.Dir)
	var usable []detect.EnvFile
	var hidden []string
	for _, f := range files {
		if f.Local {
			hidden = append(hidden, f.Name)
			continue
		}
		usable = append(usable, f)
	}
	if len(files) == 0 {
		ui.Note("No .env files were found in this project.")
	}
	if len(hidden) > 0 {
		ui.Note("Not offered, because the name looks like local work: %s", strings.Join(hidden, ", "))
	}
	var opts []ui.Option
	def := 0
	for i, f := range usable {
		hint := fmt.Sprintf("%d keys", f.Keys)
		if f.Plain {
			hint += " · often used for local work, check it"
		}
		opts = append(opts, ui.Option{Label: f.Name, Hint: hint})
		if strings.Contains(strings.ToLower(f.Name), "prod") && e.Name == "production" {
			def = i
		}
	}
	opts = append(opts, ui.Option{Label: "This project needs no settings file"})
	i, err := ui.Select("", opts, def, "This file's values (database address, payment keys, secrets) are sent to the server,\nnot the file you use on your own computer. Pick the one made for "+e.Name+".\nshipit never prints the values, and your local files are never uploaded with the code.")
	if err != nil {
		return err
	}
	if i == len(usable) {
		e.EnvLinkAs = ""
		goto protect
	}
	e.EnvFile = usable[i].Name
	if usable[i].Plain {
		ok, cerr := ui.Confirm("Is "+e.EnvFile+" really the file for "+e.Name+" and not your local one?", false,
			"Many projects use a plain .env for work on their own computer. Sending that to a live\nserver could put test keys into production.")
		if cerr != nil {
			return cerr
		}
		if !ok {
			return errors.New("choose the production file, or create one such as .env.production, then run shipit init again")
		}
	}
	a.suggestRules(e)
protect:
	if e.Name == "production" {
		ok, err := ui.Confirm("Ask for extra confirmation before deploying to production?", true, "You will have to type the word production before a deploy starts. It prevents deploying by accident.")
		if err != nil {
			return err
		}
		e.Protected = ok
	}
	return nil
}

// suggestRules proposes checks based on the keys in the chosen file.
func (a *App) suggestRules(e *config.Env) {
	keys := detect.Keys(filepath.Join(a.Dir, e.EnvFile))
	set := map[string]bool{}
	for _, k := range keys {
		set[k] = true
	}
	var lines []string
	prod := e.Name == "production" || e.Name == "prod"
	if set["RAZORPAY_KEY_ID"] {
		pat, shown := "^rzp_test_", "rzp_test_"
		if prod {
			pat, shown = "^rzp_live_", "rzp_live_"
		}
		e.Rules["RAZORPAY_KEY_ID"] = pat
		lines = append(lines, "RAZORPAY_KEY_ID must start with "+shown)
		e.Require = append(e.Require, "RAZORPAY_KEY_ID")
	}
	if set["DATABASE_URL"] {
		e.Require = append(e.Require, "DATABASE_URL")
		lines = append(lines, "DATABASE_URL must be set")
	}
	if len(lines) == 0 {
		return
	}
	ui.Say("")
	ui.Say("  shipit can check the file before it is sent:")
	for _, l := range lines {
		ui.Say("    · %s", l)
	}
	ok, err := ui.Confirm("Add these checks?", true, "A wrong key, such as a test payment key in production, is stopped before anything is sent.")
	if err != nil || !ok {
		e.Rules, e.Require = map[string]string{}, nil
		return
	}
	if vals, verr := detect.Values(filepath.Join(a.Dir, e.EnvFile)); verr == nil {
		probe := *e
		probe.Protected = probe.Protected || prod // a live environment gets the stricter checks
		if deploy.PrintChecks(deploy.ValidateValues(&probe, vals)) > 0 {
			ui.Warn("The file does not pass yet. Fix it before sending: shipit secrets push %s", e.Name)
		}
	}
}

func (a *App) review(ctx context.Context, e *config.Env, conn transport.Conn) error {
	ui.Step(7, totalSteps, "Review")
	how := e.Runtime
	switch e.Runtime {
	case "compose":
		how = e.Engine + " compose, " + e.ComposeFile
	case "container":
		how = e.Engine + ", image from " + e.Build.Dockerfile
	case "pm2":
		how = "pm2"
	}
	row := func(k, v string) { ui.Say("  %-10s %s", k, v) }
	row("Project", e.Project+" · "+e.Name)
	row("Server", e.Server+"  "+ui.Dim(e.AppDir))
	row("Runs as", how)
	if e.Health != "" {
		row("Works when", e.Health+" answers")
	}
	if e.EnvFile != "" {
		row("Keys from", e.EnvFile+"  → server, linked as "+e.EnvLinkAs)
	}
	if e.Protected {
		row("Safety", "extra confirmation before each deploy")
	}
	if err := e.Validate(config.EnvPath(a.Dir, e.Name)); err != nil {
		return err
	}
	ok, err := ui.Confirm("Save these settings?", true, "They are saved in .shipit/"+e.Name+".conf. The file holds no secrets, so it is safe to commit to git.")
	if err != nil || !ok {
		return ui.ErrCancelled
	}
	if err := e.Save(a.Dir); err != nil {
		return err
	}
	ui.OK("Saved .shipit/%s.conf", e.Name)
	a.checkGitignore(e)

	d := &deploy.Deployer{Dir: a.Dir, Env: e, Conn: conn, Opt: a.opts()}
	if e.EnvFile != "" {
		send, err := ui.Confirm("Send your keys and settings to the server now?", true, "The server needs them before the first deploy. This sends "+e.EnvFile+" and backs up anything already there.")
		if err != nil {
			return err
		}
		if send {
			if err := d.PushEnv(ctx); err != nil {
				return err
			}
		}
	}
	go1, err := ui.Confirm("Deploy now?", false, "This sends the latest commit, builds it on the server and makes it live.\nYou can also do it later with: shipit deploy "+e.Name)
	if err != nil {
		return err
	}
	if go1 {
		return d.Deploy(ctx)
	}
	ui.Say("")
	ui.Say("Ready. Deploy any time with: %s", ui.Bold("shipit deploy "+e.Name))
	return nil
}

// checkGitignore warns when a settings file could end up in git.
func (a *App) checkGitignore(e *config.Env) {
	if e.EnvFile == "" {
		return
	}
	if err := exec.Command("git", "-C", a.Dir, "rev-parse", "--git-dir").Run(); err != nil {
		return
	}
	if out, _ := exec.Command("git", "-C", a.Dir, "ls-files", "--", e.EnvFile).Output(); strings.TrimSpace(string(out)) != "" {
		ui.Warn("%s is tracked by git, so its values are in your history.", e.EnvFile)
		ui.Note("Stop tracking it with: git rm --cached %s   and rotate any keys that were committed.", e.EnvFile)
		return
	}
	if exec.Command("git", "-C", a.Dir, "check-ignore", "-q", e.EnvFile).Run() == nil {
		return
	}
	ok, err := ui.Confirm(e.EnvFile+" is not in .gitignore. Add it?", true, "Keeps the file out of git so its keys cannot be committed by accident.")
	if err != nil || !ok {
		return
	}
	f, err := os.OpenFile(filepath.Join(a.Dir, ".gitignore"), os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.WriteString("\n" + e.EnvFile + "\n")
	ui.OK("Added %s to .gitignore.", e.EnvFile)
}
