package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Status classes reported by `gitpriv status`.
const (
	StInSync    = "in-sync"
	StLocalOnly = "local-only"
	StStoreOnly = "store-only"
	StDiffers   = "differs"
	StSkipped   = "skipped"
)

// StatusEntry is one classified private file.
type StatusEntry struct {
	Rel    string
	Class  string
	Detail string
}

func cmdStatus(args []string) error { return runRepos(args, true, statusRepo) }

func statusRepo(cfg *Config, r Repo, _ Opts, w io.Writer) (int, error) {
	entries, legacy, errs, err := repoStatus(cfg, r)
	if err != nil {
		return 0, err
	}
	counts := map[string]int{}
	for _, e := range entries {
		counts[e.Class]++
	}
	fmt.Fprintf(w, "%s (%s): %d in-sync, %d local-only, %d store-only, %d differs, %d skipped\n",
		r.Key, r.Root, counts[StInSync], counts[StLocalOnly], counts[StStoreOnly], counts[StDiffers], counts[StSkipped])
	if legacy {
		fmt.Fprintf(w, "  (store entries are under the legacy key %s; the next push moves them)\n", r.LegacyKey)
	}
	for _, e := range entries {
		if e.Detail != "" {
			fmt.Fprintf(w, "  %-10s %s (%s)\n", e.Class, e.Rel, e.Detail)
		} else {
			fmt.Fprintf(w, "  %-10s %s\n", e.Class, e.Rel)
		}
	}
	printErrs(w, errs)
	return len(errs), nil
}

// repoStatus compares the local private files of r with its store copy.
func repoStatus(cfg *Config, r Repo) ([]StatusEntry, bool, []error, error) {
	rules := cfg.Rules()
	res, err := collect(r.Root, rules)
	if err != nil {
		return nil, false, nil, err
	}
	errs := res.Errs
	dir, legacy := storeDir(cfg.Store, r)
	var stored []string
	if isDir(dir) {
		var serrs []error
		stored, serrs = storeFiles(dir, rules.Skip)
		errs = append(errs, serrs...)
	}
	times := storeTimes(dir)
	inStore := map[string]bool{}
	for _, s := range stored {
		inStore[s] = true
	}

	var out []StatusEntry
	compare := func(rel string) StatusEntry {
		local := filepath.Join(r.Root, filepath.FromSlash(rel))
		remote := filepath.Join(dir, filepath.FromSlash(rel))
		eq, err := sameContent(local, remote)
		if err != nil {
			errs = append(errs, err)
			return StatusEntry{rel, StDiffers, "could not compare"}
		}
		if eq {
			return StatusEntry{rel, StInSync, ""}
		}
		return StatusEntry{rel, StDiffers, newerSide(local, remote, times[rel])}
	}
	for _, f := range res.Files {
		if inStore[f.Rel] {
			out = append(out, compare(f.Rel))
			delete(inStore, f.Rel)
		} else {
			out = append(out, StatusEntry{f.Rel, StLocalOnly, "push would add"})
		}
	}
	var missing []string
	for _, rel := range stored {
		if !inStore[rel] {
			continue // already handled above
		}
		_, err := os.Lstat(filepath.Join(r.Root, filepath.FromSlash(rel)))
		switch {
		case err == nil:
			out = append(out, compare(rel))
		case errors.Is(err, fs.ErrNotExist):
			missing = append(missing, rel)
		default:
			errs = append(errs, err)
		}
	}
	ignored, err := checkIgnored(r.Root, missing)
	if err != nil {
		return nil, legacy, errs, err
	}
	for _, rel := range missing {
		detail := "pull would restore"
		if !ignored[rel] {
			detail = "pull would refuse: not git-ignored here"
		}
		out = append(out, StatusEntry{rel, StStoreOnly, detail})
	}
	for _, s := range res.Skipped {
		out = append(out, StatusEntry{s.Rel, StSkipped, s.Reason})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Rel < out[j].Rel })
	return out, legacy, errs, nil
}

func newerSide(local, remote string, committed time.Time) string {
	li, err1 := os.Stat(local)
	st := committed
	if st.IsZero() {
		if ri, err := os.Stat(remote); err == nil {
			st = ri.ModTime()
		}
	}
	if err1 != nil || st.IsZero() {
		return "content differs"
	}
	if li.ModTime().After(st) {
		return "local is newer; push would update the store"
	}
	return "store is newer; pull keeps local, push refuses without --force"
}
