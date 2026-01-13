package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"es-tmnt/internal/config"
	"es-tmnt/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	proxyHandler, err := proxy.New(cfg)
	if err != nil {
		log.Fatalf("init proxy: %v", err)
	}

	mainServer := &http.Server{
		Addr:         ":" + strconv.Itoa(cfg.Ports.HTTP),
		Handler:      proxyHandler,
		ReadTimeout:  cfg.ReadTimeout,
		WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:  cfg.IdleTimeout,
	}

	adminServer := setupAdminServer(cfg)

	go func() {
		log.Printf("proxy listening on %s", mainServer.Addr)
		if err := mainServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("main server: %v", err)
		}
	}()

	if adminServer != nil {
		go func() {
			log.Printf("admin listening on %s", adminServer.Addr)
			if err := adminServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Fatalf("admin server: %v", err)
			}
		}()
	}

	shutdownOnSignal(mainServer, adminServer)
}

func setupAdminServer(cfg config.Config) *http.Server {
	if cfg.Ports.Admin <= 0 {
		return nil
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	return &http.Server{
		Addr:    ":" + strconv.Itoa(cfg.Ports.Admin),
		Handler: mux,
	}
}

func shutdownOnSignal(mainServer *http.Server, adminServer *http.Server) {
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := mainServer.Shutdown(ctx); err != nil {
		log.Printf("main server shutdown error: %v", err)
	}
	if adminServer != nil {
		if err := adminServer.Shutdown(ctx); err != nil {
			log.Printf("admin server shutdown error: %v", err)
		}
	}
}
