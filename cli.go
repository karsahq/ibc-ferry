package main

import (
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
)

func cmdChains(cfg Config, args []string) error {
	fs := flag.NewFlagSet("chains", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("chains takes no arguments")
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tCHAIN ID\tNODE")
	for _, chain := range cfg.namedChains() {
		fmt.Fprintf(w, "%s\t%s\t%s\n", chain.Name, chain.Config.ChainID, chain.Config.Node)
	}
	return w.Flush()
}

func cmdLinks(cfg Config, statePath string, args []string) error {
	fs := flag.NewFlagSet("links", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	chainRef := fs.String("chain", "", "only show links that include this chain name or chain ID")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("links takes no positional arguments")
	}

	chainID := ""
	if *chainRef != "" {
		chain, err := cfg.chainByRef(*chainRef)
		if err != nil {
			return err
		}
		chainID = chain.Config.ChainID
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	links, err := store.List()
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "LINK ID\tSTATUS\tCREATED")
	count := 0
	for _, link := range links {
		if chainID != "" && link.ChainA != chainID && link.ChainB != chainID {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n", link.ID, formatStatus(link.Status), link.CreatedAt.Format("2006-01-02T15:04:05Z"))
		count++
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if count == 0 {
		fmt.Fprintln(os.Stderr, "no links")
	}
	return nil
}

func cmdLink(statePath string, args []string) error {
	fs := flag.NewFlagSet("link", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("link requires exactly one link ID")
	}

	store, err := openLinkStore(statePath)
	if err != nil {
		return err
	}
	defer store.Close()
	link, err := store.Get(fs.Arg(0))
	if err != nil {
		return err
	}
	fmt.Printf("id:        %s\n", link.ID)
	fmt.Printf("status:    %s\n", formatStatus(link.Status))
	fmt.Printf("created:   %s\n", link.CreatedAt.Format("2006-01-02T15:04:05Z"))
	fmt.Printf("chain-a:   %s\n", link.ChainA)
	fmt.Printf("client-a:  %s\n", link.ClientA)
	fmt.Printf("chain-b:   %s\n", link.ChainB)
	fmt.Printf("client-b:  %s\n", link.ClientB)
	return nil
}
