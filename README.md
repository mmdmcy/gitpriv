# gitpriv

Sync the **private files of your git repos** (`.env` files, keys, local
databases) between your machines, without dragging along build output,
virtualenvs or `node_modules`.

Public repos can't hold secrets, but you still want your `.env.local` on every
machine. gitpriv copies the git-ignored *private* files of a repo into a
**store**: a separate, private git repository that you clone on each machine.

## Quick start

```sh
go build -o gitpriv .                      # stdlib only, needs git on PATH

# once: a private store (keep it on a private host, see "Security")
git clone ssh://git@your-forgejo/you/private-store.git ~/private-store
gitpriv init --store ~/private-store

cd ~/src/myapp
gitpriv scan          # what would be synced, and what is skipped and why
gitpriv push -n       # dry run: exactly what push would add/update
gitpriv push          # copy into the store and commit there
git -C ~/private-store push

# on another machine
git -C ~/private-store pull
cd ~/src/myapp
gitpriv status        # in-sync / local-only / store-only / differs / skipped
gitpriv pull -n       # dry run
gitpriv pull          # restore missing files (never overwrites)
```

gitpriv commits to the store but never pushes or pulls it; you move the store
with plain `git`.

## Commands

| command | what it does |
| --- | --- |
| `gitpriv init --store <path>` | write the config with the store path (the store must be a git work tree). Existing `include`/`skip`/`max_file_bytes` are kept; defaults are never written into the file. |
| `gitpriv scan [repo...]` | list files that would be synced, files skipped (with reason), and how many other ignored entries were ignored as non-private. Works without a config. |
| `gitpriv status [repo...]` | classify each private file: `in-sync`, `local-only` (push would add), `store-only` (pull would restore, or would refuse), `differs` (says which side is newer), `skipped` (with reason). |
| `gitpriv push [-n] [--force] [repo...]` | copy added/changed private files into the store and commit only that repo's store directory. Refuses (`conflict`) to overwrite a store copy whose last commit is newer than the local file unless `--force`. |
| `gitpriv pull [-n] [repo...]` | restore store files that are missing locally. |

`repo` defaults to the current directory. Any path inside a repo works: it is
resolved to the repository root (`git rev-parse --show-toplevel`).
`-n`/`--dry-run` prints exactly what would change and changes nothing.
Exit status is non-zero when anything went wrong: scan/walk errors, push
conflicts, or files pull refused to restore.

## What gets synced

A file is synced only if **all** of these hold:

1. it is ignored by the repo's own git rules (`git ls-files --others --ignored --exclude-standard`),
2. it matches an **include** pattern,
3. it matches no **skip** pattern,
4. it is a regular file (symlinks are never followed; they are reported as skipped),
5. it is no larger than `max_file_bytes` (default 5 MiB; larger files are reported as skipped).

Default include patterns (private state):

```
**/.env*  **/*.pem  **/*.key  **/*.p12  **/*.p8  **/*.jks  **/*.keystore
**/*.private.*  **/secrets/**  **/.secrets/**  **/pb_data/**
**/*.sqlite  **/*.sqlite3  **/*.db
```

Default skip patterns (junk, never synced even if included):

```
**/.git/**  **/node_modules/**  **/.venv*/**  **/venv/**  **/__pycache__/**
**/.pytest_cache/**  **/.mypy_cache/**  **/.ruff_cache/**  **/.next/**
**/out/**  **/dist/**  **/build/**  **/target/**  **/.turbo/**  **/.cache/**
**/coverage/**  **/Pods/**  **/android/.gradle/**  **/*.tsbuildinfo
**/.DS_Store  **/*.log
```

Pattern syntax: `*`, `?` and `[...]` match within one path segment, `**`
matches any number of segments. Like `.gitignore`, a pattern without `/`
matches at any depth (`*.pem` = `**/*.pem`), a trailing `/` means a directory
and everything below it (`out/` = `**/out/**`), and a leading `/` anchors at the
repo root (`/notes.md`).

## Config

`~/.config/gitpriv/config.json` (override the path with `GITPRIV_CONFIG`):

```json
{
  "store": "~/private-store",
  "include": ["/LOCAL_NOTES.md", "**/.claude/settings.local.json"],
  "skip": ["**/fixtures/**"],
  "max_file_bytes": 10485760
}
```

- `store` (required for status/push/pull): path to the store work tree; `~` is expanded.
- `include`: **added** to the default include list.
- `skip`: **added** to the default skip list. Configs written by older
  versions (a full `skip` list, no `include`) keep working: their entries are
  treated as extra skips and the default include list applies.
- `max_file_bytes`: per-file size cap; default 5 MiB.

## Store layout

```
private-store/
  Owner/name/.env.local                 from git@github.com:Owner/name.git
  Owner/name/apps/mobile/.env.local
  local-scratch/.env                    repo "scratch" without a remote
```

The key is `<owner>/<name>` from `remote.origin.url`, whatever the host,
protocol, port or user (`git@host:o/n.git`, `ssh://git@host:2222/o/n.git`,
`https://user@host/o/n.git` are all `o/n`), so moving a repo from GitHub to
Forgejo keeps its entries. Repos without a remote use `local-<dirname>`.

Stores written by older versions used the flattened URL as key
(`github.com-Owner-name`). If only that directory exists, status and pull read
it (and say so), and the next push moves it to the new key in its commit.

## Safety guarantees

- Only git-ignored files are read, so tracked files never travel through the store.
- `pull` never overwrites an existing file. If the local file differs from the
  store copy (sha256), it is reported as `differs` and left untouched.
- `pull` runs `git check-ignore` in the target repo before restoring each
  file and **refuses** files that repo does not ignore, so a restored secret
  can't be picked up by a later `git add -A`.
- `pull` does not write through symlinked directories; symlinks are never followed on either side.
- `push` only commits the repo's own directory in the store, never unrelated
  store changes, and does not overwrite a newer store copy without `--force`
  (older versions remain in the store's git history either way).
- Store files are written with mode `0600`.

## Security

Files are stored **plaintext**, and every version stays in the store's git
history. Keep the store on a private host you control, such as the self-hosted
Forgejo, not on GitHub (not even as a private repository).

## Roadmap

- syncing deletions (files removed locally removed from the store)
- encryption at rest (e.g. age)
- per-repo include/skip overrides
