// Package archive packs a project into the tar stream that gets uploaded.
// The upload never contains settings files or git history, whatever is in the folder.
package archive

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Excluded says whether a path must never be uploaded.
func Excluded(p string) bool {
	p = strings.TrimPrefix(path.Clean(filepath.ToSlash(p)), "./")
	for _, part := range strings.Split(p, "/") {
		switch part {
		case ".git", ".shipit", "node_modules", ".DS_Store":
			return true
		}
	}
	base := path.Base(p)
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		l := strings.ToLower(base)
		for _, ok := range []string{".example", ".sample", ".template"} {
			if strings.HasSuffix(l, ok) {
				return false
			}
		}
		return true
	}
	return false
}

// Source says what to pack.
type Source struct {
	Dir string
	// Ref packs that git commit or tag. Empty packs the working folder as it is now.
	Ref string
}

// Pack writes the tar to w. extra adds files that are not in the project
// (the VERSION file, shipit's compose override).
func Pack(ctx context.Context, s Source, extra map[string][]byte, w io.Writer) error {
	tw := tar.NewWriter(w)
	var err error
	if s.Ref != "" {
		err = fromGit(ctx, s, tw)
	} else {
		err = fromFolder(s.Dir, tw)
	}
	if err != nil {
		return err
	}
	for name, data := range extra {
		hdr := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), ModTime: time.Now(), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return tw.Close()
}

func fromGit(ctx context.Context, s Source, tw *tar.Writer) error {
	cmd := exec.CommandContext(ctx, "git", "-C", s.Dir, "archive", "--format=tar", s.Ref)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not run git: %w", err)
	}
	tr := tar.NewReader(out)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = cmd.Wait()
			return fmt.Errorf("reading the git archive failed: %w (%s)", err, strings.TrimSpace(stderr.String()))
		}
		if hdr.Typeflag == tar.TypeXGlobalHeader || Excluded(hdr.Name) {
			continue
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("git could not pack %q: %s", s.Ref, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func fromFolder(dir string, tw *tar.Writer) error {
	return filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil || rel == "." {
			return err
		}
		if Excluded(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = filepath.ToSlash(rel)
		if d.IsDir() {
			hdr.Name += "/"
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			fh, err := os.Open(p)
			if err != nil {
				return err
			}
			defer fh.Close()
			if _, err := io.Copy(tw, fh); err != nil {
				return err
			}
		}
		return nil
	})
}
