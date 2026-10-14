package main

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// File is a private file selected for syncing (path relative to repo root).
type File struct {
	Rel  string
	Size int64
}

// Skipped is a candidate that matched an include pattern but is not synced.
type Skipped struct {
	Rel    string
	Reason string
}

// ScanResult is everything collect learned about one repo.
type ScanResult struct {
	Files   []File
	Skipped []Skipped
	Other   int     // ignored files/dirs that are not private state (junk)
	Errs    []error // walk/stat errors; the command must exit non-zero
}

// normPattern turns a user pattern into the canonical slash-glob form:
//   - a pattern without "/" matches at any depth ("*.pem" -> "**/*.pem")
//   - a trailing "/" means "everything below this dir" ("out/" -> "**/out/**")
//   - a leading "/" anchors at the repo root ("/notes.md" -> "notes.md")
func normPattern(p string) string {
	dir := strings.HasSuffix(p, "/")
	p = strings.TrimSuffix(p, "/")
	if strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(p, "/")
	} else if !strings.Contains(p, "/") {
		p = "**/" + p
	}
	if dir {
		p += "/**"
	}
	return p
}

// matchGlob reports whether the slash-separated rel path matches pat.
// "*", "?" and "[...]" match within one path segment; "**" matches any
// number of segments (including zero).
func matchGlob(rel, pat string) bool {
	return matchSeq(strings.Split(rel, "/"), strings.Split(normPattern(pat), "/"))
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

func matchAny(rel string, pats []string) bool {
	for _, p := range pats {
		if matchGlob(rel, p) {
			return true
		}
	}
	return false
}

// dirSkipped reports whether a whole directory can be pruned: some skip
// pattern of the form "<x>/**" matches it.
func dirSkipped(rel string, skip []string) bool {
	for _, p := range skip {
		if np := normPattern(p); strings.HasSuffix(np, "/**") && matchGlob(rel, np) {
			return true
		}
	}
	return false
}

// ignoredEntries lists git-ignored paths. Fully ignored directories are
// returned once with a trailing "/" so junk like node_modules can be pruned
// without listing every file in it.
func ignoredEntries(repoDir string) ([]string, error) {
	cmd := exec.Command("git", "-C", repoDir, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git ls-files in %s: %v: %s", repoDir, err, strings.TrimSpace(stderr.String()))
	}
	var entries []string
	for _, e := range bytes.Split(out, []byte{0}) {
		if len(e) > 0 {
			entries = append(entries, string(e))
		}
	}
	return entries, nil
}

// collect selects the private files of repoDir (which must be the repo's
// top-level directory): git-ignored AND included AND not skipped AND a
// regular file (symlinks are never followed) no larger than the size cap.
func collect(repoDir string, rules Rules) (*ScanResult, error) {
	entries, err := ignoredEntries(repoDir)
	if err != nil {
		return nil, err
	}
	res := &ScanResult{}
	consider := func(rel string, info fs.FileInfo) {
		if !matchAny(rel, rules.Include) {
			res.Other++
			return
		}
		if matchAny(rel, rules.Skip) {
			res.Skipped = append(res.Skipped, Skipped{rel, "matches a skip pattern"})
			return
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			res.Skipped = append(res.Skipped, Skipped{rel, "symlink (not followed)"})
		case !info.Mode().IsRegular():
			res.Skipped = append(res.Skipped, Skipped{rel, "not a regular file"})
		case info.Size() > rules.MaxFileBytes:
			res.Skipped = append(res.Skipped, Skipped{rel, fmt.Sprintf("too large (%s > %s cap)", human(info.Size()), human(rules.MaxFileBytes))})
		default:
			res.Files = append(res.Files, File{rel, info.Size()})
		}
	}
	for _, e := range entries {
		rel := strings.TrimSuffix(e, "/")
		full := filepath.Join(repoDir, filepath.FromSlash(rel))
		if !strings.HasSuffix(e, "/") {
			info, err := os.Lstat(full)
			if err != nil {
				res.Errs = append(res.Errs, err)
				continue
			}
			consider(rel, info)
			continue
		}
		if dirSkipped(rel, rules.Skip) {
			res.Other++
			continue
		}
		err := filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				res.Errs = append(res.Errs, err)
				if d != nil && d.IsDir() && p != full {
					return fs.SkipDir
				}
				return nil
			}
			r, err := filepath.Rel(repoDir, p)
			if err != nil {
				res.Errs = append(res.Errs, err)
				return nil
			}
			r = filepath.ToSlash(r)
			if d.IsDir() {
				if p != full && dirSkipped(r, rules.Skip) {
					res.Other++
					return fs.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				res.Errs = append(res.Errs, err)
				return nil
			}
			consider(r, info)
			return nil
		})
		if err != nil {
			res.Errs = append(res.Errs, err)
		}
	}
	sort.Slice(res.Files, func(i, j int) bool { return res.Files[i].Rel < res.Files[j].Rel })
	sort.Slice(res.Skipped, func(i, j int) bool { return res.Skipped[i].Rel < res.Skipped[j].Rel })
	return res, nil
}

func human(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KiB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}
