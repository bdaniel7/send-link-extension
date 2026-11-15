package main

import (
	"log"
	"net/http"
	"os"
	"time"
)

const maxAgeMs = 48 * 60 * 60 * 1000 // drop links older than 48h
const heartbeatInterval = 20 * time.Second

func main() {
	port := getEnv("PORT", "8787")
	token := os.Getenv("API_TOKEN")
	if token == "" {
		log.Fatal("Set the API_TOKEN environment variable before starting (see .env.example).")
	}

	store := NewStore("links.json")
	hub := NewHub()
	go hub.heartbeatLoop(heartbeatInterval)

	h := &Handler{store: store, hub: hub, token: token, maxAgeMs: maxAgeMs}

	mux := http.NewServeMux()
	mux.HandleFunc("/links", requireBearer(token, h.handleLinks))
	mux.HandleFunc("/devices", requireBearer(token, h.handleDevices))
	mux.HandleFunc("/stream", h.handleStream) // auth via query param, checked inside

	log.Printf("Link relay listening on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
