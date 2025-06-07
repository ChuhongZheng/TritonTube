package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"tritontube/internal/proto"
	"tritontube/internal/storage"

	"google.golang.org/grpc"
)

func main() {
	// Parse command line flags
	port := flag.Int("port", 8080, "Port to listen on")
	flag.Parse()

	// Get storage directory from positional argument
	if flag.NArg() != 1 {
		log.Fatal("Usage: ./storage -port PORT STORAGE_DIR")
	}
	rootDir := flag.Arg(0)

	// Create root directory
	absRootDir, err := filepath.Abs(rootDir)
	if err != nil {
		log.Fatalf("Failed to get absolute path for root directory: %v", err)
	}

	// Create storage directory if it doesn't exist
	if err := os.MkdirAll(absRootDir, 0755); err != nil {
		log.Fatalf("Failed to create storage directory: %v", err)
	}

	// Create storage server
	server, err := storage.NewServer(absRootDir)
	if err != nil {
		log.Fatalf("Failed to create storage server: %v", err)
	}

	// Create gRPC server with increased message size limit
	grpcServer := grpc.NewServer(
		grpc.MaxRecvMsgSize(32*1024*1024), // 32MB max receive size
		grpc.MaxSendMsgSize(32*1024*1024), // 32MB max send size
	)
	proto.RegisterStorageServiceServer(grpcServer, server)

	// Start listening
	lis, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	log.Printf("Storage server listening on port %d, storing videos in %s", *port, absRootDir)
	if err := grpcServer.Serve(lis); err != nil {
		log.Fatalf("Failed to serve: %v", err)
	}
}
