package archive

import (
	"archive/tar"
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

func TestExcluded(t *testing.T) {
	yes := []string{".env", ".env.production", "sub/.env.local", ".git/config", ".shipit/production.conf", "node_modules/x/y.js", "a/node_modules/b"}
	no := []string{".env.example", ".env.sample", "src/main.go", "environment.ts", "docker-compose.prod.yml", ".github/workflows/x.yml"}
	for _, p := range yes {
		if !Excluded(p) {
			t.Errorf("%s must be excluded", p)
		}
	}
	for _, p := range no {
		if Excluded(p) {
			t.Errorf("%s must be included", p)
		}
	}
}

func names(t *testing.T, b []byte) []string {
	t.Helper()
	var out []string
	tr := tar.NewReader(bytes.NewReader(b))
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			out = append(out, h.Name)
		}
	}
	sort.Strings(out)
	return out
}

func TestPackFolderSkipsSecrets(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{"app.js": "x", ".env.production": "SECRET=1", ".env.example": "SECRET=", "sub/ok.txt": "y"} {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(body), 0o644)
	}
	var buf bytes.Buffer
	if err := Pack(context.Background(), Source{Dir: dir}, map[string][]byte{"VERSION": []byte("v1")}, &buf); err != nil {
		t.Fatal(err)
	}
	got := names(t, buf.Bytes())
	want := []string{".env.example", "VERSION", "app.js", "sub/ok.txt"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
}

func TestPackGitCommitIgnoresWorkingFolderAndCommittedEnv(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	_ = os.WriteFile(filepath.Join(dir, "app.js"), []byte("v1"), 0o644)
	_ = os.WriteFile(filepath.Join(dir, ".env.production"), []byte("S=1"), 0o644) // committed by mistake
	run("add", "-f", ".")
	run("commit", "-qm", "one")
	run("tag", "v1")
	_ = os.WriteFile(filepath.Join(dir, "later.js"), []byte("not committed"), 0o644)

	var buf bytes.Buffer
	if err := Pack(context.Background(), Source{Dir: dir, Ref: "v1"}, nil, &buf); err != nil {
		t.Fatal(err)
	}
	got := names(t, buf.Bytes())
	if len(got) != 1 || got[0] != "app.js" {
		t.Fatalf("want only app.js, got %v", got)
	}
	if err := Pack(context.Background(), Source{Dir: dir, Ref: "no-such-tag"}, nil, io.Discard); err == nil {
		t.Fatal("an unknown ref must be an error")
	}
}
