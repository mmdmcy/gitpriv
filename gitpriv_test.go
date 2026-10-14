package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// gitEnv isolates tests from the user's git config.
func gitEnv(t *testing.T) {
	t.Helper()
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "test")
	t.Setenv("GIT_AUTHOR_EMAIL", "test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "test")
	t.Setenv("GIT_COMMITTER_EMAIL", "test@example.com")
	t.Setenv("GITPRIV_CONFIG", filepath.Join(t.TempDir(), "config.json"))
}

func tempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func run(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// squash collapses runs of whitespace so tests don't depend on alignment.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// newRepo creates a git repo with the given remote (may be empty) and
// .gitignore content.
func newRepo(t *testing.T, remote, gitignore string) string {
	t.Helper()
	dir := tempDir(t)
	run(t, dir, "init", "-q")
	if remote != "" {
		run(t, dir, "remote", "add", "origin", remote)
	}
	write(t, filepath.Join(dir, ".gitignore"), gitignore)
	return dir
}

func newStore(t *testing.T) *Config {
	t.Helper()
	store := tempDir(t)
	run(t, store, "init", "-q")
	return &Config{Store: store}
}

func mustRepo(t *testing.T, dir string) Repo {
	t.Helper()
	repos, err := resolveRepos([]string{dir})
	if err != nil {
		t.Fatal(err)
	}
	return repos[0]
}

func TestMatchGlob(t *testing.T) {
	cases := []struct {
		rel, pat string
		want     bool
	}{
		{".env", "**/.env*", true},
		{"a/b/.env.local", "**/.env*", true},
		{"a/b/env", "**/.env*", false},
		{"node_modules/x/y.js", "**/node_modules/**", true},
		{"a/node_modules/x", "**/node_modules/**", true},
		{"node_modules", "**/node_modules/**", true},
		{"a/.DS_Store", "**/.DS_Store", true},
		{"a/.DS_Store", ".DS_Store", true}, // no slash: any depth
		{"a/.DS_Store", "/.DS_Store", false},
		{".DS_Store", "/.DS_Store", true},
		{"x/out/y.txt", "out/", true}, // trailing slash: dir anywhere
		{"x/output/y.txt", "out/", false},
		{"apps/m/key.pem", "*.pem", true},
		{"a/b/c.txt", "a/*.txt", false},
		{"a/c.txt", "a/*.txt", true},
		{"a/x/y/c.txt", "a/**/c.txt", true},
		{"a/c.txt", "a/**/c.txt", true},
		{".venv-3.12/lib/x.py", "**/.venv*/**", true},
		{"src/config.local.ts", "**/*.private.*", false},
		{"p/pb_data/data.db", "**/pb_data/**", true},
	}
	for _, c := range cases {
		if got := matchGlob(c.rel, c.pat); got != c.want {
			t.Errorf("matchGlob(%q, %q) = %v, want %v", c.rel, c.pat, got, c.want)
		}
	}
	if !dirSkipped("a/node_modules", defaultSkip) || dirSkipped("a/src", defaultSkip) {
		t.Error("dirSkipped wrong")
	}
}

func TestRemoteKey(t *testing.T) {
	cases := map[string]string{
		"git@github.com:Owner/name.git":               "Owner/name",
		"github.com:Owner/name":                       "Owner/name",
		"ssh://git@forge.example:2222/Owner/name.git": "Owner/name",
		"ssh://forge.example/Owner/name":              "Owner/name",
		"https://user@github.com/Owner/name.git":      "Owner/name",
		"https://user:tok@github.com/Owner/name.git/": "Owner/name",
		"http://192.168.1.5:3000/Owner/name.git":      "Owner/name",
		"https://gitlab.com/group/sub/Owner/name.git": "Owner/name",
		"/srv/git/Owner/name.git":                     "Owner/name",
		"file:///srv/git/Owner/name.git":              "Owner/name",
		"":                                            "local-repo",
		"name.git":                                    "local-repo",
	}
	for remote, want := range cases {
		if got := repoKey("/x/repo", remote); got != want {
			t.Errorf("repoKey(%q) = %q, want %q", remote, got, want)
		}
	}
	if got := legacyKey("/x/repo", "https://github.com/Owner/name.git"); got != "github.com-Owner-name" {
		t.Errorf("legacyKey = %q", got)
	}
	if got := legacyKey("/x/repo", ""); got != "repo" {
		t.Errorf("legacyKey no remote = %q", got)
	}
}

func TestSubdirInvocation(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "git@github.com:me/proj.git", ".env*\n")
	sub := filepath.Join(dir, "supabase", "functions")
	write(t, filepath.Join(sub, ".env.deploy"), "X=1\n")
	repos, err := resolveRepos([]string{sub, dir})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Root != dir || repos[0].Key != "me/proj" {
		t.Fatalf("resolveRepos = %+v, want one repo at %s", repos, dir)
	}
	res, err := collect(repos[0].Root, (&Config{}).Rules())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Rel != "supabase/functions/.env.deploy" {
		t.Fatalf("files = %+v", res.Files)
	}
	if _, err := resolveRepos([]string{tempDir(t)}); err == nil {
		t.Error("expected error for a non-repo directory")
	}
}

