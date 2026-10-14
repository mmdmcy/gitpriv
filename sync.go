package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

// Opts are the per-command flags.
type Opts struct {
	DryRun bool // --dry-run / -n: print what would change, change nothing
	Force  bool // --force (push only): overwrite store copies that are newer
}

// parseFlags splits flags from repo arguments. allowed lists the flags the
// command accepts ("dry-run", "force").
func parseFlags(args []string, allowed ...string) (Opts, []string, error) {
	ok := map[string]bool{}
	for _, a := range allowed {
		ok[a] = true
	}
	var o Opts
	var rest []string
	for _, a := range args {
		switch {
		case ok["dry-run"] && (a == "--dry-run" || a == "-n"):
			o.DryRun = true
		case ok["force"] && a == "--force":
			o.Force = true
		case a == "--":
			continue
		case strings.HasPrefix(a, "-"):
			return o, nil, fmt.Errorf("unknown flag %q", a)
		default:
			rest = append(rest, a)
		}
	}
	return o, rest, nil
}

// runRepos loads config, resolves repos and runs fn on each, turning the
// number of reported problems into a non-zero exit.
func runRepos(args []string, needStore bool, fn func(*Config, Repo, Opts, io.Writer) (int, error), flags ...string) error {
	opts, rest, err := parseFlags(args, flags...)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(needStore)
	if err != nil {
		return err
	}
	repos, err := resolveRepos(rest)
	if err != nil {
		return err
	}
	problems := 0
	for _, r := range repos {
		n, err := fn(cfg, r, opts, os.Stdout)
		if err != nil {
			return fmt.Errorf("%s: %w", r.Key, err)
		}
		problems += n
	}
	if problems > 0 {
		return fmt.Errorf("%d problem(s) reported above", problems)
	}
	return nil
}

func cmdScan(args []string) error { return runRepos(args, false, scanRepo) }
func cmdPush(args []string) error { return runRepos(args, true, pushRepo, "dry-run", "force") }
func cmdPull(args []string) error { return runRepos(args, true, pullRepo, "dry-run") }

func scanRepo(cfg *Config, r Repo, _ Opts, w io.Writer) (int, error) {
	res, err := collect(r.Root, cfg.Rules())
	if err != nil {
		return 0, err
	}
	var total int64
	for _, f := range res.Files {
		total += f.Size
	}
	fmt.Fprintf(w, "%s (%s): %d file(s) to sync (%s), %d skipped, %d other ignored entries\n",
		r.Key, r.Root, len(res.Files), human(total), len(res.Skipped), res.Other)
	for _, f := range res.Files {
		fmt.Fprintf(w, "  sync     %s (%s)\n", f.Rel, human(f.Size))
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(w, "  skipped  %s: %s\n", s.Rel, s.Reason)
	}
	printErrs(w, res.Errs)
	return len(res.Errs), nil
}

func printErrs(w io.Writer, errs []error) {
	for _, e := range errs {
		fmt.Fprintf(w, "  error    %v\n", e)
	}
}

