package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/kraftbj/mailreaper/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("error loading config from %q: %v", *configPath, err)
		os.Exit(1)
	}

	fmt.Printf("mailreaper: loaded config with %d account(s)\n", len(cfg.Accounts))
}
