package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// classifySchema versions the JSON of `gitpriv classify --json`.
const classifySchema = "gitpriv.classify.v1"

// Classified is what gitpriv knows about everything under one path: the
// rebuildable junk (each at its highest folder, with the rule that made it
// junk) and the private files. Whatever is in neither list is unknown, and
// whoever asked carries it.
type Classified struct {
	SchemaVersion string   `json:"schema_version"`
	Path          string   `json:"path"`
	Repositories  []string `json:"repositories"`
	Junk          []Judged `json:"junk"`
	Private       []Judged `json:"private"`
	Errors        []string `json:"errors"`
}

// Judged is one entry, relative to Classified.Path ("." for the path
// itself), slash-separated.
type Judged struct {
	Path string `json:"path"`
	Dir  bool   `json:"dir"`
	// Rule is the pattern or marker file that decided it.
	Rule string `json:"rule"`
	// Source is "default", "config", ".gitpriv" or "marker".
	Source string `json:"source"`
}

// A path's standing in its repository. Junk is judged on entries git does
// not track only: a tracked file is never junk.
type standing int

const (
	tracked   standing = iota // in git, or holding something that is
	untracked                 // untracked and not ignored: git has no copy
	ignored                   // git-ignored
	outside                   // in no repository
)

type rule struct{ pat, source string }

// repoCtx is one repository's ignored and untracked entries (repository-
// relative, without a trailing "/"), as git lists them.
type repoCtx struct {
	root      string
	ignored   map[string]bool
	untracked map[string]bool
	include   []rule
	skip      []rule // judges ignored entries
	broken    bool   // git could not list it: nothing in it is junk
}

type classifier struct {
	start    string
	cfg      *Config
	out      *Classified
	anywhere []rule // judges untracked entries and entries in no repository
	include  []rule // outside repositories
}

func classifyRules(cfg *Config) (anywhere, include []rule) {
	for _, p := range defaultSkip {
		if p != "**/.git/**" {
			anywhere = append(anywhere, rule{p, "default"})
		}
	}
	for _, p := range defaultInclude {
		include = append(include, rule{p, "default"})
	}
	for _, p := range cfg.Include {
		include = append(include, rule{p, "config"})
	}
	return anywhere, include
}

// classify judges everything under path. It never fails on what it finds
// (unreadable folders, a repository git cannot list): those are in Errors
// and nothing in them is called junk.
func classify(path string, cfg *Config) (*Classified, error) {
	abs, err := filepath.Abs(expandHome(path))
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	c := &classifier{start: abs, cfg: cfg, out: &Classified{
		SchemaVersion: classifySchema,
		Path:          abs,
		Repositories:  []string{},
		Junk:          []Judged{},
		Private:       []Judged{},
		Errors:        []string{},
	}}
	c.anywhere, c.include = classifyRules(cfg)
	if info.Mode()&os.ModeSymlink != 0 {
		return c.out, nil // links are never followed
	}
	// The repository around the path, when the path is inside one but is
	// not its root (a root is found by the walk itself).
	var ctx *repoCtx
	st := outside
	dir := abs
	if !info.IsDir() {
		dir = filepath.Dir(abs)
	}
	if !info.IsDir() || !hasGit(abs) {
		if top, err := gitOutput(dir, "rev-parse", "--show-toplevel"); err == nil {
			root := filepath.Clean(strings.TrimSpace(top))
			if rel, err := filepath.Rel(root, abs); err == nil && rel != "." && !strings.HasPrefix(rel, "..") {
				rel = filepath.ToSlash(rel)
				// Listed from its top folder: git (2.43) fails on a pathspec
				// three folders deep inside an ignored folder.
				ctx = c.openRepo(root, strings.SplitN(rel, "/", 2)[0])
				st = ctx.standingOf(rel)
				// A folder above the path that is junk makes the path junk.
				parts := strings.Split(rel, "/")
				up := tracked
				for i := 0; i < len(parts)-1; i++ {
					a := strings.Join(parts[:i+1], "/")
					up = ctx.child(up, a)
					if r, ok := c.junkRule(filepath.Join(root, filepath.FromSlash(a)), true, ctx, up); ok {
						c.out.Junk = append(c.out.Junk, Judged{".", info.IsDir(), r.pat, r.source})
						return c.out, nil
					}
				}
			}
		}
	}
	c.visit(abs, info.IsDir(), ctx, st)
	sort.Slice(c.out.Junk, func(i, j int) bool { return c.out.Junk[i].Path < c.out.Junk[j].Path })
	sort.Slice(c.out.Private, func(i, j int) bool { return c.out.Private[i].Path < c.out.Private[j].Path })
	return c.out, nil
}

