package commit

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const prompt = `Write a git commit message for the changes below.
Use the imperative mood ("Add", "Fix"). First line under 72 characters, then a blank line,
then short bullet points only if they help. Output only the message, nothing else.

`

const maxDiff = 20000

// buildInput is what a message command receives on standard input. It holds
// the changes and never the contents of files that look like keys.
func buildInput(dir string, all []File) string {
	// never trust the caller to have left keys out: drop them here too
	var files []File
	for _, f := range all {
		if !f.Skipped && !SecretLike(f.Path) {
			files = append(files, f)
		}
	}
	var sb strings.Builder
	sb.WriteString(prompt)
	sb.WriteString("Files:\n")
	for _, f := range files {
		fmt.Fprintf(&sb, "%s %s%s\n", f.Status, f.Path, counts(f))
	}
	sb.WriteString("\nChanges:\n")
	var tracked []string
	for _, f := range files {
		if f.Status != "?" {
			tracked = append(tracked, f.Path)
		}
	}
	if len(tracked) > 0 {
		out, _ := git(dir, "", append([]string{"diff", "HEAD", "--"}, tracked...)...)
		sb.WriteString(out)
	}
	for _, f := range files {
		if f.Status != "?" || sb.Len() > maxDiff {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, f.Path))
		if err != nil || bytes.IndexByte(b[:min(len(b), 8000)], 0) >= 0 {
			continue
		}
		lines := strings.SplitN(string(b), "\n", 41)
		if len(lines) > 40 {
			lines = lines[:40]
		}
		fmt.Fprintf(&sb, "\nnew file %s:\n+%s\n", f.Path, strings.Join(lines, "\n+"))
	}
	s := sb.String()
	if len(s) > maxDiff {
		s = s[:maxDiff] + "\n[cut: the diff is longer than shown]\n"
	}
	return s
}

// Generate asks an external command for a message. The command receives the
// changes on standard input and prints the message. Any failure returns an
// error so the caller can fall back to Suggest.
func Generate(dir string, files []File, command string) (string, error) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("bash", "-c", command)
	}
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(buildInput(dir, files))
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			return "", fmt.Errorf("%v: %s", err, firstLines(errb.String(), 3))
		}
	case <-time.After(90 * time.Second):
		_ = cmd.Process.Kill()
		return "", fmt.Errorf("it took longer than 90 seconds")
	}
	msg := clean(out.String())
	if msg == "" {
		return "", fmt.Errorf("it printed nothing")
	}
	return msg, nil
}

// clean removes code fences and blank edges that tools sometimes add.
func clean(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		lines := strings.Split(s, "\n")
		lines = lines[1:]
		if n := len(lines); n > 0 && strings.HasPrefix(strings.TrimSpace(lines[n-1]), "```") {
			lines = lines[:n-1]
		}
		s = strings.TrimSpace(strings.Join(lines, "\n"))
	}
	return strings.Trim(s, "\"` \n")
}
