// Package config reads and writes shipit's settings files.
//
// The format is deliberately plain and is parsed, never executed:
//
//	# comment
//	key = value
//	[section]
//	key = value
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Entry is one key = value line.
type Entry struct{ Key, Value string }

// Block is a section. The unnamed block (Name == "") holds the top-level keys.
type Block struct {
	Name    string
	Entries []Entry
}

// File is a parsed settings file that keeps the order of what was written.
type File struct{ Blocks []*Block }

func newFile() *File { return &File{Blocks: []*Block{{Name: ""}}} }

// Parse reads a settings file. Errors name the line so people can fix them.
func Parse(r io.Reader, name string) (*File, error) {
	f := newFile()
	cur := f.Blocks[0]
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") {
				return nil, fmt.Errorf("%s line %d: a section must look like [name]", name, n)
			}
			cur = &Block{Name: strings.TrimSpace(line[1 : len(line)-1])}
			f.Blocks = append(f.Blocks, cur)
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("%s line %d: expected key = value, got %q", name, n, line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return nil, fmt.Errorf("%s line %d: the key before = is empty", name, n)
		}
		v = stripInlineComment(v)
		cur.Entries = append(cur.Entries, Entry{k, v})
	}
	return f, sc.Err()
}

// stripInlineComment removes "  # note" from the end of an unquoted value and
// removes one pair of surrounding quotes. A quoted value may contain '#'.
func stripInlineComment(v string) string {
	if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') {
		if end := strings.IndexByte(v[1:], v[0]); end >= 0 {
			return v[1 : 1+end]
		}
		return v[1:]
	}
	if i := strings.Index(v, " #"); i >= 0 {
		v = v[:i]
	}
	return strings.TrimSpace(v)
}

// ParseFile reads a settings file from disk.
func ParseFile(path string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	return Parse(fh, path)
}

// Block returns the named section, or nil.
func (f *File) Block(name string) *Block {
	for _, b := range f.Blocks {
		if b.Name == name {
			return b
		}
	}
	return nil
}

// Get returns a value from a section ("" is the top level).
func (f *File) Get(section, key string) (string, bool) {
	b := f.Block(section)
	if b == nil {
		return "", false
	}
	for _, e := range b.Entries {
		if e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

// Set adds or replaces a value, creating the section when needed.
func (f *File) Set(section, key, value string) {
	b := f.Block(section)
	if b == nil {
		b = &Block{Name: section}
		f.Blocks = append(f.Blocks, b)
	}
	for i := range b.Entries {
		if b.Entries[i].Key == key {
			b.Entries[i].Value = value
			return
		}
	}
	b.Entries = append(b.Entries, Entry{key, value})
}

// Sections lists the named sections in file order.
func (f *File) Sections() []string {
	var out []string
	for _, b := range f.Blocks {
		if b.Name != "" {
			out = append(out, b.Name)
		}
	}
	return out
}

// Render writes the file back out. header lines are written first as comments.
func (f *File) Render(header ...string) string {
	var sb strings.Builder
	for _, h := range header {
		sb.WriteString("# " + h + "\n")
	}
	for i, b := range f.Blocks {
		if b.Name == "" && len(b.Entries) == 0 {
			continue
		}
		if i > 0 || len(header) > 0 {
			sb.WriteString("\n")
		}
		if b.Name != "" {
			sb.WriteString("[" + b.Name + "]\n")
		}
		for _, e := range b.Entries {
			sb.WriteString(e.Key + " = " + quoteIfNeeded(e.Value) + "\n")
		}
	}
	return sb.String()
}

func quoteIfNeeded(v string) string {
	if strings.Contains(v, " #") || strings.HasPrefix(v, "#") || strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
		return "\"" + v + "\""
	}
	return v
}

// WriteFile saves the file, creating the folder if needed.
func (f *File) WriteFile(path string, perm os.FileMode, header ...string) error {
	if err := os.MkdirAll(dirOf(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(f.Render(header...)), perm)
}

func dirOf(p string) string {
	i := strings.LastIndexAny(p, "/\\")
	if i < 0 {
		return "."
	}
	return p[:i]
}
