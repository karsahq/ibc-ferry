package main

import (
	"flag"
	"fmt"
	"os"
	"time"
)

func cmdPrune(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	olderThan := fs.Duration("older-than", 7*24*time.Hour, "delete terminal packets last updated before this age")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("prune takes no positional arguments")
	}
	if *olderThan <= 0 {
		return fmt.Errorf("older-than must be positive")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()

	count, err := store.PruneTerminalPackets(time.Now().UTC().Add(-*olderThan))
	if err != nil {
		return err
	}
	fmt.Printf("pruned %d terminal packet(s) older than %s\n", count, *olderThan)
	return nil
}

func pruneTerminal(store *LinkStore, stats *relayStats, retention time.Duration) error {
	if retention <= 0 {
		return nil
	}
	count, err := store.PruneTerminalPackets(time.Now().UTC().Add(-retention))
	if err != nil {
		return err
	}
	stats.addPruned(count)
	return nil
}