func TestCollectFilters(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "", "*\n!.gitignore\n!.env.example\n!src/\n!src/**\n")
	write(t, filepath.Join(dir, ".env"), "A=1\n")
	write(t, filepath.Join(dir, ".env.example"), "A=\n") // not ignored
	write(t, filepath.Join(dir, "apps/web/.env.local"), "B=1\n")
	write(t, filepath.Join(dir, "certs/dev.pem"), "pem\n")
	write(t, filepath.Join(dir, "notes.md"), "private notes\n")            // ignored, not included
	write(t, filepath.Join(dir, "node_modules/pkg/.env"), "junk\n")        // skipped dir
	write(t, filepath.Join(dir, ".venv-3.12/lib/.env"), "junk\n")          // skipped dir
	write(t, filepath.Join(dir, "apps/web/.next/.env"), "junk\n")          // skipped dir
	write(t, filepath.Join(dir, "a/.DS_Store"), "junk\n")                  // not included
	write(t, filepath.Join(dir, "data/app.db"), strings.Repeat("x", 2048)) // too large
	write(t, filepath.Join(dir, "src/app.ts"), "code\n")                   // not ignored
	if err := os.Symlink(filepath.Join(dir, ".env"), filepath.Join(dir, ".env.link")); err != nil {
		t.Fatal(err)
	}
	cfg := &Config{MaxFileBytes: 1024, Include: []string{"notes.md"}, Skip: []string{"certs/"}}
	res, err := collect(dir, cfg.Rules())
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range res.Files {
		got = append(got, f.Rel)
	}
	want := []string{".env", "apps/web/.env.local", "notes.md"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
	reasons := map[string]string{}
	for _, s := range res.Skipped {
		reasons[s.Rel] = s.Reason
	}
	if !strings.HasPrefix(reasons["data/app.db"], "too large") {
		t.Errorf("data/app.db: %q", reasons["data/app.db"])
	}
	if !strings.HasPrefix(reasons[".env.link"], "symlink") {
		t.Errorf(".env.link: %q", reasons[".env.link"])
	}
	if len(res.Errs) != 0 {
		t.Errorf("errs = %v", res.Errs)
	}
	// certs/ is skipped by the user config (merged with defaults).
	if _, ok := reasons["certs/dev.pem"]; ok {
		t.Errorf("certs/dev.pem should be pruned silently, got %q", reasons["certs/dev.pem"])
	}
}

func TestCollectReportsWalkErrors(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores permissions")
	}
	gitEnv(t)
	dir := newRepo(t, "", "secrets/\n")
	locked := filepath.Join(dir, "secrets", "locked")
	write(t, filepath.Join(locked, "a.key"), "k\n")
	if err := os.Chmod(locked, 0o000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(locked, 0o755)
	res, err := collect(dir, (&Config{}).Rules())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errs) == 0 {
		t.Error("expected a walk error for an unreadable directory")
	}
}

