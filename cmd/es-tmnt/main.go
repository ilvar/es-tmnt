package main

import (
	"fmt"
	"log"
	"net/http"

	"es-tmnt/internal/config"
	"es-tmnt/internal/proxy"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config error: %v", err)
	}
	service, err := proxy.New(cfg)
	if err != nil {
		log.Fatalf("proxy init error: %v", err)
	}
	address := fmt.Sprintf(":%d", cfg.Ports.HTTP)
	log.Printf("starting proxy on %s", address)
	if err := http.ListenAndServe(address, service); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
