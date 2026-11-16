package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// judged maps each junk or private path to "rule source".
func judged(list []Judged) map[string]string {
	m := map[string]string{}
	for _, j := range list {
		m[j.Path] = j.Rule + " " + j.Source
	}
	return m
}

func mustClassify(t *testing.T, path string) *Classified {
	t.Helper()
	out, err := classify(path, &Config{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestClassifyInsideARepository(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "", ".env*\nbuild/\nreport/\nnode_modules/\n*.log\n")
	write(t, filepath.Join(dir, ".env"), "A=1\n")
	write(t, filepath.Join(dir, "build/app.js"), "x")                          // ignored, junk only when ignored
	write(t, filepath.Join(dir, "node_modules/pkg/index.js"), "x")             // ignored
	write(t, filepath.Join(dir, "report/qa/DerivedData/Build/x.o"), "x")       // ignored folder, junk deep inside
	write(t, filepath.Join(dir, "report/qa/notes.txt"), "keep")                // ignored, unknown: carried
	write(t, filepath.Join(dir, "app/.dart_tool/cache"), "x")                  // untracked, rebuildable anywhere
	write(t, filepath.Join(dir, "tools/my-env/pyvenv.cfg"), "home = /usr\n")   // untracked virtualenv
	write(t, filepath.Join(dir, "cache/CACHEDIR.TAG"), cacheDirSignature+"\n") // untracked cache
	write(t, filepath.Join(dir, "fake/CACHEDIR.TAG"), "not the signature\n")   // not a cache
	write(t, filepath.Join(dir, "out/result.csv"), "data")                     // untracked "out": not junk
	write(t, filepath.Join(dir, "vendor/node_modules/kept.js"), "tracked")     // tracked: never junk
	write(t, filepath.Join(dir, "x.log"), "log")                               // ignored file
	run(t, dir, "add", "-f", ".gitignore", "vendor/node_modules/kept.js")
	run(t, dir, "commit", "-qm", "init")

	out := mustClassify(t, dir)
	want := map[string]string{
		"build":                 "**/build/** default",
		"node_modules":          "**/node_modules/** default",
		"report/qa/DerivedData": "**/DerivedData/** default",
		"app/.dart_tool":        "**/.dart_tool/** default",
		"tools/my-env":          "pyvenv.cfg marker",
		"cache":                 "CACHEDIR.TAG marker",
		"x.log":                 "**/*.log default",
	}
	if got := judged(out.Junk); !reflect.DeepEqual(got, want) {
		t.Errorf("junk = %v\nwant %v", got, want)
	}
	if got := judged(out.Private); !reflect.DeepEqual(got, map[string]string{".env": "**/.env* default"}) {
		t.Errorf("private = %v", got)
	}
	if !reflect.DeepEqual(out.Repositories, []string{"."}) || len(out.Errors) != 0 {
		t.Errorf("repositories = %v, errors = %v", out.Repositories, out.Errors)
	}
}

func TestClassifyOutsideARepository(t *testing.T) {
	gitEnv(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, "qa/DerivedData/a/b.o"), "x")
	write(t, filepath.Join(dir, "qa/Index.noindex/c"), "x")
	write(t, filepath.Join(dir, "photos/build/IMG_1.jpg"), "a real folder called build")
	write(t, filepath.Join(dir, "photos/trip.log"), "a diary, not a log")
	write(t, filepath.Join(dir, ".DS_Store"), "x")
	write(t, filepath.Join(dir, "keys/deploy.pem"), "pem")
	// A repository nested in a plain folder is judged by its own rules.
	repo := filepath.Join(dir, "code")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, repo, "init", "-q")
	write(t, filepath.Join(repo, ".gitignore"), "dist/\n")
	write(t, filepath.Join(repo, "dist/a.js"), "x")

	out := mustClassify(t, dir)
	want := map[string]string{
		"qa/DerivedData":   "**/DerivedData/** default",
		"qa/Index.noindex": "**/*.noindex/** default",
		".DS_Store":        "**/.DS_Store default",
		"code/dist":        "**/dist/** default",
	}
	if got := judged(out.Junk); !reflect.DeepEqual(got, want) {
		t.Errorf("junk = %v\nwant %v", got, want)
	}
	if got := judged(out.Private); !reflect.DeepEqual(got, map[string]string{"keys/deploy.pem": "**/*.pem default"}) {
		t.Errorf("private = %v", got)
	}
	if !reflect.DeepEqual(out.Repositories, []string{"code"}) {
		t.Errorf("repositories = %v", out.Repositories)
	}
	// The path itself can be junk.
	if got := judged(mustClassify(t, filepath.Join(dir, "qa/DerivedData")).Junk); !reflect.DeepEqual(got, map[string]string{".": "**/DerivedData/** default"}) {
		t.Errorf("DerivedData by itself = %v", got)
	}
}

