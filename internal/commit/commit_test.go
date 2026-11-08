package commit

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
		return string(out)
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	write(t, dir, "app.go", "package main\n")
	write(t, dir, "README.md", "# demo\n")
	run("add", "-A")
	run("commit", "-qm", "first")
	return dir
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSecretLike(t *testing.T) {
	yes := []string{".env", ".env.production", "sub/.env.local", "cert.pem", "server.key", "id_rsa", "credentials.json", "my-service-account-1.json"}
	no := []string{".env.example", ".env.sample", "environment.go", "keyboard.go", "README.md", "monkey.txt", "docker-compose.prod.yml"}
	for _, p := range yes {
		if !SecretLike(p) {
			t.Errorf("%s must be treated as secret", p)
		}
	}
	for _, p := range no {
		if SecretLike(p) {
			t.Errorf("%s must not be treated as secret", p)
		}
	}
}

func TestScanSeparatesSecretsAndCountsLines(t *testing.T) {
	dir := repo(t)
	write(t, dir, "app.go", "package main\n\nfunc main() {}\n")
	write(t, dir, "new.go", "package main\n// new\n")
	write(t, dir, ".env.production", "KEY=1\n")
	if err := os.Remove(filepath.Join(dir, "README.md")); err != nil {
		t.Fatal(err)
	}
	set, err := Scan(dir)
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]File{}
	for _, f := range set.Files {
		by[f.Path] = f
	}
	if by["app.go"].Status != "M" || by["app.go"].Added != 2 {
		t.Errorf("app.go: %+v", by["app.go"])
	}
	if by["new.go"].Status != "?" || by["new.go"].Added != 2 {
		t.Errorf("new.go: %+v", by["new.go"])
	}
	if by["README.md"].Status != "D" {
		t.Errorf("README.md: %+v", by["README.md"])
	}
	if !by[".env.production"].Skipped {
		t.Error(".env.production must be skipped")
	}
	if got := len(set.Committable()); got != 3 {
		t.Errorf("want 3 committable files, got %d", got)
	}
}

func TestSuggest(t *testing.T) {
	cases := []struct {
		name  string
		files []File
		want  string
	}{
		{"one doc", []File{{Path: "README.md", Status: "M"}}, "docs: update README.md"},
		{"new code and changed code", []File{{Path: "internal/a.go", Status: "?"}, {Path: "internal/b.go", Status: "M"}}, "Add a.go and update b.go"},
		{"only tests", []File{{Path: "x_test.go", Status: "A"}}, "test: add x_test.go"},
		{"build files", []File{{Path: "go.mod", Status: "M"}, {Path: "Makefile", Status: "M"}}, "build: update go.mod and Makefile"},
		{"many files", []File{{Path: "a/1.go", Status: "M"}, {Path: "a/2.go", Status: "M"}, {Path: "a/3.go", Status: "M"}, {Path: "a/4.go", Status: "M"}}, "Update 4 files"},
	}
	for _, c := range cases {
		got, _, _ := strings.Cut(Suggest(c.files), "\n")
		if got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
	if Suggest(nil) != "" {
		t.Error("no files, no message")
	}
	long := Suggest([]File{{Path: "a/1.go", Status: "?"}, {Path: "a/2.go", Status: "M"}, {Path: "docs/averyveryverylongfilenamethatgoeson.md", Status: "D"}, {Path: "a/very-long-file-name-number-two.go", Status: "R"}})
	if first, _, _ := strings.Cut(long, "\n"); len(first) > 72 {
		t.Errorf("subject is too long (%d): %q", len(first), first)
	}
}

func TestCommitStagesOnlyTheListedFiles(t *testing.T) {
	dir := repo(t)
	write(t, dir, "app.go", "package main\n// changed\n")
	write(t, dir, ".env.production", "SECRET=1\n")
	set, _ := Scan(dir)
	if err := Commit(dir, set.Committable(), "Update app.go"); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "show", "--stat", "--format=%s", "HEAD").CombinedOutput()
	if !strings.Contains(string(out), "Update app.go") || !strings.Contains(string(out), "app.go") {
		t.Errorf("commit is wrong:\n%s", out)
	}
	if strings.Contains(string(out), ".env.production") {
		t.Fatalf("a settings file was committed:\n%s", out)
	}
	if status, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput(); !strings.Contains(string(status), ".env.production") {
		t.Error("the settings file should still be untracked, untouched")
	}
	if err := Commit(dir, nil, "  "); err == nil {
		t.Error("an empty message must be refused")
	}
}

func TestGenerateUsesTheCommandAndFallsBack(t *testing.T) {
	dir := repo(t)
	write(t, dir, "app.go", "package main\n// changed\n")
	write(t, dir, ".env.production", "TOPSECRETVALUE=1\n")
	set, _ := Scan(dir)

	got, err := Generate(dir, set.Committable(), `cat > /dev/null; printf '%s\n' '`+"```"+`' 'Make app say hello' '`+"```"+`'`)
	if err != nil || got != "Make app say hello" {
		t.Fatalf("want the command's message with fences removed, got %q, %v", got, err)
	}
	in := buildInput(dir, set.Skipped())
	if strings.Contains(in, "TOPSECRETVALUE") {
		t.Fatal("the contents of a settings file must never be sent to the message command")
	}
	if _, err := Generate(dir, set.Committable(), "exit 3"); err == nil {
		t.Error("a failing command must be an error so the caller can fall back")
	}
	if _, err := Generate(dir, set.Committable(), "cat > /dev/null"); err == nil {
		t.Error("an empty answer must be an error")
	}
}

func TestCommitLeavesAKeyFileYouStagedYourself(t *testing.T) {
	dir := repo(t)
	write(t, dir, "app.go", "package main\n// changed\n")
	write(t, dir, ".env.production", "SECRET=1\n")
	if out, err := exec.Command("git", "-C", dir, "add", "-f", ".env.production").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	set, _ := Scan(dir)
	if err := Commit(dir, set.Committable(), "Update app.go"); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("git", "-C", dir, "show", "--name-only", "--format=", "HEAD").CombinedOutput()
	if strings.Contains(string(out), "env.production") {
		t.Fatalf("a key file you staged yourself must not ride along:\n%s", out)
	}
}

func TestCommitHandlesStagedAndUnstagedDeletions(t *testing.T) {
	dir := repo(t)
	if out, err := exec.Command("git", "-C", dir, "rm", "-q", "README.md").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out) // deletion already staged
	}
	write(t, dir, "extra.go", "package main\n")
	if out, err := exec.Command("git", "-C", dir, "add", "extra.go").CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if err := exec.Command("git", "-C", dir, "commit", "-qm", "add extra").Run(); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, "extra.go")) // deletion not staged
	write(t, dir, "app.go", "package main\n// two\n")
	set, _ := Scan(dir)
	if err := Commit(dir, set.Committable(), "Remove extra.go and update app.go"); err != nil {
		t.Fatal(err)
	}
	if st, _ := exec.Command("git", "-C", dir, "status", "--porcelain").CombinedOutput(); strings.TrimSpace(string(st)) != "" {
		t.Errorf("everything should be committed, got:\n%s", st)
	}
}
