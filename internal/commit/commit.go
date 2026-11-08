// Package commit turns uncommitted work into a commit with a message written
// from the changes. It never commits without a yes, and it never stages
// anything that looks like a key or a settings file.
package commit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// File is one changed file.
type File struct {
	Path    string
	Status  string // M changed, A added, D deleted, R renamed, ? new and not yet tracked
	Added   int
	Removed int
	Skipped bool // looks like a key or settings file, so it is never committed by shipit
}

// Set is everything that differs from the last commit.
type Set struct{ Files []File }

func git(dir string, stdin string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(e.String())
		if msg == "" {
			msg = strings.TrimSpace(o.String())
		}
		return msg, err
	}
	return o.String(), nil
}

var secretName = regexp.MustCompile(`(?i)(^\.env($|\.)|\.pem$|\.key$|\.p12$|\.pfx$|^id_(rsa|ed25519|ecdsa)|^credentials\.json$|service-account.*\.json$)`)

// SecretLike says whether a path looks like a key or a settings file.
func SecretLike(p string) bool {
	base := path.Base(filepath.ToSlash(p))
	l := strings.ToLower(base)
	for _, ok := range []string{".example", ".sample", ".template"} {
		if strings.HasSuffix(l, ok) {
			return false
		}
	}
	return secretName.MatchString(base)
}

// Scan lists what has changed since the last commit.
func Scan(dir string) (*Set, error) {
	out, err := git(dir, "", "status", "--porcelain=v1", "-uall", "-z")
	if err != nil {
		return nil, fmt.Errorf("could not read git status: %s", out)
	}
	set := &Set{}
	toks := strings.Split(out, "\x00")
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if len(t) < 4 {
			continue
		}
		xy, p := t[:2], t[3:]
		if xy[0] == 'R' || xy[0] == 'C' {
			i++ // the next token is the old name
		}
		st := "M"
		switch {
		case xy == "??":
			st = "?"
		case xy[0] == 'D' || xy[1] == 'D':
			st = "D"
		case xy[0] == 'A':
			st = "A"
		case xy[0] == 'R' || xy[0] == 'C':
			st = "R"
		}
		set.Files = append(set.Files, File{Path: p, Status: st, Skipped: SecretLike(p)})
	}
	sort.Slice(set.Files, func(a, b int) bool { return set.Files[a].Path < set.Files[b].Path })

	if num, nerr := git(dir, "", "diff", "HEAD", "--numstat"); nerr == nil {
		counts := map[string][2]int{}
		for _, l := range strings.Split(num, "\n") {
			f := strings.Split(l, "\t")
			if len(f) == 3 {
				a, _ := strconv.Atoi(f[0])
				r, _ := strconv.Atoi(f[1])
				counts[f[2]] = [2]int{a, r}
			}
		}
		for i := range set.Files {
			if c, ok := counts[set.Files[i].Path]; ok {
				set.Files[i].Added, set.Files[i].Removed = c[0], c[1]
			}
		}
	}
	for i := range set.Files {
		f := &set.Files[i]
		if f.Status == "?" && !f.Skipped {
			f.Added = countLines(filepath.Join(dir, f.Path))
		}
	}
	return set, nil
}

func countLines(p string) int {
	b, err := os.ReadFile(p)
	if err != nil || len(b) > 1<<20 || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
		return 0
	}
	return bytes.Count(b, []byte("\n"))
}

// Committable is what shipit would commit: everything except skipped files.
func (s *Set) Committable() []File {
	var out []File
	for _, f := range s.Files {
		if !f.Skipped {
			out = append(out, f)
		}
	}
	return out
}

// Skipped lists files left out because they look like keys or settings.
func (s *Set) Skipped() []File {
	var out []File
	for _, f := range s.Files {
		if f.Skipped {
			out = append(out, f)
		}
	}
	return out
}

// ---------- the message ----------

func category(p string) string {
	l := strings.ToLower(filepath.ToSlash(p))
	base := path.Base(l)
	switch {
	case strings.HasPrefix(l, ".github/"):
		return "ci"
	case strings.HasSuffix(base, ".md") || strings.HasPrefix(l, "docs/") || base == "license" || base == "licence":
		return "docs"
	case strings.HasPrefix(l, "test/") || strings.HasPrefix(l, "tests/") || strings.Contains(l, "_test.") ||
		strings.Contains(l, ".test.") || strings.Contains(l, ".spec.") || strings.Contains(l, "__tests__/"):
		return "test"
	case base == "dockerfile" || base == "containerfile" || strings.HasPrefix(base, "dockerfile.") || base == "makefile" ||
		base == "go.mod" || base == "go.sum" || base == "package.json" || base == "package-lock.json" ||
		base == "pnpm-lock.yaml" || base == "yarn.lock" || base == "bun.lockb" || base == "bun.lock" ||
		strings.HasPrefix(base, "docker-compose") || strings.HasPrefix(base, "compose.") || strings.HasPrefix(l, ".shipit/"):
		return "build"
	}
	return "code"
}