func pushRepo(cfg *Config, r Repo, o Opts, w io.Writer) (int, error) {
	dry := o.DryRun
	res, err := collect(r.Root, cfg.Rules())
	if err != nil {
		return 0, err
	}
	if len(res.Errs) > 0 {
		fmt.Fprintf(w, "%s: not pushing, scan had errors:\n", r.Key)
		printErrs(w, res.Errs)
		return len(res.Errs), nil
	}
	would := ""
	if dry {
		would = "would "
	}
	dir, legacy := storeDir(cfg.Store, r)
	newDir := filepath.Join(cfg.Store, filepath.FromSlash(r.Key))
	paths := []string{r.Key}
	if legacy {
		fmt.Fprintf(w, "%s: %smove legacy store dir %s -> %s\n", r.Key, would, r.LegacyKey, r.Key)
		paths = append(paths, r.LegacyKey)
		if !dry {
			if err := os.MkdirAll(filepath.Dir(newDir), 0o700); err != nil {
				return 0, err
			}
			if err := os.Rename(dir, newDir); err != nil {
				return 0, err
			}
			dir = newDir
		}
	}
	times := storeTimes(dir)
	var added, updated, same, conflicts int
	for _, f := range res.Files {
		src := filepath.Join(r.Root, filepath.FromSlash(f.Rel))
		dst := filepath.Join(dir, filepath.FromSlash(f.Rel))
		action := "add"
		if _, err := os.Lstat(dst); err == nil {
			eq, err := sameContent(src, dst)
			if err != nil {
				return 0, err
			}
			if eq {
				same++
				continue
			}
			action = "update"
			if st, ok := times[f.Rel]; ok && !o.Force {
				if info, err := os.Stat(src); err == nil && st.After(info.ModTime()) {
					conflicts++
					fmt.Fprintf(w, "  conflict %s: store copy is newer than the local file (pull/status first, or push --force)\n", f.Rel)
					continue
				}
			}
		}
		fmt.Fprintf(w, "  %s%-6s %s\n", would, action, f.Rel)
		if action == "add" {
			added++
		} else {
			updated++
		}
		if !dry {
			if err := copyFile(src, filepath.Join(newDir, filepath.FromSlash(f.Rel))); err != nil {
				return 0, fmt.Errorf("%s: %w", f.Rel, err)
			}
		}
	}
	fmt.Fprintf(w, "%s: %s%d added, %d updated, %d unchanged", r.Key, would, added, updated, same)
	if conflicts > 0 {
		fmt.Fprintf(w, ", %d conflict(s) not pushed", conflicts)
	}
	if len(res.Skipped) > 0 {
		fmt.Fprintf(w, ", %d skipped (see gitpriv scan)", len(res.Skipped))
	}
	fmt.Fprintln(w)
	if dry || (added+updated == 0 && !legacy) {
		return conflicts, nil
	}
	msg := fmt.Sprintf("gitpriv: sync %s (%d added, %d updated) %s", r.Key, added, updated, time.Now().Format(time.RFC3339))
	return conflicts, gitCommit(cfg.Store, msg, paths)
}

func pullRepo(cfg *Config, r Repo, o Opts, w io.Writer) (int, error) {
	dry := o.DryRun
	dir, legacy := storeDir(cfg.Store, r)
	if !isDir(dir) {
		fmt.Fprintf(w, "%s: nothing in store\n", r.Key)
		return 0, nil
	}
	if legacy {
		fmt.Fprintf(w, "%s: reading legacy store dir %s\n", r.Key, r.LegacyKey)
	}
	would := ""
	if dry {
		would = "would "
	}
	files, errs := storeFiles(dir, cfg.Rules().Skip)
	problems := len(errs)
	printErrs(w, errs)

	var missing []string
	var present, differs int
	for _, rel := range files {
		dst := filepath.Join(r.Root, filepath.FromSlash(rel))
		if _, err := os.Lstat(dst); err == nil {
			eq, err := sameContent(filepath.Join(dir, filepath.FromSlash(rel)), dst)
			if err != nil || !eq {
				differs++
				fmt.Fprintf(w, "  differs  %s (local copy kept)\n", rel)
				continue
			}
			present++
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return problems, err
		}
		missing = append(missing, rel)
	}
	ignored, err := checkIgnored(r.Root, missing)
	if err != nil {
		return problems, err
	}
	var restored, refused int
	for _, rel := range missing {
		if !ignored[rel] {
			refused++
			fmt.Fprintf(w, "  refused  %s: not git-ignored in this repo, restoring it could get it committed\n", rel)
			continue
		}
		if err := safeParent(r.Root, rel); err != nil {
			refused++
			fmt.Fprintf(w, "  refused  %s: %v\n", rel, err)
			continue
		}
		fmt.Fprintf(w, "  %srestore %s\n", would, rel)
		restored++
		if !dry {
			if err := copyFileNoClobber(filepath.Join(dir, filepath.FromSlash(rel)), filepath.Join(r.Root, filepath.FromSlash(rel))); err != nil {
				return problems, fmt.Errorf("%s: %w", rel, err)
			}
		}
	}
	fmt.Fprintf(w, "%s: %srestore %d, %d already present, %d differ (kept), %d refused\n",
		r.Key, would, restored, present, differs, refused)
	return problems + refused, nil
}

