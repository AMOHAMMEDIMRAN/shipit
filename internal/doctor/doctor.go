// Package doctor asks a server what it has and says what is missing, with the
// fix, before a deploy fails because of it.
package doctor

import (
	"context"
	"fmt"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/remote"
	"github.com/amohammedimran/shipit/internal/transport"
)

// Item is one finding: ok, warn or problem.
type Item struct{ Status, Title, Fix string }

// Facts asks the server about itself. appDir and ports are optional.
func Facts(ctx context.Context, c transport.Conn, appDir string, ports []string) (map[string]string, error) {
	env := map[string]string{"SHIPIT_APP_DIR": appDir, "SHIPIT_PORTS": strings.Join(ports, " ")}
	raw, err := remote.Capture(ctx, c, "probe", env)
	if err != nil {
		return nil, fmt.Errorf("could not look at the server: %v", err)
	}
	return remote.KV(raw), nil
}

// Summary is the one-line "Found:" text.
func Summary(f map[string]string) string {
	parts := []string{f["os"], f["arch"]}
	for _, t := range []string{"docker", "podman", "pm2", "node"} {
		if f["tool."+t] == "yes" {
			if v := strings.TrimPrefix(f["ver."+t], "v"); v != "" {
				parts = append(parts, t+" "+v)
			} else {
				parts = append(parts, t)
			}
		}
	}
	if f["tool.systemctl"] == "yes" {
		parts = append(parts, "systemd")
	}
	return strings.Join(parts, " · ")
}

// Engines lists the container engines that work for this login.
func Engines(f map[string]string) []string {
	var out []string
	if f["tool.docker"] == "yes" && f["docker.access"] == "yes" {
		out = append(out, "docker")
	}
	if f["tool.podman"] == "yes" && f["podman.access"] == "yes" {
		out = append(out, "podman")
	}
	return out
}

// Check judges the facts for one environment.
func Check(f map[string]string, e *config.Env, ports []string) []Item {
	var out []Item
	add := func(status, title, fix string) { out = append(out, Item{status, title, fix}) }

	switch f["kernel"] {
	case "Linux", "Darwin":
		add("ok", "The server runs "+f["os"]+" ("+f["arch"]+")", "")
	default:
		add("problem", "This server type is not supported yet", "Windows servers arrive in a later version. Use a Linux or macOS server for now.")
	}
	for _, t := range []string{"tar", "curl"} {
		if f["tool."+t] == "yes" {
			add("ok", t+" is installed", "")
		} else {
			add("problem", t+" is missing", "Install it with the server's package manager, for example: sudo apt install "+t)
		}
	}
	switch e.Runtime {
	case "compose", "container":
		eng := e.Engine
		switch {
		case f["tool."+eng] != "yes":
			add("problem", eng+" is not installed", "Install "+eng+" on the server, or choose the other engine with: shipit init")
		case f[eng+".access"] != "yes":
			fix := "Check that this login may use " + eng + "."
			if eng == "docker" {
				fix = "Add this login to the docker group: sudo usermod -aG docker $USER, then log out and in again."
			}
			add("problem", eng+" is installed but this login cannot use it", fix)
		default:
			add("ok", eng+" works for this login", "")
		}
		if e.Runtime == "compose" && f["tool."+eng] == "yes" {
			if f[eng+".compose"] == "yes" {
				add("ok", eng+" compose is available", "")
			} else {
				add("problem", eng+" compose is not available", "Install the compose plugin for "+eng+".")
			}
		}
		if eng == "podman" && f["podman.rootless"] == "true" {
			if f["linger"] == "yes" {
				add("ok", "Podman containers keep running after you log out", "")
			} else {
				add("warn", "Podman containers will stop when you log out", "Run once on the server: sudo loginctl enable-linger $USER")
			}
		}
	case "pm2":
		if f["tool.pm2"] == "yes" {
			add("ok", "pm2 is installed", "")
		} else {
			add("problem", "pm2 is missing, or does not run for this login", "Install it on the server with: npm install -g pm2, and check that pm2 --version works over SSH.")
		}
	case "systemd":
		if f["tool.systemctl"] != "yes" {
			add("problem", "systemd is not available on this server", "Choose another way to run it with: shipit init")
		}
		if f["sudo"] == "yes" {
			add("ok", "This login can restart services", "")
		} else {
			add("problem", "This login cannot restart services", "Allow it without a password: add  "+f["user"]+" ALL=(root) NOPASSWD: /usr/bin/systemctl restart "+e.Service+"  with visudo.")
		}
	}
	switch {
	case f["appdir.state"] == "":
	case f["appdir.writable"] != "yes":
		add("problem", e.AppDir+" cannot be created or written by this login", "Run once on the server: sudo mkdir -p "+e.AppDir+" && sudo chown $USER "+e.AppDir)
	case f["appdir.state"] == "other":
		add("warn", e.AppDir+" already has files that shipit did not put there", "shipit only adds releases/ and shared/ next to them. Pick another folder if this is a different project.")
	case f["appdir.state"] == "missing":
		add("ok", e.AppDir+" will be created", "")
	default:
		add("ok", e.AppDir+" is ready", "")
	}
	for _, p := range ports {
		if f["port."+p] == "used" {
			add("warn", "Port "+p+" is already in use on the server", "That is expected if it is this project's current version. If not, choose another port.")
		}
	}
	return out
}
