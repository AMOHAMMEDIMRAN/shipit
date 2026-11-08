package detect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const backendCompose = `name: devpply-backend-prod
services:
  prod-redis:
    image: redis:7-alpine
    container_name: devpply-prod-redis
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
  prod-api:
    build:
      context: .
      dockerfile: Dockerfile
    image: ghcr.io/amohammedimran/devpply-backend:main
    container_name: devpply-prod-api
    env_file:
      - .env.production
    ports:
      - "127.0.0.1:6000:8000"
volumes:
  prod_redis_data:
    name: devpply_prod_redis_data
`

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestComposeFacts(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.prod.yml", backendCompose)
	cf, err := ParseCompose(filepath.Join(dir, "docker-compose.prod.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if cf.Name != "devpply-backend-prod" || len(cf.Services) != 2 {
		t.Fatalf("wrong parse: %+v", cf)
	}
	if !cf.AnyBuild() || !cf.FixedNames() {
		t.Error("should see a build and fixed container names")
	}
	if got := cf.HealthURL(); got != "http://127.0.0.1:6000/" {
		t.Errorf("health url: %q", got)
	}
	if got := cf.HostPorts(); len(got) != 1 || got[0] != "6000" {
		t.Errorf("host ports: %v", got)
	}
	var api Service
	for _, s := range cf.Services {
		if s.Name == "prod-api" {
			api = s
		}
	}
	if len(api.EnvFiles) != 1 || api.EnvFiles[0] != ".env.production" {
		t.Errorf("env files: %v", api.EnvFiles)
	}
}

func TestOverrideOnlyTouchesBuiltServices(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "c.yml", backendCompose)
	cf, _ := ParseCompose(filepath.Join(dir, "c.yml"))
	o := cf.Override("devply", "v1.4.0")
	if !strings.Contains(o, "image: ghcr.io/amohammedimran/devpply-backend:v1.4.0") {
		t.Errorf("built service not retagged:\n%s", o)
	}
	if strings.Contains(o, "prod-redis") {
		t.Errorf("a service that is only pulled must not be overridden:\n%s", o)
	}
}

func TestPortForms(t *testing.T) {
	cases := map[string]bool{"8000": false, "3000:3000": true, "127.0.0.1:4000:3000": true, "3000:3000/tcp": true}
	for in, want := range cases {
		_, ok := parsePort(in)
		if ok != want {
			t.Errorf("%s: published=%v want %v", in, ok, want)
		}
	}
}

func TestScanPrefersProductionCompose(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "docker-compose.yml", "services:\n  a:\n    image: x\n")
	write(t, dir, "docker-compose.prod.yml", "services:\n  a:\n    image: x\n")
	write(t, dir, "Dockerfile", "FROM scratch\n")
	got := Scan(dir)
	if len(got) < 3 || got[0].File != "docker-compose.prod.yml" {
		t.Fatalf("production file should come first: %+v", got)
	}
	for _, c := range got {
		if c.File == "docker-compose.yml" && c.Note == "" {
			t.Error("a plain compose name should carry a local-development hint")
		}
	}
}

func TestEnvFilesHideLocalAndNeverReadValues(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, ".env", "A=1\nB=2\n")
	write(t, dir, ".env.local", "A=1\n")
	write(t, dir, ".env.production", "A=1\nB=2\nC=3\n")
	write(t, dir, ".env.example", "A=\n")
	got := EnvFiles(dir)
	byName := map[string]EnvFile{}
	for _, f := range got {
		byName[f.Name] = f
	}
	if _, ok := byName[".env.example"]; ok {
		t.Error(".env.example should not be listed")
	}
	if !byName[".env.local"].Local || byName[".env.production"].Local {
		t.Errorf("local detection wrong: %+v", byName)
	}
	if !byName[".env"].Plain || byName[".env.production"].Keys != 3 {
		t.Errorf("plain or key count wrong: %+v", byName)
	}
}
