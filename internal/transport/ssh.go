package transport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
	"golang.org/x/term"
)

// SSHOptions says how to reach a machine.
type SSHOptions struct {
	Host, User, KeyPath string
	Port                int
	KnownHosts          string
	// TrustNewHost is asked the first time a machine is seen. It gets the
	// machine address and the fingerprint and returns true to trust it. When
	// nil, an unknown machine is refused.
	TrustNewHost func(host, fingerprint string) bool
	// Passphrase is asked when a key file is protected.
	Passphrase func(keyPath string) (string, error)
}

// ErrUnknownHost means the machine has never been trusted and nobody can be asked.
var ErrUnknownHost = errors.New("this server is not trusted yet")

// SSH is a connection over ssh.
type SSH struct {
	cl   *ssh.Client
	desc string
}

// ExpandHome turns a leading ~ into the home folder.
func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, p[1:])
		}
	}
	return p
}

// DialSSH connects and checks the machine's identity.
func DialSSH(o SSHOptions) (*SSH, error) {
	if o.Port == 0 {
		o.Port = 22
	}
	var methods []ssh.AuthMethod
	if o.KeyPath != "" {
		signer, err := loadKey(ExpandHome(o.KeyPath), o.Passphrase)
		if err != nil {
			return nil, err
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if sock := os.Getenv("SSH_AUTH_SOCK"); sock != "" {
		if c, err := net.Dial("unix", sock); err == nil {
			methods = append(methods, ssh.PublicKeysCallback(agent.NewClient(c).Signers))
		}
	}
	if len(methods) == 0 {
		return nil, errors.New("no key to log in with. Give a key file for this server")
	}
	cb, err := hostKeyCallback(o)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ClientConfig{
		User: o.User, Auth: methods, HostKeyCallback: cb, Timeout: 15 * time.Second,
	}
	addr := net.JoinHostPort(o.Host, strconv.Itoa(o.Port))
	cl, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return nil, explainDial(err, o)
	}
	return &SSH{cl: cl, desc: o.User + "@" + addr}, nil
}

func explainDial(err error, o SSHOptions) error {
	msg := err.Error()
	switch {
	case errors.Is(err, ErrUnknownHost):
		return err
	case strings.Contains(msg, "unable to authenticate"):
		return fmt.Errorf("the server refused the key (%s). Add the key's public half to ~/.ssh/authorized_keys for %s on the server, or pick another key", o.KeyPath, o.User)
	case strings.Contains(msg, "REMOTE HOST IDENTIFICATION") || strings.Contains(msg, "key mismatch"):
		return err
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "no route"):
		return fmt.Errorf("could not reach %s on port %d. Check the address, and that port %d is open in the server's firewall", o.Host, o.Port, o.Port)
	case strings.Contains(msg, "connection refused"):
		return fmt.Errorf("%s refused the connection on port %d. Check that the SSH service is running there", o.Host, o.Port)
	}
	return fmt.Errorf("could not connect to %s: %w", o.Host, err)
}

func loadKey(path string, ask func(string) (string, error)) (ssh.Signer, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read the key file %s: %w", path, err)
	}
	signer, err := ssh.ParsePrivateKey(b)
	var pm *ssh.PassphraseMissingError
	if errors.As(err, &pm) {
		if ask == nil {
			return nil, fmt.Errorf("the key %s is protected by a passphrase, and nobody can be asked for it. Load it into ssh-agent first", path)
		}
		pass, aerr := ask(path)
		if aerr != nil {
			return nil, aerr
		}
		return ssh.ParsePrivateKeyWithPassphrase(b, []byte(pass))
	}
	if err != nil {
		return nil, fmt.Errorf("the key file %s is not a usable private key: %w", path, err)
	}
	return signer, nil
}

func hostKeyCallback(o SSHOptions) (ssh.HostKeyCallback, error) {
	path := o.KnownHosts
	if path == "" {
		return nil, errors.New("internal: known hosts path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			return nil, err
		}
	}
	kh, err := knownhosts.New(path)
	if err != nil {
		return nil, err
	}
	return func(host string, remote net.Addr, key ssh.PublicKey) error {
		err := kh(host, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		if len(ke.Want) > 0 {
			return fmt.Errorf("the identity of %s has CHANGED since you last connected. This can mean someone is intercepting the connection, or that the server was reinstalled. If you are sure it was reinstalled, remove the old entry for it from %s and connect again", host, path)
		}
		fp := ssh.FingerprintSHA256(key)
		if o.TrustNewHost == nil || !o.TrustNewHost(host, fp) {
			return ErrUnknownHost
		}
		line := knownhosts.Line([]string{host}, key)
		fh, ferr := os.OpenFile(path, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0o600)
		if ferr != nil {
			return ferr
		}
		defer fh.Close()
		_, ferr = fh.WriteString(line + "\n")
		return ferr
	}, nil
}

func (s *SSH) Describe() string { return s.desc }
func (s *SSH) Close() error     { return s.cl.Close() }

// runner sends the script as a file and runs it, so commands inside the script
// can never swallow the script by reading from stdin.
const runner = `f=$(mktemp) && cat > "$f" && bash "$f" </dev/null; rc=$?; rm -f "$f"; exit $rc`

func (s *SSH) session(ctx context.Context) (*ssh.Session, func(), error) {
	sess, err := s.cl.NewSession()
	if err != nil {
		return nil, nil, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			sess.Close()
		case <-done:
		}
	}()
	return sess, func() { close(done); sess.Close() }, nil
}

func (s *SSH) Run(ctx context.Context, script string, env map[string]string, out io.Writer) error {
	sess, closeFn, err := s.session(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	sess.Stdin = strings.NewReader(ExportHeader(env) + script)
	sess.Stdout, sess.Stderr = out, out
	return sess.Run(runner)
}

func (s *SSH) Upload(ctx context.Context, dir string, tarStream io.Reader) error {
	sess, closeFn, err := s.session(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	var stderr bytes.Buffer
	sess.Stdin = tarStream
	sess.Stderr = &stderr
	cmd := "mkdir -p " + Quote(dir) + " && tar -xf - -C " + Quote(dir)
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("unpacking the upload on the server failed: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (s *SSH) Put(ctx context.Context, path string, mode os.FileMode, data []byte) error {
	sess, closeFn, err := s.session(ctx)
	if err != nil {
		return err
	}
	defer closeFn()
	var stderr bytes.Buffer
	sess.Stdin = bytes.NewReader(data)
	sess.Stderr = &stderr
	tmp := path + ".shipit-tmp"
	cmd := fmt.Sprintf("umask 077; mkdir -p %s && cat > %s && chmod %o %s && mv -f %s %s",
		Quote(filepath.Dir(path)), Quote(tmp), mode.Perm(), Quote(tmp), Quote(tmp), Quote(path))
	if err := sess.Run(cmd); err != nil {
		return fmt.Errorf("writing %s on the server failed: %v: %s", path, err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// AskPassphrase reads a key passphrase from the terminal.
func AskPassphrase(keyPath string) (string, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("the key is protected by a passphrase and there is no terminal to ask in")
	}
	fmt.Fprintf(os.Stderr, "  Passphrase for %s: ", keyPath)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
