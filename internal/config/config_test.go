package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseRoundTrip(t *testing.T) {
	src := `# top comment
server = here   # trailing note
note = "keeps # inside quotes"
[build]
arg = A=1
arg = B=2
[rules]
KEY = ^rzp_live_
`
	f, err := Parse(strings.NewReader(src), "test.conf")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Get("", "server"); v != "here" {
		t.Errorf("trailing comment not removed: %q", v)
	}
	if v, _ := f.Get("", "note"); v != "keeps # inside quotes" {
		t.Errorf("quoted value wrong: %q", v)
	}
	if got := f.Values("build", "arg"); len(got) != 2 || got[1] != "B=2" {
		t.Errorf("repeated key wrong: %v", got)
	}
	again, err := Parse(strings.NewReader(f.Render()), "again")
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := again.Get("", "note"); v != "keeps # inside quotes" {
		t.Errorf("round trip lost the quoted value: %q", v)
	}
}

func TestParseErrorsNameTheLine(t *testing.T) {
	_, err := Parse(strings.NewReader("ok = 1\nthis is wrong\n"), "x.conf")
	if err == nil || !strings.Contains(err.Error(), "x.conf line 2") {
		t.Fatalf("want an error naming line 2, got %v", err)
	}
	if _, err := Parse(strings.NewReader("[oops\n"), "y.conf"); err == nil {
		t.Fatal("an unclosed section must be an error")
	}
}

func writeEnv(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, ConfDir)
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "production.conf"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadEnvValidation(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"broad dir", "server=s\nruntime=script\nstart_cmd=x\napp_dir=/opt\n", "too broad"},
		{"relative dir", "server=s\nruntime=script\nstart_cmd=x\napp_dir=apps/x\n", "full path"},
		{"unknown runtime", "server=s\nruntime=magic\napp_dir=/srv/x\n", "not one of"},
		{"container needs port", "server=s\nruntime=container\nengine=docker\napp_dir=/srv/x\n[build]\ndockerfile=Dockerfile\n", "needs port"},
		{"bad rule", "server=s\nruntime=script\nstart_cmd=x\napp_dir=/srv/x\n[rules]\nK = (\n", "not a valid pattern"},
		{"laptop build not yet", "server=s\nruntime=script\nstart_cmd=x\napp_dir=/srv/x\n[build]\nwhere=laptop\n", "not available yet"},
	}
	for _, c := range cases {
		_, err := LoadEnv(writeEnv(t, c.body), "production")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want error containing %q, got %v", c.name, c.want, err)
		}
	}
}

func TestLoadEnvDefaultsAndSave(t *testing.T) {
	dir := writeEnv(t, "server=s\nruntime=script\nstart_cmd=./run\napp_dir=/srv/demo\nhealth=http://127.0.0.1:1/\n")
	e, err := LoadEnv(dir, "production")
	if err != nil {
		t.Fatal(err)
	}
	if e.Branch != "main" || e.Keep != 3 || e.HealthTimeout != 60 || e.EnvLinkAs != ".env" {
		t.Errorf("wrong defaults: %+v", e)
	}
	if e.Project == "" {
		t.Error("project should default to the folder name")
	}
	e.Name = "staging"
	e.EnvLinkAs = ".env.production"
	if err := e.Save(dir); err != nil {
		t.Fatal(err)
	}
	back, err := LoadEnv(dir, "staging")
	if err != nil {
		t.Fatal(err)
	}
	if back.EnvLinkAs != ".env.production" || back.Project != e.Project {
		t.Errorf("save and load disagree: %+v", back)
	}
}

func TestResourceNamesAreSafe(t *testing.T) {
	e := &Env{Name: "production", Project: "My Cool_App!"}
	if got := e.Resource(); got != "shipit-my-cool-app-production" {
		t.Errorf("got %q", got)
	}
}
