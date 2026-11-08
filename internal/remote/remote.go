// Package remote holds the script that runs on servers and the code that
// feeds it settings and reads its answers.
package remote

import (
	"context"
	_ "embed"
	"io"
	"path"
	"sort"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/transport"
)

//go:embed engine.sh
var engine string

// Run sends the engine to the server with a mode and settings.
func Run(ctx context.Context, c transport.Conn, mode string, env map[string]string, out io.Writer) error {
	e := map[string]string{}
	for k, v := range env {
		e[k] = v
	}
	e["SHIPIT_MODE"] = mode
	return c.Run(ctx, engine, e, out)
}

// Capture runs a mode and returns everything it printed.
func Capture(ctx context.Context, c transport.Conn, mode string, env map[string]string) (string, error) {
	var sb strings.Builder
	err := Run(ctx, c, mode, env, &sb)
	return sb.String(), err
}

// EnvPath is where an environment's settings file lives on the server.
func EnvPath(e *config.Env) string { return path.Join(e.AppDir, "shared", ".env") }

// Settings turns an environment into the variables the engine reads.
func Settings(e *config.Env) map[string]string {
	m := map[string]string{
		"SHIPIT_APP_DIR":         e.AppDir,
		"SHIPIT_RUNTIME":         e.Runtime,
		"SHIPIT_ENGINE":          e.Engine,
		"SHIPIT_NAME":            e.Resource(),
		"SHIPIT_HEALTH_URL":      e.Health,
		"SHIPIT_HEALTH_TRIES":    itoa(tries(e.HealthTimeout)),
		"SHIPIT_HEALTH_SLEEP":    "2",
		"SHIPIT_KEEP":            itoa(e.Keep),
		"SHIPIT_ENV_LINK_AS":     e.EnvLinkAs,
		"SHIPIT_ENV_REMOTE_PATH": EnvPath(e),
		"SHIPIT_ENV_MODE":        "600",
		"SHIPIT_REQUIRED":        strings.Join(e.Require, " "),
		"SHIPIT_RULES":           rules(e.Rules),
		"SHIPIT_COMPOSE_FILE":    e.ComposeFile,
		"SHIPIT_COMPOSE_PROJECT": composeProject(e),
		"SHIPIT_PORT":            e.Port,
		"SHIPIT_SERVICE":         e.Service,
		"SHIPIT_PM2_FILE":        e.PM2File,
		"SHIPIT_BUILD_CMD":       e.BuildCmd,
		"SHIPIT_START_CMD":       e.StartCmd,
		"SHIPIT_STOP_CMD":        e.StopCmd,
		"SHIPIT_STATUS_CMD":      e.StatusCmd,
		"SHIPIT_PRE_SWITCH":      e.PreSwitch,
		"SHIPIT_POST_DEPLOY":     e.PostDeploy,
		"SHIPIT_PRESERVE":        strings.Join(e.Preserve, " "),
		"SHIPIT_DOCKERFILE":      e.Build.Dockerfile,
		"SHIPIT_BUILD_CONTEXT":   orDefault(e.Build.Context, "."),
		"SHIPIT_BUILD_TARGET":    e.Build.Target,
		"SHIPIT_BUILD_PLATFORM":  e.Build.Platform,
		"SHIPIT_BUILD_ARGS":      strings.Join(e.Build.Args, "\n"),
		"SHIPIT_IMAGE":           orDefault(e.Build.Image, e.Resource()),
	}
	return m
}

func composeProject(e *config.Env) string {
	if e.ComposeProject != "" {
		return e.ComposeProject
	}
	return e.Resource()
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func rules(r map[string]string) string {
	keys := make([]string, 0, len(r))
	for k := range r {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k + "\t" + r[k] + "\n")
	}
	return sb.String()
}

// KV parses key=value lines.
func KV(s string) map[string]string {
	m := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok {
			m[k] = v
		}
	}
	return m
}

// Check is one line of an env file check: ok, warn or problem.
type Check struct{ Status, Name, Message string }

// Checks parses status|name|message lines.
func Checks(s string) []Check {
	var out []Check
	for _, l := range strings.Split(s, "\n") {
		p := strings.SplitN(strings.TrimSpace(l), "|", 3)
		if len(p) == 3 && (p[0] == "ok" || p[0] == "warn" || p[0] == "problem") {
			out = append(out, Check{p[0], p[1], p[2]})
		}
	}
	return out
}

// tries turns a timeout in seconds into the number of 2-second checks.
func tries(timeout int) int {
	if timeout < 2 {
		return 30
	}
	return timeout / 2
}
