package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"internal/storage"
)

func main() {
	host := flag.String("host", "localhost", "Host address for the server")
	port := flag.Int("port", 8090, "Port number for the server")
	flag.Parse()

	// Validate arguments
	if *port <= 0 {
		log.Fatal("Error: Port number must be positive")
	}

	if flag.NArg() < 1 {
		fmt.Println("Usage: storage [OPTIONS] <baseDir>")
		fmt.Println("Error: Base directory argument is required")
		os.Exit(1)
	}
	baseDir := flag.Arg(0)

	// Create server instance
	server, err := storage.NewServer(baseDir)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	// Create listener
	addr := fmt.Sprintf("%s:%d", *host, *port)
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	// Handle signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("Received signal %v, shutting down...", sig)
		server.Stop()
	}()

	// Start server
	log.Printf("Starting storage server on %s", addr)
	log.Printf("Base directory: %s", baseDir)
	if err := server.Start(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
