# gitpriv

Sync the **private files of your git repos** between your machines — without the junk.

Public repos can't hold secrets or personal notes. But you still want your
`.env` files, `me.md` notes, and `.codex/` or `.claude/` state on every machine.
gitpriv solves that by treating the git-ignored files of a repo as a first-class
payload that lives in a **private store**: a separate git repository that only
you clone.

## How it works

```
repo/                  public (GitHub)
  .env                 ignored by .gitignore
  me.md                ignored by .gitignore
  node_modules/        ignored, but JUNK — never synced
  src/                 tracked, public

private-store/         your own git repo (clone to each machine)
  repo/.env
  repo/me.md
```

- `gitpriv push` collects every git-**ignored** file in a repo
  (`git ls-files -io --exclude-standard`), filters out junk with a skip list,
  copies them into the store, and commits.
- `gitpriv pull` restores store files that are missing locally. It never
  overwrites files that already exist.
- **No junk:** `node_modules/`, `build/`, `dist/`, `target/`, `vendor/`,
  `__pycache__/`, `.cache/`, `logs/`, `*.log`, `*.tmp`, `*.swp`, core dumps and
  `.DS_Store` are skipped by default. Extend the skip list in your config.
- **No public leakage:** only git-ignored files are synced, so tracked files
  can't accidentally travel through the private store.

## Install

```sh
go build -o gitpriv .
```

Requires `git` on PATH. Written in Go, stdlib only — one static binary,
cross-compiles to any machine (`GOOS=darwin go build` for a Mac).

## Setup

```sh
git init private-store              # or: git clone <your-private-remote>
gitpriv init --store /path/to/private-store
```

The store is a normal git repo. Clone it to each machine and run `gitpriv init`
there too. Push the store's commits wherever you keep private things (your own
server, a private GitHub repo, a USB drive — your call).

## Usage

```sh
gitpriv scan            # what would sync (from the current directory)
gitpriv push            # sync gitignored files into the store
gitpriv pull            # restore missing files on this machine
gitpriv push ~/src/one ~/src/two   # several repos at once
```

## Config

`~/.config/gitpriv/config.json`:

```json
{
  "store": "/path/to/private-store",
  "skip": [
    "**/node_modules/**",
    "**/extra-secret-dir/**"
  ]
}
```

When `skip` is unset, a sensible default list is used.

## Security notes

- Files are stored **plaintext**. Treat the store like you treat your SSH keys:
  keep it on your own infra or a private remote, never a public one.
- File permissions are set to `0600` in the store; restore them however you
  like on each machine.

## Roadmap

- per-repo skip/include config
- syncing deletions (removed files removed from the store)
- encryption at rest (e.g. age)
- dry-run for `pull`
