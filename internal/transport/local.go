package transport

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
)

// Local runs everything on this computer.
type Local struct{}

func (Local) Describe() string { return "this computer" }
func (Local) Close() error     { return nil }

func (Local) Run(ctx context.Context, script string, env map[string]string, out io.Writer) error {
	f, err := os.CreateTemp("", "shipit-*.sh")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(ExportHeader(env) + script); err != nil {
		return err
	}
	f.Close()
	cmd := exec.CommandContext(ctx, "bash", f.Name())
	cmd.Stdout, cmd.Stderr = out, out
	cmd.Stdin = nil
	return cmd.Run()
}

func (Local) Upload(ctx context.Context, dir string, tarStream io.Reader) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "tar", "-xf", "-", "-C", dir)
	cmd.Stdin = tarStream
	if b, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("unpacking the upload failed: %v: %s", err, b)
	}
	return nil
}

func (Local) Put(ctx context.Context, path string, mode os.FileMode, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".shipit-tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Chmod(tmp, mode); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
