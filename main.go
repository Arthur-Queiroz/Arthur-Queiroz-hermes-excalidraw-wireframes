package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	address := envOrDefault("WIREFRAME_ADDRESS", ":8080")
	handler, err := NewServer(Config{
		DataDir:       envOrDefault("WIREFRAME_DATA_DIR", "/data"),
		APIToken:      os.Getenv("WIREFRAME_API_TOKEN"),
		PublicBaseURL: envOrDefault("WIREFRAME_PUBLIC_URL", "https://excalidraw.devarthur.com.br"),
		StaticDir:     envOrDefault("EXCALIDRAW_STATIC_DIR", "/app/excalidraw"),
	})
	if err != nil {
		log.Fatal(err)
	}

	server := &http.Server{
		Addr:              address,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	stopped := make(chan os.Signal, 1)
	signal.Notify(stopped, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		log.Printf("wireframe server listening on %s", address)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("serve: %v", err)
		}
	}()

	<-stopped
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}

func envOrDefault(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
