package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Values returns every value of a repeated key in a section.
func (f *File) Values(section, key string) []string {
	var out []string
	if b := f.Block(section); b != nil {
		for _, e := range b.Entries {
			if e.Key == key {
				out = append(out, e.Value)
			}
		}
	}
	return out
}

// ---------- servers (stored once on the laptop) ----------

// Server is one machine shipit can deploy to.
type Server struct {
	Name string
	Type string // ssh | local
	Host string
	Port int
	User string
	Key  string
}

// Dir is where shipit keeps its own files on this computer.
func Dir() (string, error) {
	if d := os.Getenv("SHIPIT_CONFIG_DIR"); d != "" {
		return d, nil
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "shipit"), nil
}

func serversPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "servers.conf"), nil
}

// KnownHostsPath is shipit's own list of trusted server fingerprints.
func KnownHostsPath() (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "known_hosts"), nil
}

// LoadServers reads every saved server. A missing file means no servers yet.
func LoadServers() (map[string]*Server, error) {
	p, err := serversPath()
	if err != nil {
		return nil, err
	}
	f, err := ParseFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*Server{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*Server{}
	for _, name := range f.Sections() {
		get := func(k string) string { v, _ := f.Get(name, k); return v }
		s := &Server{Name: name, Type: get("type"), Host: get("host"), User: get("user"), Key: get("key")}
		if s.Type == "" {
			s.Type = "ssh"
		}
		s.Port = 22
		if v := get("port"); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 1 || n > 65535 {
				return nil, fmt.Errorf("server %q: port %q is not a valid port number", name, v)
			}
			s.Port = n
		}
		out[name] = s
	}
	return out, nil
}

// SaveServer adds or replaces one server.
func SaveServer(s *Server) error {
	p, err := serversPath()
	if err != nil {
		return err
	}
	f, err := ParseFile(p)
	if errors.Is(err, os.ErrNotExist) {
		f = newFile()
	} else if err != nil {
		return err
	}
	f.Set(s.Name, "type", s.Type)
	if s.Type == "ssh" {
		f.Set(s.Name, "host", s.Host)
		f.Set(s.Name, "port", strconv.Itoa(s.Port))
		f.Set(s.Name, "user", s.User)
		f.Set(s.Name, "key", s.Key)
	}
	return f.WriteFile(p, 0o600, "shipit servers. Stored on this computer only. Never commit this file.")
}

// RemoveServer deletes a saved server.
func RemoveServer(name string) error {
	p, err := serversPath()
	if err != nil {
		return err
	}
	f, err := ParseFile(p)
	if err != nil {
		return err
	}
	kept := f.Blocks[:0]
	found := false
	for _, b := range f.Blocks {
		if b.Name == name {
			found = true
			continue
		}
		kept = append(kept, b)
	}
	if !found {
		return fmt.Errorf("there is no server named %q", name)
	}
	f.Blocks = kept
	return f.WriteFile(p, 0o600, "shipit servers. Stored on this computer only. Never commit this file.")
}

// ---------- environments (stored in the project, no secrets) ----------

// ConfDir is the project folder that holds one settings file per environment.
const ConfDir = ".shipit"

// Build says how a container image is made.
type Build struct {
	Dockerfile string
	Context    string
	Target     string
	Platform   string
	Where      string // server (laptop and registry arrive later)
	Image      string
	Args       []string // NAME=value
}

// Env is one place a project is deployed to, such as production.
type Env struct {
	Name           string
	Project        string // names containers and images; saved so renaming the folder cannot change them
	Server         string
	Runtime        string // compose | container | pm2 | systemd | script
	Engine         string // docker | podman
	AppDir         string
	Branch         string
	Health         string
	HealthTimeout  int // seconds to wait for Health before going back
	Protected      bool
	Keep           int
	EnvFile        string // local file that holds this environment's values
	EnvLinkAs      string // name it gets inside every release
	Require        []string
	Rules          map[string]string // KEY -> regular expression its value must match
	ComposeFile    string
	ComposeProject string
	Port           string // container runtime: "3000:3000" or "127.0.0.1:3000:3000"
	Service        string // systemd unit
	PM2File        string
	BuildCmd       string
	StartCmd       string
	StopCmd        string
	StatusCmd      string
	PreSwitch      string
	PostDeploy     string
	LocalCheck     string
	CommitCmd      string // optional command that writes commit messages
	Preserve       []string
	Domain         string
	PushGit        bool
	Build          Build
}

var envNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidEnvName says whether a name is safe to use as a file and resource name.
func ValidEnvName(n string) bool { return envNameRe.MatchString(n) }

