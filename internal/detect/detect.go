// Package detect looks at a project folder and reports what it recognises, so
// the person can pick and shipit can work out the rest. It never assumes.
package detect

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Candidate is one way shipit could deploy this project.
type Candidate struct {
	Kind     string // compose | container | pm2 | node | static | quadlet
	File     string // relative path of the file that gave it away
	Evidence string // one short line shown to the person
	Note     string // a warning or hint, may be empty
	Score    int    // higher is likelier
	Compose  *Compose
}

var composeNames = regexp.MustCompile(`^(docker-compose|compose)(\.[a-z0-9_-]+)?\.ya?ml$`)

// Scan reports every candidate in dir, likeliest first.
func Scan(dir string) []Candidate {
	var out []Candidate
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case composeNames.MatchString(name):
			out = append(out, composeCandidate(dir, name))
		case name == "Dockerfile" || name == "Containerfile" || strings.HasPrefix(name, "Dockerfile.") || strings.HasPrefix(name, "Containerfile."):
			out = append(out, Candidate{Kind: "container", File: name, Score: 20,
				Evidence: "a single image, built from " + name})
		case strings.HasPrefix(name, "ecosystem.config."):
			out = append(out, Candidate{Kind: "pm2", File: name, Score: 40, Evidence: "a pm2 process file"})
		case strings.HasSuffix(name, ".container"):
			out = append(out, Candidate{Kind: "quadlet", File: name, Score: 15,
				Evidence: "a Podman Quadlet unit", Note: "Quadlet support arrives in a later version"})
		}
	}
	if c, ok := nodeCandidate(dir); ok {
		out = append(out, c)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err == nil {
		out = append(out, Candidate{Kind: "static", File: "index.html", Score: 5, Evidence: "a static site (index.html)",
			Note: "static sites arrive in a later version"})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
	return out
}

func composeCandidate(dir, name string) Candidate {
	c := Candidate{Kind: "compose", File: name, Score: 50}
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "prod"):
		c.Score += 30
	case strings.Contains(lower, "stag"):
		c.Score += 10
	case strings.Contains(lower, "dev") || strings.Contains(lower, "local") || strings.Contains(lower, "test"):
		c.Score -= 30
		c.Note = "looks like it is for local development"
	case name == "docker-compose.yml" || name == "compose.yml" || name == "compose.yaml" || name == "docker-compose.yaml":
		c.Score -= 10
		c.Note = "a plain name is often the local development file"
	}
	cf, err := ParseCompose(filepath.Join(dir, name))
	if err != nil {
		c.Evidence = "a compose file, but shipit could not read it"
		c.Note = err.Error()
		c.Score -= 40
		return c
	}
	c.Compose = cf
	names := make([]string, len(cf.Services))
	for i, s := range cf.Services {
		names[i] = s.Name
	}
	c.Evidence = fmt.Sprintf("compose, %d service%s (%s)", len(cf.Services), plural(len(cf.Services)), strings.Join(names, ", "))
	if cf.AnyBuild() {
		c.Evidence += ", builds from source"
	}
	return c
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func nodeCandidate(dir string) (Candidate, bool) {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return Candidate{}, false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(b, &pkg) != nil {
		return Candidate{}, false
	}
	if _, ok := pkg.Scripts["start"]; !ok {
		return Candidate{}, false
	}
	return Candidate{Kind: "node", File: "package.json", Score: 30,
		Evidence: "a Node app with a start script (" + pkg.Scripts["start"] + ")"}, true
}

// PackageManager guesses install and build commands from the lockfile.
func PackageManager(dir string) (install, build string) {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(dir, f)); return err == nil }
	var b []byte
	b, _ = os.ReadFile(filepath.Join(dir, "package.json"))
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	_ = json.Unmarshal(b, &pkg)
	run := "npm run build"
	switch {
	case has("pnpm-lock.yaml"):
		install, run = "pnpm install --frozen-lockfile", "pnpm build"
	case has("yarn.lock"):
		install, run = "yarn install --frozen-lockfile", "yarn build"
	case has("bun.lockb") || has("bun.lock"):
		install, run = "bun install --frozen-lockfile", "bun run build"
	case has("package-lock.json"):
		install = "npm ci"
	default:
		install = "npm install"
	}
	if _, ok := pkg.Scripts["build"]; ok {
		return install, install + " && " + run
	}
	return install, install
}

// ---------- env files ----------

// EnvFile is a settings file found in the project. Only counts are kept.
type EnvFile struct {
	Name  string
	Keys  int
	Local bool // the name suggests it is for local work
	Plain bool // exactly ".env", which many projects use for local work
	Note  string
}

var localName = regexp.MustCompile(`(?i)(local|development|\bdev\b|\.dev$|test)`)

// EnvFiles lists the .env files in dir with how many keys each has.
func EnvFiles(dir string) []EnvFile {
	entries, _ := os.ReadDir(dir)
	var out []EnvFile
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !(n == ".env" || strings.HasPrefix(n, ".env.")) {
			continue
		}
		low := strings.ToLower(n)
		if strings.HasSuffix(low, ".example") || strings.HasSuffix(low, ".sample") || strings.HasSuffix(low, ".template") || strings.Contains(low, ".bak") {
			continue
		}
		f := EnvFile{Name: n, Keys: CountKeys(filepath.Join(dir, n))}
		f.Plain = n == ".env"
		f.Local = localName.MatchString(strings.TrimPrefix(n, ".env"))
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

var keyRe = regexp.MustCompile(`^\s*(?:export\s+)?([A-Za-z_][A-Za-z0-9_]*)\s*=`)

// CountKeys counts KEY=value lines without reading the values.
func CountKeys(path string) int {
	return len(Keys(path))
}

// Keys returns the key names in an env file.
func Keys(path string) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	var out []string
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if m := keyRe.FindStringSubmatch(sc.Text()); m != nil {
			out = append(out, m[1])
		}
	}
	return out
}

// Values reads an env file into a map. Used on this computer only, to check
// rules before anything is sent. Values are never printed.
func Values(path string) (map[string]string, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	out := map[string]string{}
	sc := bufio.NewScanner(fh)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		m := keyRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		v := strings.TrimSpace(line[len(m[0]):])
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		out[m[1]] = v
	}
	return out, sc.Err()
}

// LooksLocal says whether an env file name suggests it is for local work.
func LooksLocal(name string) bool {
	return localName.MatchString(strings.TrimPrefix(name, ".env"))
}
