package main

import (
	"flag"
	"fmt"
	"log"
	"os"
)

func main() {
	configPath := flag.String("config", "", "path to the JSON config file")
	flag.Parse()

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "ibc-ferry: --config is required")
		flag.Usage()
		os.Exit(2)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	if err := run(cfg); err != nil {
		log.Fatalf("relay: %v", err)
	}
}
