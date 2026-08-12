package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
	"github.com/kraftbj/mailreaper/internal/scanner"
	"github.com/kraftbj/mailreaper/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "mailreaper.db", "path to SQLite database file")
	catchup := flag.Bool("catchup", false, "scan all messages on first run (not just last 7 days)")
	backfill := flag.Bool("backfill", false, "re-evaluate every message in Expired and triage folders against current rules; rescues mis-routed messages and pulls newly-extractable deadlines forward to Expired")
	flag.Parse()

	// Load config.
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Printf("error loading config from %q: %v", *configPath, err)
		os.Exit(1)
	}

	// Open database.
	database, err := db.Open(*dbPath)
	if err != nil {
		log.Printf("error opening database %q: %v", *dbPath, err)
		os.Exit(1)
	}
	defer database.Close()

	// Seed default rules.
	if err := database.SeedDefaults(rules.DefaultRules); err != nil {
		log.Printf("error seeding default rules: %v", err)
		os.Exit(1)
	}

	// Upsert each configured account.
	for _, acct := range cfg.Accounts {
		if err := database.UpsertAccount(acct.Username, acct.Name); err != nil {
			log.Printf("error upserting account %q: %v", acct.Username, err)
		}
	}

	// Set up signal handling with context cancellation.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %v, shutting down", sig)
		cancel()
	}()

	scan := scanner.New(database, cfg)
	if *catchup {
		log.Println("catchup mode: first scan will process all messages")
		scan.LookbackDays = 0
	}

	if *backfill {
		log.Println("backfill mode: re-evaluating Expired and all triage folders before normal scan loop starts")
		runBackfill(ctx, scan, cfg)
	}

	// Start scan loop goroutine — runs immediately, then on interval.
	go func() {
		interval := time.Duration(cfg.Scan.IntervalMinutes) * time.Minute
		runFullScan(ctx, scan, cfg)
		// After first scan, revert to normal 7-day lookback
		scan.LookbackDays = 7
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runFullScan(ctx, scan, cfg)
			}
		}
	}()

	// Start grace cleanup loop goroutine (every 6 hours).
	go func() {
		ticker := time.NewTicker(6 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				log.Printf("grace cleanup: running (not yet implemented)")
			}
		}
	}()

	// Start web server (blocks until context cancelled).
	srv := server.NewServer(database, cfg.Server.BindAddr, cfg.Server.Port)
	srv.OnScanRequested = func() {
		runFullScan(ctx, scan, cfg)
	}
	srv.OnRescanRequested = func() {
		prev := scan.LookbackDays
		scan.LookbackDays = 0
		runFullScan(ctx, scan, cfg)
		scan.LookbackDays = prev
	}
	log.Printf("starting web server on %s:%d", cfg.Server.BindAddr, cfg.Server.Port)
	if err := srv.Start(ctx); err != nil {
		log.Printf("web server error: %v", err)
		os.Exit(1)
	}
	log.Println("shutdown complete")
}

// runFullScan connects to each configured IMAP account, runs ScanAccount and
// DetectFeedback, then closes the connection.
func runFullScan(ctx context.Context, scan *scanner.Scanner, cfg *config.Config) {
	log.Printf("scan: starting full scan across %d account(s)", len(cfg.Accounts))

	for _, acct := range cfg.Accounts {
		select {
		case <-ctx.Done():
			return
		default:
		}

		client, err := imappkg.Connect(acct)
		if err != nil {
			log.Printf("scan: connect to account %q: %v", acct.Name, err)
			continue
		}

		folders := acct.Folders.Scan
		if len(folders) == 0 {
			folders = []string{"INBOX"}
		}

		if err := scan.ScanAccount(ctx, client, acct.Username, folders); err != nil {
			log.Printf("scan: ScanAccount for %q: %v", acct.Name, err)
		}

		inbox := "INBOX"
		if len(folders) > 0 {
			inbox = folders[0]
		}

		if err := scan.DetectFeedback(client, acct.Username, inbox); err != nil {
			log.Printf("scan: DetectFeedback for %q: %v", acct.Name, err)
		}

		if err := scan.DetectManualClassifications(client, acct.Username); err != nil {
			log.Printf("scan: DetectManualClassifications for %q: %v", acct.Name, err)
		}

		if err := scan.SweepDeferredExpiries(client, acct.Username); err != nil {
			log.Printf("scan: SweepDeferredExpiries for %q: %v", acct.Name, err)
		}

		if err := client.Close(); err != nil {
			log.Printf("scan: close connection for %q: %v", acct.Name, err)
		}
	}

	// Distill manual classification patterns into static rules.
	if err := scan.DistillRules(); err != nil {
		log.Printf("scan: DistillRules: %v", err)
	}

	log.Printf("scan: full scan complete")
}

// runBackfill walks every Expired and triage folder for each account,
// re-evaluating each message against current rules and moving misplaced
// messages. Triggered by --backfill on startup.
func runBackfill(ctx context.Context, scan *scanner.Scanner, cfg *config.Config) {
	log.Printf("backfill: starting across %d account(s)", len(cfg.Accounts))

	for _, acct := range cfg.Accounts {
		select {
		case <-ctx.Done():
			return
		default:
		}

		client, err := imappkg.Connect(acct)
		if err != nil {
			log.Printf("backfill: connect to account %q: %v", acct.Name, err)
			continue
		}

		rescue := "INBOX"
		if folders := acct.Folders.Scan; len(folders) > 0 {
			rescue = folders[0]
		}

		stats, err := scan.BackfillFolders(ctx, client, acct.Username, rescue)
		if err != nil {
			log.Printf("backfill: BackfillFolders for %q: %v", acct.Name, err)
		} else if stats != nil {
			log.Printf("backfill: account %q — folders=%d inspected=%d moved=%d kept=%d skipped=%d errored=%d",
				acct.Name, stats.Folders, stats.Inspected, stats.Moved, stats.Kept, stats.Skipped, stats.Errored)
		}

		if err := client.Close(); err != nil {
			log.Printf("backfill: close connection for %q: %v", acct.Name, err)
		}
	}

	log.Printf("backfill: complete")
}
