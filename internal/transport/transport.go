// Package transport carries scripts and files to a machine. Two kinds exist:
// ssh for another machine, and local for this one (also what the tests use).
package transport

import (
	"context"
	"io"
	"os"
	"sort"
	"strings"
)

// Conn is a connection to a machine that runs POSIX shell scripts.
type Conn interface {
	// Run runs a bash script with env exported, streaming output to out.
	Run(ctx context.Context, script string, env map[string]string, out io.Writer) error
	// Upload unpacks a tar stream into dir (created if missing).
	Upload(ctx context.Context, dir string, tarStream io.Reader) error
	// Put writes one file with the given mode, replacing any existing one.
	Put(ctx context.Context, path string, mode os.FileMode, data []byte) error
	Describe() string
	Close() error
}

// Quote makes a string safe to place inside a POSIX shell command.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ExportHeader turns env into export lines placed in front of a script.
func ExportHeader(env map[string]string) string {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString("export " + k + "=" + Quote(env[k]) + "\n")
	}
	return sb.String()
}

// Capture runs a script and returns what it printed.
func Capture(ctx context.Context, c Conn, script string, env map[string]string) (string, error) {
	var sb strings.Builder
	err := c.Run(ctx, script, env, &sb)
	return sb.String(), err
}
