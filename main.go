// gitpriv syncs gitignored private files of repos into a private store.
package main

import (
	"fmt"
	"os"
)

func usage() {
	fmt.Fprintln(os.Stderr, `gitpriv - sync private (gitignored) files between your machines

Usage:
  gitpriv init --store <path>       create config at ~/.config/gitpriv/config.json
  gitpriv scan [repo-dir...]        list what would be synced (default: cwd)
  gitpriv push [repo-dir...]        copy gitignored files into the store and commit
  gitpriv pull [repo-dir...]        restore store files that are missing locally

The store is a normal git repository (local clone or bare). Clone it to each
machine and point the config at it. Only files ignored by the repo's own git
rules are synced, and junk (node_modules, build/, caches, logs) is filtered
out via the skip list.`)
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
