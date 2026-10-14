package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func gitOutput(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return string(out), err
}

func cmdScan(args []string) error {
	cfg, err := loadConfig(false)
	if err != nil {
		return err
	}
	repos, err := resolveRepos(args)
	if err != nil {
		return err
	}
	problems := 0
	for _, repo := range repos {
		res, err := collect(repo, cfg.Rules())
		if err != nil {
			return err
		}
		printScan(os.Stdout, repoKey(repo), res)
		problems += len(res.Errs)
	}
	if problems > 0 {
		return fmt.Errorf("%d error(s) while scanning", problems)
	}
	return nil
}

func cmdPush(args []string) error {
	cfg, err := loadConfig(true)
	if err != nil {
		return err
	}
	repos, err := resolveRepos(args)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		res, err := collect(repo, cfg.Rules())
		if err != nil {
			return err
		}
		if len(res.Errs) > 0 {
			for _, e := range res.Errs {
				fmt.Fprintln(os.Stderr, "gitpriv:", e)
			}
			return fmt.Errorf("refusing to push %s: scan had errors", repo)
		}
		var files []string
		for _, f := range res.Files {
			files = append(files, f.Rel)
		}
		name := repoKey(repo)
		if len(files) == 0 {
			fmt.Printf("%s: nothing to sync\n", name)
			continue
		}
		fmt.Printf("%s: %d file(s)\n", name, len(files))
		staging := filepath.Join(cfg.Store, name)
		for _, f := range files {
			src := filepath.Join(repo, filepath.FromSlash(f))
			dst := filepath.Join(staging, filepath.FromSlash(f))
			if err := copyFile(src, dst); err != nil {
				return fmt.Errorf("%s: %w", f, err)
			}
		}
		msg := fmt.Sprintf("gitpriv: sync %s (%d files) %s", name, len(files), time.Now().Format(time.RFC3339))
		if err := gitCommit(cfg.Store, msg); err != nil {
			return err
		}
		fmt.Printf("%s: pushed %d file(s) to store\n", name, len(files))
	}
	return nil
}

func cmdPull(args []string) error {
	cfg, err := loadConfig(true)
	if err != nil {
		return err
	}
	repos, err := resolveRepos(args)
	if err != nil {
		return err
	}
	for _, repo := range repos {
		name := repoKey(repo)
		stage := filepath.Join(cfg.Store, name)
		info, err := os.Stat(stage)
		if err != nil || !info.IsDir() {
			fmt.Printf("%s: nothing in store\n", name)
			continue
		}
		var restored, skipped int
		filepath.WalkDir(stage, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(stage, p)
			if err != nil {
				return nil
			}
			dst := filepath.Join(repo, rel)
			if _, err := os.Stat(dst); err == nil {
				skipped++
				return nil
			}
			if err := copyFile(p, dst); err != nil {
				return err
			}
			restored++
			fmt.Printf("  %s\n", rel)
			return nil
		})
		fmt.Printf("%s: restored %d, skipped %d (already present)\n", name, restored, skipped)
	}
	return nil
}

func resolveRepos(args []string) ([]string, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	var repos []string
	for _, a := range args {
		abs, err := filepath.Abs(a)
		if err != nil {
			return nil, err
		}
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
		repos = append(repos, abs)
	}
	return repos, nil
}

func gitCommit(store, msg string) error {
	for _, c := range []string{"add", "commit"} {
		args := []string{"-C", store, c}
		if c == "commit" {
			args = append(args, "-m", msg)
		} else {
			args = append(args, "-A")
		}
		out, err := exec.Command("git", args...).CombinedOutput()
		if err != nil {
			if c == "commit" && strings.Contains(string(out), "nothing to commit") {
				return nil
			}
			return fmt.Errorf("git %s: %s", c, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

func repoKey(repoDir string) string {
	out, err := exec.Command("git", "-C", repoDir, "config", "--get", "remote.origin.url").Output()
	if err == nil {
		u := strings.TrimSpace(string(out))
		for _, p := range []string{"https://", "http://", "ssh://", "git@", "file://"} {
			u = strings.TrimPrefix(u, p)
		}
		u = strings.TrimSuffix(u, ".git")
		u = strings.NewReplacer(":", "-", "/", "-", "@", "-").Replace(u)
		if u != "" {
			return u
		}
	}
	return filepath.Base(repoDir)
}

func copyFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}

func printScan(w io.Writer, name string, res *ScanResult) {
	var total int64
	for _, f := range res.Files {
		total += f.Size
	}
	fmt.Fprintf(w, "%s: %d file(s) to sync (%s), %d skipped, %d other ignored entries\n",
		name, len(res.Files), human(total), len(res.Skipped), res.Other)
	for _, f := range res.Files {
		fmt.Fprintf(w, "  sync  %s (%s)\n", f.Rel, human(f.Size))
	}
	for _, s := range res.Skipped {
		fmt.Fprintf(w, "  skip  %s: %s\n", s.Rel, s.Reason)
	}
	for _, e := range res.Errs {
		fmt.Fprintf(w, "  error %v\n", e)
	}
}
