package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
)

func ignoredEntries(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "-io", "--exclude-standard", "-z")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %w", repoDir, err)
	}
	var entries []string
	for _, e := range bytes.Split(out, []byte{0}) {
		if len(e) > 0 {
			entries = append(entries, string(e))
		}
	}
	return entries, nil
}

func matchGlob(rel, pat string) bool {
	p := strings.Split(pat, "/")
	r := strings.Split(rel, "/")
	if p[0] == "**" {
		for i := 0; i <= len(r); i++ {
			if matchSeq(r[i:], p[1:]) {
				return true
			}
		}
		return false
	}
	return matchSeq(r, p)
}

func matchSeq(r, p []string) bool {
	for len(p) > 0 {
		if p[0] == "**" {
			p = p[1:]
			if len(p) == 0 {
				return true
			}
			for i := 0; i <= len(r); i++ {
				if matchSeq(r[i:], p) {
					return true
				}
			}
			return false
		}
		if len(r) == 0 {
			return false
		}
		ok, err := path.Match(p[0], r[0])
		if err != nil || !ok {
			return false
		}
		r, p = r[1:], p[1:]
	}
	return len(r) == 0
}

func isSkipped(rel string, skip []string) bool {
	for _, pat := range skip {
		if matchGlob(rel, pat) {
			return true
		}
	}
	return false
}

func collect(repoDir string, skip []string) ([]string, error) {	entries, err := ignoredEntries(repoDir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		rel := strings.TrimSuffix(e, "/")
		full := filepath.Join(repoDir, filepath.FromSlash(rel))
		info, err := os.Stat(full)
		if err != nil {
			continue
		}
		if info.IsDir() {
			filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
				if err != nil || d.IsDir() {
					return nil
				}
				r, err := filepath.Rel(repoDir, p)
				if err != nil {
					return nil
				}
				if !isSkipped(filepath.ToSlash(r), skip) {
					files = append(files, filepath.ToSlash(r))
				}
				return nil
			})
			continue
		}
		if !isSkipped(rel, skip) {
			files = append(files, rel)
		}
	}
	return files, nil
}