// ListEnvs returns the environment names defined in a project folder.
func ListEnvs(projectDir string) []string {
	m, _ := filepath.Glob(filepath.Join(projectDir, ConfDir, "*.conf"))
	var out []string
	for _, p := range m {
		n := strings.TrimSuffix(filepath.Base(p), ".conf")
		if n != "common" {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// EnvPath is where an environment's settings file lives.
func EnvPath(projectDir, name string) string {
	return filepath.Join(projectDir, ConfDir, name+".conf")
}

// LoadEnv reads and checks one environment. common.conf, when present,
// supplies defaults for every environment.
func LoadEnv(projectDir, name string) (*Env, error) {
	if !ValidEnvName(name) {
		return nil, fmt.Errorf("%q is not a usable environment name. Use lowercase letters, numbers and dashes", name)
	}
	p := EnvPath(projectDir, name)
	f, err := ParseFile(p)
	if errors.Is(err, os.ErrNotExist) {
		have := ListEnvs(projectDir)
		if len(have) == 0 {
			return nil, errors.New("this project is not set up yet. Run: shipit init")
		}
		return nil, fmt.Errorf("there is no environment named %q here. You have: %s", name, strings.Join(have, ", "))
	}
	if err != nil {
		return nil, err
	}
	if cf, cerr := ParseFile(filepath.Join(projectDir, ConfDir, "common.conf")); cerr == nil {
		merged := newFile()
		for _, b := range cf.Blocks {
			for _, e := range b.Entries {
				merged.Set(b.Name, e.Key, e.Value)
			}
		}
		for _, b := range f.Blocks {
			for _, e := range b.Entries {
				merged.Set(b.Name, e.Key, e.Value)
			}
		}
		f = merged
	}

	get := func(k string) string { v, _ := f.Get("", k); return v }
	e := &Env{
		Name: name, Project: get("project"), Server: get("server"), Runtime: get("runtime"), Engine: get("engine"),
		AppDir: get("app_dir"), Branch: get("branch"), Health: get("health"),
		EnvFile: get("env_file"), EnvLinkAs: get("env_link_as"),
		ComposeFile: get("compose_file"), ComposeProject: get("compose_project"), Port: get("port"),
		Service: get("service"), PM2File: get("pm2_file"),
		BuildCmd: get("build_cmd"), StartCmd: get("start_cmd"), StopCmd: get("stop_cmd"), StatusCmd: get("status_cmd"),
		PreSwitch: get("pre_switch"), PostDeploy: get("post_deploy"), LocalCheck: get("local_check"), CommitCmd: get("commit_message_cmd"),
		Domain:  get("domain"),
		Require: strings.Fields(get("require")), Preserve: strings.Fields(get("preserve")),
		Rules: map[string]string{},
	}
	e.Protected = isTrue(get("protected"))
	e.PushGit = isTrue(get("push_git"))
	e.Keep = 3
	e.HealthTimeout = 60
	if v := get("health_timeout"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 2 {
			return nil, fmt.Errorf("%s: health_timeout must be a number of seconds, 2 or more", p)
		}
		e.HealthTimeout = n
	}
	if v := get("keep"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return nil, fmt.Errorf("%s: keep must be a whole number of 1 or more", p)
		}
		e.Keep = n
	}
	if e.Branch == "" {
		e.Branch = "main"
	}
	if e.Project == "" {
		abs, aerr := filepath.Abs(projectDir)
		if aerr != nil {
			abs = projectDir
		}
		e.Project = slug(filepath.Base(abs))
	}
	if _, set := f.Get("", "env_link_as"); !set {
		e.EnvLinkAs = ".env"
	}
	if b := f.Block("rules"); b != nil {
		for _, en := range b.Entries {
			if _, err := regexp.Compile(en.Value); err != nil {
				return nil, fmt.Errorf("%s: the rule for %s is not a valid pattern: %v", p, en.Key, err)
			}
			e.Rules[en.Key] = en.Value
		}
	}
	bget := func(k string) string { v, _ := f.Get("build", k); return v }
	e.Build = Build{
		Dockerfile: bget("dockerfile"), Context: bget("context"), Target: bget("target"),
		Platform: bget("platform"), Where: bget("where"), Image: bget("image"),
		Args: f.Values("build", "arg"),
	}
	if e.Build.Where == "" {
		e.Build.Where = "server"
	}
	return e, e.Validate(p)
}

func isTrue(v string) bool {
	switch strings.ToLower(v) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// Validate checks an environment and explains problems in plain words.
func (e *Env) Validate(where string) error {
	need := func(v, key string) error {
		if v == "" {
			return fmt.Errorf("%s: %s is missing. Run: shipit init", where, key)
		}
		return nil
	}
	if err := need(e.Server, "server"); err != nil {
		return err
	}
	if err := need(e.Runtime, "runtime"); err != nil {
		return err
	}
	if err := need(e.AppDir, "app_dir"); err != nil {
		return err
	}
	if !strings.HasPrefix(e.AppDir, "/") {
		return fmt.Errorf("%s: app_dir must be a full path starting with /, not %q", where, e.AppDir)
	}
	switch strings.TrimRight(e.AppDir, "/") {
	case "", "/usr", "/etc", "/home", "/var", "/opt", "/root", "/bin", "/lib", "/boot":
		return fmt.Errorf("%s: app_dir %q is too broad. Use a folder just for this project, such as /opt/apps/myapp", where, e.AppDir)
	}
	switch e.Runtime {
	case "compose", "container":
		if e.Engine != "docker" && e.Engine != "podman" {
			return fmt.Errorf("%s: engine must be docker or podman", where)
		}
	case "systemd":
		if e.Service == "" {
			return fmt.Errorf("%s: runtime systemd needs service (the unit name)", where)
		}
	case "pm2":
		if e.PM2File == "" && e.StartCmd == "" {
			return fmt.Errorf("%s: runtime pm2 needs start_cmd or pm2_file", where)
		}
	case "script":
		if e.StartCmd == "" {
			return fmt.Errorf("%s: runtime script needs start_cmd", where)
		}
	default:
		return fmt.Errorf("%s: runtime %q is not one of compose, container, pm2, systemd, script", where, e.Runtime)
	}
	if e.Runtime == "compose" && e.ComposeFile == "" {
		return fmt.Errorf("%s: runtime compose needs compose_file", where)
	}
	if e.Runtime == "container" {
		if e.Build.Dockerfile == "" {
			return fmt.Errorf("%s: runtime container needs [build] dockerfile", where)
		}
		if e.Port == "" {
			return fmt.Errorf("%s: runtime container needs port, for example 3000:3000", where)
		}
	}
	if e.Build.Where != "server" {
		return fmt.Errorf("%s: building on %q is not available yet. Use where = server", where, e.Build.Where)
	}
	return nil
}

// ToFile turns an environment back into a settings file for saving.
func (e *Env) ToFile() *File {
	f := newFile()
	set := func(k, v string) {
		if v != "" {
			f.Set("", k, v)
		}
	}
	set("project", e.Project)
	set("server", e.Server)
	set("runtime", e.Runtime)
	set("engine", e.Engine)
	set("app_dir", e.AppDir)
	set("branch", e.Branch)
	set("health", e.Health)
	if e.HealthTimeout != 0 && e.HealthTimeout != 60 {
		f.Set("", "health_timeout", strconv.Itoa(e.HealthTimeout))
	}
	if e.Protected {
		f.Set("", "protected", "true")
	}
	set("env_file", e.EnvFile)
	if e.EnvLinkAs != ".env" {
		f.Set("", "env_link_as", e.EnvLinkAs)
	}
	set("require", strings.Join(e.Require, " "))
	set("compose_file", e.ComposeFile)
	set("compose_project", e.ComposeProject)
	set("port", e.Port)
	set("service", e.Service)
	set("pm2_file", e.PM2File)
	set("build_cmd", e.BuildCmd)
	set("start_cmd", e.StartCmd)
	set("stop_cmd", e.StopCmd)
	set("status_cmd", e.StatusCmd)
	set("pre_switch", e.PreSwitch)
	set("post_deploy", e.PostDeploy)
	set("local_check", e.LocalCheck)
	set("commit_message_cmd", e.CommitCmd)
	set("domain", e.Domain)
	set("preserve", strings.Join(e.Preserve, " "))
	if e.Build.Dockerfile != "" {
		f.Set("build", "dockerfile", e.Build.Dockerfile)
		if e.Build.Context != "" {
			f.Set("build", "context", e.Build.Context)
		}
		for _, kv := range [][2]string{{"target", e.Build.Target}, {"platform", e.Build.Platform}, {"image", e.Build.Image}} {
			if kv[1] != "" {
				f.Set("build", kv[0], kv[1])
			}
		}
		f.Set("build", "where", "server")
		if b := f.Block("build"); b != nil {
			for _, a := range e.Build.Args {
				b.Entries = append(b.Entries, Entry{"arg", a})
			}
		}
	}
	keys := make([]string, 0, len(e.Rules))
	for k := range e.Rules {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		f.Set("rules", k, e.Rules[k])
	}
	return f
}

// Save writes the environment to .shipit/<name>.conf.
func (e *Env) Save(projectDir string) error {
	return e.ToFile().WriteFile(EnvPath(projectDir, e.Name), 0o644,
		"shipit settings for "+e.Name+". Safe to commit: it holds no secrets.")
}

// Resource is the name used for containers, processes and image names, so
// projects on one server never collide.
func (e *Env) Resource() string {
	return "shipit-" + slug(e.Project) + "-" + e.Name
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	return strings.Trim(s, "-")
}

// Slug exposes the naming rule to other packages.
func Slug(s string) string { return slug(s) }
