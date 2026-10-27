// Command fraudlens runs the fraud detection server and dashboard.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // embed zone data so Asia/Kolkata works in minimal containers

	"github.com/vansh7877/vansh-repo/internal/engine"
	"github.com/vansh7877/vansh-repo/internal/model"
	"github.com/vansh7877/vansh-repo/internal/server"
	"github.com/vansh7877/vansh-repo/internal/store"
)

func main() {
	addr := flag.String("addr", envOr("FRAUD_ADDR", ":8080"), "listen address")
	dataDir := flag.String("data", envOr("FRAUD_DATA_DIR", "./data"), "directory for the transaction log (empty = memory only)")
	cfgPath := flag.String("config", envOr("FRAUD_CONFIG", ""), "optional JSON file overriding rule thresholds")
	fsync := flag.Bool("fsync", false, "fsync the log on every write (safer, slower)")
	retention := flag.Int("retention-days", 0, "drop transactions older than N days (0 = keep all)")
	printConfig := flag.Bool("print-config", false, "print the effective configuration as JSON and exit")
	flag.Parse()

	cfg, err := engine.LoadConfig(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if *printConfig {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cfg)
		return
	}
	eng, err := engine.New(cfg)
	if err != nil {
		log.Fatal(err)
	}

	var lg *store.Log
	if *dataDir != "" {
		if lg, err = store.Open(*dataDir, *fsync); err != nil {
			log.Fatal(err)
		}
		defer lg.Close()
		start := time.Now()
		n, err := lg.Replay(func(b []model.Transaction) { eng.Ingest(b) })
		if err != nil {
			log.Fatalf("replay: %v", err)
		}
		if n > 0 {
			log.Printf("restored %d transactions in %s", n, time.Since(start).Round(time.Millisecond))
		}
	}

	if *retention > 0 {
		compact := func() {
			cutoff := time.Now().AddDate(0, 0, -*retention)
			if kept := eng.Compact(cutoff); kept != nil {
				if err := lg.Rewrite(kept); err != nil {
					log.Printf("rewrite log: %v", err)
				}
				log.Printf("retention: kept %d transactions newer than %s", len(kept), cutoff.Format("2006-01-02"))
			}
		}
		compact()
		go func() {
			for range time.Tick(6 * time.Hour) {
				compact()
			}
		}()
	}

	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.New(eng, lg, server.Options{APIKey: os.Getenv("FRAUD_API_KEY")}),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("FraudLens listening on %s (dashboard: http://localhost%s)", *addr, *addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