// storeFiles lists regular files under dir (slash paths relative to dir),
// minus anything matching skip. Symlinks in the store are never followed.
func storeFiles(dir string, skip []string) ([]string, []error) {
	var files []string
	var errs []error
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			errs = append(errs, err)
			if d != nil && d.IsDir() && p != dir {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			errs = append(errs, err)
			return nil
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if p != dir && (d.Name() == ".git" || dirSkipped(rel, skip)) {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || matchAny(rel, skip) || !filepath.IsLocal(rel) {
			return nil
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		errs = append(errs, err)
	}
	sort.Strings(files)
	return files, errs
}

// checkIgnored asks git which of rels are ignored in repo (tracked paths
// are never reported as ignored).
func checkIgnored(root string, rels []string) (map[string]bool, error) {
	res := map[string]bool{}
	if len(rels) == 0 {
		return res, nil
	}
	cmd := exec.Command("git", "-C", root, "check-ignore", "-z", "--stdin")
	cmd.Stdin = strings.NewReader(strings.Join(rels, "\x00") + "\x00")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	var ee *exec.ExitError
	if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
		return nil, fmt.Errorf("git check-ignore: %v: %s", err, strings.TrimSpace(stderr.String()))
	}
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) > 0 {
			res[string(p)] = true
		}
	}
	return res, nil
}

// safeParent refuses to write through a symlinked directory inside the repo.
func safeParent(root, rel string) error {
	cur := root
	parts := strings.Split(rel, "/")
	for _, part := range parts[:len(parts)-1] {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("parent %s is not a plain directory", cur)
		}
	}
	return nil
}

func hashFile(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sameContent(a, b string) (bool, error) {
	ia, err := os.Lstat(a)
	if err != nil {
		return false, err
	}
	ib, err := os.Lstat(b)
	if err != nil {
		return false, err
	}
	if !ia.Mode().IsRegular() || !ib.Mode().IsRegular() {
		return false, nil
	}
	if ia.Size() != ib.Size() {
		return false, nil
	}
	ha, err := hashFile(a)
	if err != nil {
		return false, err
	}
	hb, err := hashFile(b)
	if err != nil {
		return false, err
	}
	return ha == hb, nil
}

// gitCommit stages and commits only the given store paths, so unrelated
// changes in the store are never swept into a gitpriv commit.
func gitCommit(store, msg string, paths []string) error {
	add := append([]string{"-C", store, "add", "-A", "--"}, paths...)
	if out, err := exec.Command("git", add...).CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %s", strings.TrimSpace(string(out)))
	}
	commit := append([]string{"-C", store, "commit", "-q", "-m", msg, "--"}, paths...)
	if out, err := exec.Command("git", commit...).CombinedOutput(); err != nil {
		if strings.Contains(string(out), "nothing to commit") || strings.Contains(string(out), "no changes added") {
			return nil
		}
		return fmt.Errorf("git commit: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// copyFile writes src to dst atomically with mode 0600 and src's mtime,
// replacing dst if it exists.
func copyFile(src, dst string) error { return writeCopy(src, dst, false) }

// copyFileNoClobber is copyFile but fails instead of replacing dst.
func copyFileNoClobber(src, dst string) error { return writeCopy(src, dst, true) }

func writeCopy(src, dst string, noClobber bool) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".gitpriv-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chtimes(tmp.Name(), info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if noClobber {
		// link(2) fails if dst exists, so a file created meanwhile is kept.
		return os.Link(tmp.Name(), dst)
	}
	return os.Rename(tmp.Name(), dst)
}

// storeTimes maps files under dir (slash paths relative to dir) to the time
// of the last store commit that touched them. Uncommitted files are absent.
func storeTimes(dir string) map[string]time.Time {
	times := map[string]time.Time{}
	if !isDir(dir) {
		return times
	}
	out, err := gitOutput(dir, "-c", "core.quotePath=false", "log", "--relative", "--format=@%ct", "--name-only", "--", ".")
	if err != nil {
		return times
	}
	var cur time.Time
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "@"):
			var sec int64
			fmt.Sscan(line[1:], &sec)
			cur = time.Unix(sec, 0)
		default:
			if _, seen := times[line]; !seen {
				times[line] = cur
			}
		}
	}
	return times
}
