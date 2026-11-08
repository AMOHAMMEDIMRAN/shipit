package cli

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/deploy"
	"github.com/amohammedimran/shipit/internal/doctor"
	"github.com/amohammedimran/shipit/internal/transport"
	"github.com/amohammedimran/shipit/internal/ui"
)

func (a *App) server(ctx context.Context) error {
	sub := ""
	if len(a.a.pos) > 0 {
		sub, a.a.pos = a.a.pos[0], a.a.pos[1:]
	}
	switch sub {
	case "", "list":
		servers, err := config.LoadServers()
		if err != nil {
			return err
		}
		if len(servers) == 0 {
			fmt.Println("No servers yet. Add one with: shipit server add")
			return nil
		}
		for _, s := range servers {
			where := "this computer"
			if s.Type == "ssh" {
				where = fmt.Sprintf("%s@%s:%d", s.User, s.Host, s.Port)
			}
			fmt.Printf("  %-14s %s\n", s.Name, ui.Dim(where))
		}
		return nil
	case "add":
		s, conn, facts, err := serverFlow(ctx, 1, 1)
		if err != nil {
			return err
		}
		conn.Close()
		ui.OK("Saved the server as %s.", ui.Bold(s.Name))
		ui.Note("Found: %s", doctor.Summary(facts))
		return nil
	case "test":
		return a.serverTest(ctx)
	case "remove", "rm":
		if len(a.a.pos) == 0 {
			return errors.New("say which server to remove: shipit server remove <name>")
		}
		if err := config.RemoveServer(a.a.pos[0]); err != nil {
			return err
		}
		ui.OK("Removed %s from this computer. Nothing was changed on the server.", a.a.pos[0])
		return nil
	}
	return fmt.Errorf("shipit server does not have a %q command. Try: add, list, test, remove", sub)
}

func (a *App) serverTest(ctx context.Context) error {
	servers, err := config.LoadServers()
	if err != nil {
		return err
	}
	if len(a.a.pos) == 0 {
		return errors.New("say which server to test: shipit server test <name>")
	}
	s, ok := servers[a.a.pos[0]]
	if !ok {
		return fmt.Errorf("there is no server called %q. See: shipit server list", a.a.pos[0])
	}
	conn, err := openServer(s)
	if err != nil {
		ui.Fail("Could not connect to "+s.Name+".", err.Error(), "")
		return deploy.ErrStopped
	}
	defer conn.Close()
	ui.OK("Connected to %s.", conn.Describe())
	facts, err := doctor.Facts(ctx, conn, "", nil)
	if err != nil {
		return err
	}
	ui.Note("Found: %s", doctor.Summary(facts))
	return nil
}

// foundKeys lists private keys in ~/.ssh.
func foundKeys() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	var out []string
	for _, n := range []string{"id_ed25519", "id_ecdsa", "id_rsa"} {
		p := filepath.Join(home, ".ssh", n)
		if _, err := os.Stat(p); err == nil {
			out = append(out, "~/.ssh/"+n)
		}
	}
	return out
}

func splitHostPort(s string) (string, int, error) {
	s = strings.TrimSpace(s)
	if h, p, err := net.SplitHostPort(s); err == nil {
		n, perr := strconv.Atoi(p)
		if perr != nil || n < 1 || n > 65535 {
			return "", 0, fmt.Errorf("%q is not a port number", p)
		}
		return h, n, nil
	}
	if strings.ContainsAny(s, " /") || s == "" {
		return "", 0, errors.New("type an address like 203.0.113.10 or myserver.example.com")
	}
	return s, 22, nil
}

const rdpSteps = `Remote Desktop (RDP) shows a screen to a person. Programs cannot send files
and commands through it, so shipit cannot deploy over RDP.

Turn on OpenSSH once, while you are connected over RDP. In PowerShell as Administrator:

  Add-WindowsCapability -Online -Name OpenSSH.Server~~~~0.0.1.0
  Start-Service sshd
  Set-Service -Name sshd -StartupType Automatic

Then open port 22 in the Windows firewall, and add your public key to
C:\ProgramData\ssh\administrators_authorized_keys (administrator accounts)
or C:\Users\<name>\.ssh\authorized_keys (other accounts).

Windows servers are supported in a later version of shipit. Linux and macOS work now.`

