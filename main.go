// gitpriv syncs the private, git-ignored files of repos (.env files, keys,
// local databases) between machines through a private git "store".
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `gitpriv - sync private (git-ignored) files between your machines

Usage:
  gitpriv init --store <path>          write ~/.config/gitpriv/config.json
  gitpriv scan   [repo...]             list what would be synced and what is skipped
  gitpriv status [repo...]             compare local private files with the store
  gitpriv push   [-n] [--force] [repo...]
                                       copy private files into the store and commit
  gitpriv pull   [-n] [repo...]        restore store files that are missing locally

repo defaults to the current directory; any path inside a repo works.
-n/--dry-run prints exactly what would change and changes nothing.

A file is synced only if it is git-ignored AND matches an include pattern
(.env*, keys, certs, secrets/, local databases ...) AND matches no skip
pattern AND is a regular file under max_file_bytes (default 5 MiB).
pull never overwrites an existing file and refuses to restore a file that
the target repo does not ignore. Set GITPRIV_CONFIG to use another config.`)
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "init":
		err = cmdInit(args)
	case "scan":
		err = cmdScan(args)
	case "status", "st":
		err = cmdStatus(args)
	case "push":
		err = cmdPush(args)
	case "pull":
		err = cmdPull(args)
	case "help", "-h", "--help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "gitpriv: unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "gitpriv:", err)
		os.Exit(1)
	}
}
