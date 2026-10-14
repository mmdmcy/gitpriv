package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// Repo is a git repository identified by its top-level directory.
type Repo struct {
	Root      string // absolute path of the work tree root
	Key       string // store directory, e.g. "owner/name" or "local-name"
	LegacyKey string // key used by gitpriv <= 10a9b8c, for fallback reads
}

// resolveRepos maps each argument (default: cwd) to its repository root, so
// running from a subdirectory behaves the same as running from the root.
func resolveRepos(args []string) ([]Repo, error) {
	if len(args) == 0 {
		args = []string{"."}
	}
	var repos []Repo
	seen := map[string]bool{}
	for _, a := range args {
		abs, err := filepath.Abs(expandHome(a))
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			abs = filepath.Dir(abs)
		}
		out, err := gitOutput(abs, "rev-parse", "--show-toplevel")
		if err != nil || strings.TrimSpace(out) == "" {
			return nil, fmt.Errorf("%s is not inside a git work tree", abs)
		}
		root := filepath.Clean(strings.TrimSpace(out))
		if seen[root] {
			continue
		}
		seen[root] = true
		remote, _ := gitOutput(root, "config", "--get", "remote.origin.url")
		remote = strings.TrimSpace(remote)
		repos = append(repos, Repo{
			Root:      root,
			Key:       repoKey(root, remote),
			LegacyKey: legacyKey(root, remote),
		})
	}
	return repos, nil
}

// repoKey is "<owner>/<name>" from the origin remote, independent of host,
// protocol, port and user, so moving a repo between hosts (GitHub ->
// Forgejo) keeps its store entries. Repos without a usable remote get
// "local-<dirname>".
func repoKey(root, remote string) string {
	if k := remoteOwnerName(remote); k != "" {
		return k
	}
	return "local-" + filepath.Base(root)
}

// remoteOwnerName extracts "owner/name" from any common remote URL form:
//
//	git@host:owner/name.git
//	ssh://git@host:2222/owner/name.git
//	https://user@host/owner/name.git
//	/srv/git/owner/name.git
func remoteOwnerName(remote string) string {
	p := strings.TrimSpace(remote)
	if p == "" {
		return ""
	}
	if strings.Contains(p, "://") {
		u, err := url.Parse(p)
		if err != nil {
			return ""
		}
		p = u.Path
	} else if i := strings.Index(p, ":"); i > 0 && !strings.Contains(p[:i], "/") {
		p = p[i+1:] // scp-like: [user@]host:path
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	var segs []string
	for _, s := range strings.Split(p, "/") {
		if s != "" && s != "." && s != ".." {
			segs = append(segs, s)
		}
	}
	if len(segs) < 2 {
		return ""
	}
	return segs[len(segs)-2] + "/" + segs[len(segs)-1]
}

// legacyKey reproduces the original key scheme (full URL flattened with
// dashes, or the bare directory name) so existing stores keep working.
func legacyKey(root, remote string) string {
	u := remote
	for _, p := range []string{"https://", "http://", "ssh://", "git@", "file://"} {
		u = strings.TrimPrefix(u, p)
	}
	u = strings.TrimSuffix(u, ".git")
	u = strings.NewReplacer(":", "-", "/", "-", "@", "-").Replace(u)
	if u != "" {
		return u
	}
	return filepath.Base(root)
}

// storeDir returns the directory holding repo's files in the store. It is
// the new key, unless only a legacy-key directory exists.
func storeDir(store string, r Repo) (dir string, legacy bool) {
	dir = filepath.Join(store, filepath.FromSlash(r.Key))
	if isDir(dir) || r.LegacyKey == r.Key {
		return dir, false
	}
	old := filepath.Join(store, filepath.FromSlash(r.LegacyKey))
	if isDir(old) {
		return old, true
	}
	return dir, false
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
