package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// repoRulesFile is the per-repository rules file at the repository root.
// It travels with the repository (commit it), so every machine and every
// tool that asks gitpriv judges the repository the same way.
const repoRulesFile = ".gitpriv"

// RepoFile is what a .gitpriv file says.
type RepoFile struct {
	Include []string
	Skip    []string
}

// parseRepoFile reads .gitpriv lines: "skip PATTERN" (rebuildable junk,
// never synced or carried) or "include PATTERN" (private, synced), blank
// lines and "#" comments. Anything else is an error naming its line, so a
// typo never silently changes what counts as junk.
func parseRepoFile(data []byte) (RepoFile, error) {
	var f RepoFile
	sc := bufio.NewScanner(bytes.NewReader(data))
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		word, pat, _ := strings.Cut(line, " ")
		pat = strings.TrimSpace(pat)
		if pat == "" || strings.ContainsAny(pat, "\t") {
			return f, fmt.Errorf("line %d: want \"skip PATTERN\" or \"include PATTERN\", got %q", n, line)
		}
		switch word {
		case "skip":
			f.Skip = append(f.Skip, pat)
		case "include":
			f.Include = append(f.Include, pat)
		default:
			return f, fmt.Errorf("line %d: want \"skip PATTERN\" or \"include PATTERN\", got %q", n, line)
		}
	}
	return f, sc.Err()
}

// loadRepoFile reads root/.gitpriv; a missing file is no rules. A link is
// refused: the rules are the repository's own file.
func loadRepoFile(root string) (RepoFile, error) {
	p := filepath.Join(root, repoRulesFile)
	info, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return RepoFile{}, nil
	}
	if err != nil {
		return RepoFile{}, err
	}
	if !info.Mode().IsRegular() {
		return RepoFile{}, fmt.Errorf("%s is not a regular file", p)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return RepoFile{}, err
	}
	f, err := parseRepoFile(data)
	if err != nil {
		return RepoFile{}, fmt.Errorf("%s: %w", p, err)
	}
	return f, nil
}

// repoRules is base with the repository's .gitpriv added.
func repoRules(root string, base Rules) (Rules, error) {
	f, err := loadRepoFile(root)
	if err != nil {
		return base, err
	}
	base.Include = append(append([]string{}, base.Include...), f.Include...)
	base.Skip = append(append([]string{}, base.Skip...), f.Skip...)
	return base, nil
}

// cacheDirSignature starts every CACHEDIR.TAG (https://bford.info/cachedir/).
const cacheDirSignature = "Signature: 8a477f597d28d172789f06886806bc55"

// junkMarker names the marker that makes dir rebuildable whatever it is
// called: a CACHEDIR.TAG with the standard's signature (Cargo's target,
// many caches) or a pyvenv.cfg (a Python virtualenv: .venv-vision,
// venv-tf457). "" when there is none.
func junkMarker(dir string) string {
	if f, err := os.Open(filepath.Join(dir, "CACHEDIR.TAG")); err == nil {
		buf := make([]byte, len(cacheDirSignature))
		n, _ := f.Read(buf)
		f.Close()
		if string(buf[:n]) == cacheDirSignature {
			return "CACHEDIR.TAG"
		}
	}
	if info, err := os.Lstat(filepath.Join(dir, "pyvenv.cfg")); err == nil && info.Mode().IsRegular() {
		return "pyvenv.cfg"
	}
	return ""
}