func TestPushPullRoundTrip(t *testing.T) {
	gitEnv(t)
	cfg := newStore(t)
	a := newRepo(t, "git@github.com:me/proj.git", ".env*\nlocal.txt\n")
	write(t, filepath.Join(a, ".env"), "SECRET=a\n")
	write(t, filepath.Join(a, "sub/.env.local"), "SECRET=b\n")
	ra := mustRepo(t, a)

	// dry-run changes nothing in the store
	var buf bytes.Buffer
	if _, err := pushRepo(cfg, ra, Opts{DryRun: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(cfg.Store, "me")) {
		t.Fatal("dry-run push wrote to the store")
	}
	if !strings.Contains(buf.String(), "would add") {
		t.Errorf("dry-run output: %s", buf.String())
	}
	if _, err := pushRepo(cfg, ra, Opts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(cfg.Store, "me/proj/.env")) != "SECRET=a\n" {
		t.Fatal("push did not copy .env")
	}
	if out, _ := gitOutput(cfg.Store, "status", "--porcelain"); out != "" {
		t.Fatalf("store not committed: %s", out)
	}

	// Machine B: same remote on another host, one file already present.
	b := newRepo(t, "ssh://git@forgejo.lan:2222/me/proj.git", ".env*\n")
	write(t, filepath.Join(b, ".env"), "SECRET=local\n")
	// store has a file that B's repo does NOT ignore
	write(t, filepath.Join(cfg.Store, "me/proj/local.txt"), "x\n")
	rb := mustRepo(t, b)
	if rb.Key != ra.Key {
		t.Fatalf("keys differ across hosts: %s vs %s", ra.Key, rb.Key)
	}

	buf.Reset()
	n, err := pullRepo(cfg, rb, Opts{DryRun: true}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(b, "sub/.env.local")) {
		t.Fatal("dry-run pull restored a file")
	}
	if n != 1 || !strings.Contains(squash(buf.String()), "would restore sub/.env.local") {
		t.Errorf("dry-run pull: n=%d\n%s", n, buf.String())
	}

	buf.Reset()
	n, err = pullRepo(cfg, rb, Opts{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(b, "sub/.env.local")) != "SECRET=b\n" {
		t.Error("pull did not restore sub/.env.local")
	}
	if read(t, filepath.Join(b, ".env")) != "SECRET=local\n" {
		t.Error("pull overwrote an existing file")
	}
	if exists(filepath.Join(b, "local.txt")) {
		t.Error("pull restored a file the target repo does not ignore")
	}
	out := squash(buf.String())
	if n != 1 || !strings.Contains(out, "differs .env") || !strings.Contains(out, "refused local.txt") {
		t.Errorf("pull: n=%d\n%s", n, out)
	}
}

func TestPushConflictWhenStoreNewer(t *testing.T) {
	gitEnv(t)
	cfg := newStore(t)
	a := newRepo(t, "git@github.com:me/proj.git", ".env\n")
	write(t, filepath.Join(a, ".env"), "OLD\n")
	old := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(a, ".env"), old, old)
	write(t, filepath.Join(cfg.Store, "me/proj/.env"), "NEW FROM OTHER MACHINE\n")
	run(t, cfg.Store, "add", "-A")
	run(t, cfg.Store, "commit", "-qm", "other machine")
	r := mustRepo(t, a)
	var buf bytes.Buffer
	n, err := pushRepo(cfg, r, Opts{}, &buf)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || read(t, filepath.Join(cfg.Store, "me/proj/.env")) != "NEW FROM OTHER MACHINE\n" {
		t.Fatalf("push overwrote a newer store copy: n=%d\n%s", n, buf.String())
	}
	if _, err := pushRepo(cfg, r, Opts{Force: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(cfg.Store, "me/proj/.env")) != "OLD\n" {
		t.Fatal("push --force did not overwrite")
	}
}

func TestLegacyKeyFallback(t *testing.T) {
	gitEnv(t)
	cfg := newStore(t)
	a := newRepo(t, "https://github.com/me/proj.git", ".env\n")
	r := mustRepo(t, a)
	write(t, filepath.Join(cfg.Store, "github.com-me-proj/.env"), "LEGACY\n")
	run(t, cfg.Store, "add", "-A")
	run(t, cfg.Store, "commit", "-qm", "legacy")

	dir, legacy := storeDir(cfg.Store, r)
	if !legacy || dir != filepath.Join(cfg.Store, "github.com-me-proj") {
		t.Fatalf("storeDir = %s, %v", dir, legacy)
	}
	var buf bytes.Buffer
	if _, err := pullRepo(cfg, r, Opts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(a, ".env")) != "LEGACY\n" || !strings.Contains(buf.String(), "legacy store dir") {
		t.Fatalf("legacy pull:\n%s", buf.String())
	}
	// push migrates the legacy dir to the new key
	if _, err := pushRepo(cfg, r, Opts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(cfg.Store, "github.com-me-proj")) || !exists(filepath.Join(cfg.Store, "me/proj/.env")) {
		t.Fatal("push did not migrate the legacy store dir")
	}
	if out, _ := gitOutput(cfg.Store, "status", "--porcelain"); out != "" {
		t.Fatalf("migration not committed: %s", out)
	}
}

func TestStatusClassification(t *testing.T) {
	gitEnv(t)
	cfg := newStore(t)
	dir := newRepo(t, "", ".env*\nbig.db\n")
	r := mustRepo(t, dir)
	if r.Key != "local-"+filepath.Base(dir) {
		t.Fatalf("key = %s", r.Key)
	}
	sd := filepath.Join(cfg.Store, r.Key)
	write(t, filepath.Join(dir, ".env"), "same\n")
	write(t, filepath.Join(sd, ".env"), "same\n")
	write(t, filepath.Join(dir, ".env.local"), "only local\n")
	write(t, filepath.Join(sd, ".env.prod"), "only store\n")
	write(t, filepath.Join(sd, "tracked.txt"), "x\n") // not ignored in repo
	write(t, filepath.Join(dir, ".env.dev"), "local version\n")
	write(t, filepath.Join(sd, ".env.dev"), "store version\n")
	write(t, filepath.Join(dir, "big.db"), strings.Repeat("x", 200))
	cfg.MaxFileBytes = 100

	entries, _, errs, err := repoStatus(cfg, r)
	if err != nil || len(errs) > 0 {
		t.Fatal(err, errs)
	}
	got := map[string]string{}
	for _, e := range entries {
		got[e.Rel] = e.Class
	}
	want := map[string]string{
		".env":        StInSync,
		".env.local":  StLocalOnly,
		".env.prod":   StStoreOnly,
		"tracked.txt": StStoreOnly,
		".env.dev":    StDiffers,
		"big.db":      StSkipped,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("status = %v\nwant     %v", got, want)
	}
	var buf bytes.Buffer
	if n, err := statusRepo(cfg, r, Opts{}, &buf); err != nil || n != 0 {
		t.Fatalf("statusRepo n=%d err=%v", n, err)
	}
	if !strings.Contains(buf.String(), "pull would refuse") {
		t.Errorf("status output:\n%s", buf.String())
	}
}

func TestInitDoesNotFreezeDefaults(t *testing.T) {
	gitEnv(t)
	cfg := newStore(t)
	write(t, configPath(), `{"store":"/old","skip":["**/mine/**"]}`)
	if err := cmdInit([]string{"--store", cfg.Store}); err != nil {
		t.Fatal(err)
	}
	got, err := loadConfig(true)
	if err != nil {
		t.Fatal(err)
	}
	if got.Store != cfg.Store || !reflect.DeepEqual(got.Skip, []string{"**/mine/**"}) || got.Include != nil {
		t.Fatalf("config after init = %+v", got)
	}
	rules := got.Rules()
	if len(rules.Skip) != len(defaultSkip)+1 || len(rules.Include) != len(defaultInclude) {
		t.Errorf("rules not merged: %+v", rules)
	}
}