func TestClassifyAPathDeepInARepository(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "", "generated/\n")
	write(t, filepath.Join(dir, "generated/node_modules/x/y.js"), "x")
	write(t, filepath.Join(dir, "generated/keep.txt"), "x")
	write(t, filepath.Join(dir, "src/main.go"), "package main")
	run(t, dir, "add", ".gitignore", "src/main.go")

	if got := judged(mustClassify(t, filepath.Join(dir, "generated")).Junk); !reflect.DeepEqual(got, map[string]string{"node_modules": "**/node_modules/** default"}) {
		t.Errorf("generated = %v", got)
	}
	if got := judged(mustClassify(t, filepath.Join(dir, "generated/node_modules/x")).Junk); !reflect.DeepEqual(got, map[string]string{".": "**/node_modules/** default"}) {
		t.Errorf("inside node_modules = %v", got)
	}
	if out := mustClassify(t, filepath.Join(dir, "src")); len(out.Junk) != 0 {
		t.Errorf("src = %v", out.Junk)
	}
}

func TestRepoFileRules(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "", "fixtures/\nlocal/\nnotes.md\n")
	write(t, filepath.Join(dir, repoRulesFile), "# rebuilt by make fixtures\nskip fixtures/\n\ninclude notes.md\n")
	write(t, filepath.Join(dir, "fixtures/big.bin"), "x")
	write(t, filepath.Join(dir, "local/a.txt"), "x")
	write(t, filepath.Join(dir, "notes.md"), "mine")

	out := mustClassify(t, dir)
	if got := judged(out.Junk); !reflect.DeepEqual(got, map[string]string{"fixtures": "fixtures/ .gitpriv"}) {
		t.Errorf("junk = %v", got)
	}
	if got := judged(out.Private); !reflect.DeepEqual(got, map[string]string{"notes.md": "notes.md .gitpriv"}) {
		t.Errorf("private = %v", got)
	}
	// push and scan read it too.
	res, err := collect(dir, mustRules(t, dir))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Rel != "notes.md" {
		t.Errorf("collect files = %+v", res.Files)
	}
}

func mustRules(t *testing.T, dir string) Rules {
	t.Helper()
	r, err := (&Config{}).RulesFor(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRepoFileRefusesWhatItCannotRead(t *testing.T) {
	for _, bad := range []string{"fixtures/\n", "exclude x\n", "skip\n", "skip a\tb\n"} {
		if _, err := parseRepoFile([]byte(bad)); err == nil {
			t.Errorf("%q: expected an error", bad)
		}
	}
	f, err := parseRepoFile([]byte("  # c\nskip  a b/\ninclude /x\n"))
	if err != nil || !reflect.DeepEqual(f.Skip, []string{"a b/"}) || !reflect.DeepEqual(f.Include, []string{"/x"}) {
		t.Errorf("parse = %+v, %v", f, err)
	}
	// A bad file is reported and not used; the defaults still judge.
	gitEnv(t)
	dir := newRepo(t, "", "node_modules/\nfixtures/\n")
	write(t, filepath.Join(dir, repoRulesFile), "exclude fixtures/\n")
	write(t, filepath.Join(dir, "node_modules/a.js"), "x")
	write(t, filepath.Join(dir, "fixtures/a"), "x")
	out := mustClassify(t, dir)
	if got := judged(out.Junk); !reflect.DeepEqual(got, map[string]string{"node_modules": "**/node_modules/** default"}) {
		t.Errorf("junk = %v", got)
	}
	if len(out.Errors) != 1 || !strings.Contains(out.Errors[0], "line 1") {
		t.Errorf("errors = %v", out.Errors)
	}
}

func TestCollectPrunesMarkedFolders(t *testing.T) {
	gitEnv(t)
	dir := newRepo(t, "", "*\n!.gitignore\n")
	write(t, filepath.Join(dir, "venv-tf/pyvenv.cfg"), "home = /usr\n")
	write(t, filepath.Join(dir, "venv-tf/.env"), "junk")
	write(t, filepath.Join(dir, ".env"), "A=1")
	res, err := collect(dir, (&Config{}).Rules())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 1 || res.Files[0].Rel != ".env" {
		t.Errorf("files = %+v", res.Files)
	}
}

func TestClassifyCommandJSON(t *testing.T) {
	gitEnv(t)
	dir := tempDir(t)
	write(t, filepath.Join(dir, "a/node_modules/x"), "x")
	bin := filepath.Join(tempDir(t), "gitpriv")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "classify", "--json", dir)
	cmd.Env = os.Environ()
	data, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(string(data)), "\n") != 0 {
		t.Errorf("want one line of JSON, got %q", data)
	}
	var got Classified
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != classifySchema || got.Path != dir || len(got.Junk) != 1 || got.Junk[0].Path != "a/node_modules" || !got.Junk[0].Dir {
		t.Errorf("classify = %+v", got)
	}
	if err := exec.Command(bin, "classify").Run(); err == nil {
		t.Error("classify without a path should fail")
	}
}