// serverFlow asks about a server, connects, and saves it. step and total number the questions.
func serverFlow(ctx context.Context, step, total int) (*config.Server, transport.Conn, map[string]string, error) {
	if !ui.Interactive() {
		return nil, nil, nil, errors.New("adding a server needs a terminal to ask questions in")
	}
	ui.Step(step, total, "How do you connect to your server?")
	i, err := ui.Select("", []ui.Option{
		{Label: "SSH with a key", Hint: "Linux, macOS, Windows with OpenSSH"},
		{Label: "This computer", Hint: "no connection needed"},
		{Label: "Remote Desktop (RDP) only", Hint: "shows how to turn on SSH"},
	}, 0, "SSH is how shipit logs in to your server and runs commands.\nMost servers already have it. You need the server's address,\na user name, and a key file on this computer.\nPick 'This computer' to deploy to the machine you are on.")
	if err != nil {
		return nil, nil, nil, err
	}
	if i == 2 {
		ui.Say("")
		ui.Say("%s", rdpSteps)
		return nil, nil, nil, ui.ErrCancelled
	}
	s := &config.Server{Type: "ssh", Port: 22}
	if i == 1 {
		s.Type = "local"
	}
	for {
		if s.Type == "ssh" {
			addr, err := ui.Input("Server address", s.Host, "The IP address or name of your server, for example 203.0.113.10.\nIf SSH is not on port 22, add it: 203.0.113.10:2222",
				func(v string) error { _, _, e := splitHostPort(v); return e })
			if err != nil {
				return nil, nil, nil, err
			}
			s.Host, s.Port, _ = splitHostPort(addr)
			def := s.User
			if def == "" {
				if u, uerr := user.Current(); uerr == nil {
					def = u.Username
				}
			}
			s.User, err = ui.Input("User name on the server", def, "The login you use with ssh, for example ubuntu or deploy.", nil)
			if err != nil {
				return nil, nil, nil, err
			}
			keyDef := s.Key
			if keyDef == "" {
				if ks := foundKeys(); len(ks) > 0 {
					keyDef = ks[0]
				}
			}
			s.Key, err = ui.Input("Key file", keyDef, "The private key that matches a public key in ~/.ssh/authorized_keys on the server.\nIt is usually ~/.ssh/id_ed25519. shipit only reads it. It is never copied.",
				func(v string) error {
					if _, e := os.Stat(transport.ExpandHome(v)); e != nil {
						return fmt.Errorf("there is no file at %s", v)
					}
					return nil
				})
			if err != nil {
				return nil, nil, nil, err
			}
		}
		conn, err := openServer(s)
		if err == nil {
			ui.OK("Connected to %s.", conn.Describe())
			facts, ferr := doctor.Facts(ctx, conn, "", nil)
			if ferr != nil {
				conn.Close()
				return nil, nil, nil, ferr
			}
			ui.Note("Found: %s", doctor.Summary(facts))
			if facts["kernel"] != "Linux" && facts["kernel"] != "Darwin" {
				ui.Warn("This does not look like a Linux or macOS server. Windows servers arrive in a later version.")
			}
			def := s.Name
			if def == "" {
				def = defaultServerName(s)
			}
			s.Name, err = ui.Input("Give this server a name", def, "A short name you will recognise, such as hetzner-1. Projects refer to the server by this name.",
				func(v string) error {
					if !config.ValidEnvName(v) {
						return errors.New("use lowercase letters, numbers and dashes")
					}
					return nil
				})
			if err != nil {
				conn.Close()
				return nil, nil, nil, err
			}
			if err := config.SaveServer(s); err != nil {
				conn.Close()
				return nil, nil, nil, err
			}
			return s, conn, facts, nil
		}
		ui.Fail("Could not connect.", err.Error(), "")
		if s.Type == "local" {
			return nil, nil, nil, err
		}
		again, cerr := ui.Confirm("Try again with different details?", true, "")
		if cerr != nil || !again {
			return nil, nil, nil, ui.ErrCancelled
		}
	}
}

func defaultServerName(s *config.Server) string {
	if s.Type == "local" {
		return "this-computer"
	}
	first := strings.Split(s.Host, ".")[0]
	if net.ParseIP(s.Host) != nil || first == "" {
		return "server-1"
	}
	return config.Slug(first)
}