func verb(status string) string {
	switch status {
	case "A", "?":
		return "add"
	case "D":
		return "remove"
	case "R":
		return "rename"
	}
	return "update"
}

// Suggest writes a commit message from the changes alone. It reads file names,
// kinds and counts, not what the code means, so it is accurate but plain.
// Set commit_message_cmd to let another tool write a richer one.
func Suggest(files []File) string {
	if len(files) == 0 {
		return ""
	}
	cats := map[string]int{}
	for _, f := range files {
		cats[category(f.Path)]++
	}
	prefix := ""
	if len(cats) == 1 {
		for c := range cats {
			if c != "code" {
				prefix = c + ": "
			}
		}
	}
	// group file names by what happened to them
	order := []string{"add", "update", "rename", "remove"}
	groups := map[string][]string{}
	for _, f := range files {
		v := verb(f.Status)
		groups[v] = append(groups[v], path.Base(filepath.ToSlash(f.Path)))
	}
	var parts []string
	for _, v := range order {
		names := groups[v]
		if len(names) == 0 {
			continue
		}
		var what string
		switch {
		case len(names) <= 3:
			what = joinAnd(names)
		default:
			what = fmt.Sprintf("%d files", len(names))
		}
		parts = append(parts, v+" "+what)
	}
	subject := strings.Join(parts, ", ")
	if i := strings.LastIndex(subject, ", "); i >= 0 {
		subject = subject[:i] + " and " + subject[i+2:]
	}
	if prefix == "" {
		subject = strings.ToUpper(subject[:1]) + subject[1:]
	}
	subject = prefix + subject
	if len(subject) > 72 {
		scope := commonDir(files)
		where := "the project"
		if scope != "" {
			where = scope
		}
		subject = fmt.Sprintf("%sUpdate %d files in %s", prefix, len(files), where)
		if prefix != "" {
			subject = fmt.Sprintf("%supdate %d files in %s", prefix, len(files), where)
		}
	}
	if len(files) == 1 {
		return subject
	}
	var sb strings.Builder
	sb.WriteString(subject + "\n\n")
	for i, f := range files {
		if i == 15 {
			fmt.Fprintf(&sb, "- and %d more\n", len(files)-15)
			break
		}
		fmt.Fprintf(&sb, "- %s %s%s\n", verb(f.Status), f.Path, counts(f))
	}
	return strings.TrimRight(sb.String(), "\n")
}

func counts(f File) string {
	switch {
	case f.Added == 0 && f.Removed == 0:
		return ""
	case f.Removed == 0:
		return fmt.Sprintf(" (+%d)", f.Added)
	}
	return fmt.Sprintf(" (+%d -%d)", f.Added, f.Removed)
}

func joinAnd(n []string) string {
	switch len(n) {
	case 1:
		return n[0]
	case 2:
		return n[0] + " and " + n[1]
	}
	return strings.Join(n[:len(n)-1], ", ") + " and " + n[len(n)-1]
}

func commonDir(files []File) string {
	if len(files) == 0 {
		return ""
	}
	common := strings.Split(path.Dir(filepath.ToSlash(files[0].Path)), "/")
	for _, f := range files[1:] {
		d := strings.Split(path.Dir(filepath.ToSlash(f.Path)), "/")
		n := 0
		for n < len(common) && n < len(d) && common[n] == d[n] {
			n++
		}
		common = common[:n]
	}
	c := strings.Join(common, "/")
	if c == "." {
		return ""
	}
	return c
}

// Commit stages exactly the given files and commits only those paths with
// msg. Anything else already staged (including a key file you staged yourself)
// is left out of the commit and stays staged.
func Commit(dir string, files []File, msg string) error {
	if strings.TrimSpace(msg) == "" {
		return fmt.Errorf("the commit message is empty")
	}
	if len(files) == 0 {
		return fmt.Errorf("there is nothing to commit")
	}
	var add, remove, all []string
	for _, f := range files {
		all = append(all, f.Path)
		if f.Status == "D" {
			remove = append(remove, f.Path)
		} else {
			add = append(add, f.Path)
		}
	}
	if len(add) > 0 {
		if out, err := git(dir, "", append([]string{"add", "-A", "--"}, add...)...); err != nil {
			return fmt.Errorf("could not stage the files: %s", out)
		}
	}
	if len(remove) > 0 {
		if out, err := git(dir, "", append([]string{"rm", "--cached", "-q", "--ignore-unmatch", "--"}, remove...)...); err != nil {
			return fmt.Errorf("could not stage the removals: %s", out)
		}
	}
	args := append([]string{"commit", "-F", "-", "--only", "--"}, all...)
	if out, err := git(dir, msg+"\n", args...); err != nil {
		return fmt.Errorf("git could not commit: %s", firstLines(out, 6))
	}
	return nil
}

func firstLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[:n]
	}
	return strings.Join(l, " / ")
}