func hasGit(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

func (c *classifier) rel(abs string) string {
	r, err := filepath.Rel(c.start, abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

// outsideRel is abs relative to the folder holding the path, so patterns
// judge the path's own name too ("DerivedData" given by itself is junk).
func (c *classifier) outsideRel(abs string) string {
	r, err := filepath.Rel(filepath.Dir(c.start), abs)
	if err != nil {
		return abs
	}
	return filepath.ToSlash(r)
}

func (c *classifier) fail(format string, args ...any) {
	c.out.Errors = append(c.out.Errors, fmt.Sprintf(format, args...))
}

// openRepo lists root's ignored and untracked entries, below pathspec when
// it is given (so a path deep in a large repository lists only its part).
func (c *classifier) openRepo(root, pathspec string) *repoCtx {
	ctx := &repoCtx{root: root, ignored: map[string]bool{}, untracked: map[string]bool{}}
	rules, err := c.cfg.RulesFor(root)
	if err != nil {
		c.fail("%v (its .gitpriv is not used)", err)
		rules = c.cfg.Rules()
	}
	file, _ := loadRepoFile(root)
	fromFile := map[string]bool{}
	for _, p := range file.Skip {
		fromFile["s"+p] = true
	}
	for _, p := range file.Include {
		fromFile["i"+p] = true
	}
	source := func(kind, p string, defaults []string) string {
		if fromFile[kind+p] {
			return repoRulesFile
		}
		for _, d := range defaults {
			if d == p {
				return "default"
			}
		}
		return "config"
	}
	defaults := append(append([]string{}, defaultSkip...), defaultSkipIgnored...)
	for _, p := range rules.Skip {
		if p != "**/.git/**" {
			ctx.skip = append(ctx.skip, rule{p, source("s", p, defaults)})
		}
	}
	for _, p := range rules.Include {
		ctx.include = append(ctx.include, rule{p, source("i", p, defaultInclude)})
	}
	list := func(args ...string) ([]string, error) {
		args = append([]string{"-C", root, "ls-files", "-z", "--others", "--exclude-standard", "--directory"}, args...)
		if pathspec != "" {
			args = append(args, "--", ":(literal)"+pathspec)
		}
		cmd := exec.Command("git", args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files in %s: %v: %s", root, err, strings.TrimSpace(stderr.String()))
		}
		var entries []string
		for _, e := range bytes.Split(out, []byte{0}) {
			if len(e) > 0 {
				entries = append(entries, string(e))
			}
		}
		return entries, nil
	}
	ign, err := list("--ignored")
	if err == nil {
		var unt []string
		unt, err = list()
		for _, e := range unt {
			ctx.untracked[strings.TrimSuffix(e, "/")] = true
		}
	}
	if err != nil {
		c.fail("%v (nothing in it is called junk)", err)
		ctx.broken = true
		return ctx
	}
	// With --directory git also lists folders that merely hold ignored
	// files; check-ignore keeps the folders that are ignored themselves.
	var dirs []string
	for _, e := range ign {
		if strings.HasSuffix(e, "/") {
			dirs = append(dirs, e)
		} else {
			ctx.ignored[e] = true
		}
	}
	confirmed, err := checkIgnored(root, dirs)
	if err != nil {
		c.fail("%v (nothing in it is called junk)", err)
		ctx.broken = true
		return ctx
	}
	for d := range confirmed {
		ctx.ignored[strings.TrimSuffix(d, "/")] = true
	}
	return ctx
}

// standingOf judges a repository-relative path by itself and every folder
// above it.
func (r *repoCtx) standingOf(rel string) standing {
	st := tracked
	parts := strings.Split(rel, "/")
	for i := range parts {
		st = r.child(st, strings.Join(parts[:i+1], "/"))
	}
	return st
}

// child is the standing of rel inside a folder of standing parent.
func (r *repoCtx) child(parent standing, rel string) standing {
	switch {
	case r.broken:
		return tracked
	case parent == ignored || r.ignored[rel]:
		return ignored
	case parent == untracked || r.untracked[rel]:
		return untracked
	}
	return tracked
}

func (r *repoCtx) relOf(abs string) string {
	rel, err := filepath.Rel(r.root, abs)
	if err != nil {
		return ""
	}
	return filepath.ToSlash(rel)
}

// junkRule is the rule that makes a folder (dir) or file junk, if one does.
func (c *classifier) junkRule(abs string, dir bool, ctx *repoCtx, st standing) (rule, bool) {
	if st == tracked {
		return rule{}, false
	}
	if dir {
		if m := junkMarker(abs); m != "" {
			return rule{m, "marker"}, true
		}
	}
	rules, rel := c.anywhere, c.outsideRel(abs)
	if ctx != nil {
		rel = ctx.relOf(abs)
		if st == ignored {
			rules = ctx.skip
		}
	}
	for _, r := range rules {
		np := normPattern(r.pat)
		if dir && strings.HasSuffix(np, "/**") && matchGlob(rel, np) {
			return r, true
		}
		if !dir && matchGlob(rel, np) {
			return r, true
		}
	}
	return rule{}, false
}

func (c *classifier) visit(abs string, isDir bool, ctx *repoCtx, st standing) {
	if isDir && hasGit(abs) && (ctx == nil || abs != ctx.root) {
		// A repository (or a submodule, or one nested in another's ignored
		// folder) is judged by itself and is never junk.
		ctx = c.openRepo(abs, "")
		st = tracked
		c.out.Repositories = append(c.out.Repositories, c.rel(abs))
	}
	if r, ok := c.junkRule(abs, isDir, ctx, st); ok {
		c.out.Junk = append(c.out.Junk, Judged{c.rel(abs), isDir, r.pat, r.source})
		return
	}
	if !isDir {
		if st == tracked {
			return
		}
		include, rel := c.include, c.outsideRel(abs)
		if ctx != nil {
			include, rel = ctx.include, ctx.relOf(abs)
		}
		for _, r := range include {
			if matchGlob(rel, r.pat) {
				c.out.Private = append(c.out.Private, Judged{c.rel(abs), false, r.pat, r.source})
				return
			}
		}
		return
	}
	if filepath.Base(abs) == ".git" {
		return
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		c.fail("%v", err)
		return
	}
	for _, e := range entries {
		if e.Type()&os.ModeSymlink != 0 || !(e.IsDir() || e.Type().IsRegular()) {
			continue
		}
		child := filepath.Join(abs, e.Name())
		cst := st
		if ctx != nil {
			cst = ctx.child(st, ctx.relOf(child))
		}
		c.visit(child, e.IsDir(), ctx, cst)
	}
}

func cmdClassify(args []string) error {
	asJSON := false
	var paths []string
	for _, a := range args {
		switch {
		case a == "--json":
			asJSON = true
		case strings.HasPrefix(a, "-"):
			return fmt.Errorf("unknown flag %q (usage: gitpriv classify [--json] PATH)", a)
		default:
			paths = append(paths, a)
		}
	}
	if len(paths) != 1 {
		return errors.New("usage: gitpriv classify [--json] PATH")
	}
	cfg, err := loadConfig(false)
	if err != nil {
		return err
	}
	out, err := classify(paths[0], cfg)
	if err != nil {
		return err
	}
	if asJSON {
		data, err := json.Marshal(out)
		if err != nil {
			return err
		}
		_, err = fmt.Printf("%s\n", data)
		return err
	}
	printClassified(os.Stdout, out)
	return nil
}

func printClassified(w io.Writer, out *Classified) {
	fmt.Fprintf(w, "%s: %d junk, %d private, %d repositories\n", out.Path, len(out.Junk), len(out.Private), len(out.Repositories))
	for _, j := range out.Junk {
		name := j.Path
		if j.Dir {
			name += "/"
		}
		fmt.Fprintf(w, "  %-8s %s (%s, %s)\n", "junk", name, j.Rule, j.Source)
	}
	for _, p := range out.Private {
		fmt.Fprintf(w, "  %-8s %s (%s, %s)\n", "private", p.Path, p.Rule, p.Source)
	}
	for _, e := range out.Errors {
		fmt.Fprintf(w, "  %-8s %s\n", "error", e)
	}
}
