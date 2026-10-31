package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	configPath := flag.String("config", "", "path to the TOML or JSON config file (default ~/.config/ibc-ferry/config.toml)")
	statePath := flag.String("state", "", "path to the SQLite state file (default ~/.local/state/ibc-ferry/state.db)")
	flag.Usage = usage
	flag.Parse()

	path := *configPath
	if path == "" {
		var err error
		path, err = defaultConfigPath()
		if err != nil {
			log.Fatalf("config: %v", err)
		}
	}

	cfg, err := loadConfig(path)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	args := flag.Args()
	if len(args) == 0 {
		if err := run(cfg); err != nil {
			log.Fatalf("relay: %v", err)
		}
		return
	}

	switch args[0] {
	case "chains":
		err = cmdChains(cfg, args[1:])
	case "links":
		err = cmdLinks(cfg, *statePath, args[1:])
	case "link":
		err = cmdLink(*statePath, args[1:])
	case "connect":
		err = cmdConnect(cfg, *statePath, args[1:])
	case "pair":
		err = cmdPair(cfg, *statePath, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "ibc-ferry: unknown command %q\n\n", args[0])
		flag.Usage()
		os.Exit(2)
	}
	if err != nil {
		log.Fatalf("%s: %v", args[0], err)
	}
}

func usage() {
	out := os.Stderr
	fmt.Fprintln(out, "Usage:")
	fmt.Fprintln(out, "  ibc-ferry [--config <file>] [--state <file>] <command> [arguments]")
	fmt.Fprintln(out, "  ibc-ferry [--config <file>]             # run one legacy transfer")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  chains                       list configured chains")
	fmt.Fprintln(out, "  links [--chain <chain-id>]   list stored links")
	fmt.Fprintln(out, "  link <link-id>               show one stored link")
	fmt.Fprintln(out, "  connect <chain-a> <chain-b>  create or reuse clients and pair them")
	fmt.Fprintln(out, "  pair <chain-a> <client-a> <chain-b> <client-b>")
	fmt.Fprintln(out, "                               verify and persist an existing client pair")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Global flags:")
	flag.PrintDefaults()
}
