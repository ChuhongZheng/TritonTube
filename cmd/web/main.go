package main

import (
	"flag"
	"fmt"
	"net"
	"strings"
	"tritontube/internal/web"
)

// printUsage prints the usage information for the application
func printUsage() {
	fmt.Println("Usage: ./program [OPTIONS] METADATA_TYPE METADATA_OPTIONS CONTENT_TYPE CONTENT_OPTIONS")
	fmt.Println()
	fmt.Println("Arguments:")
	fmt.Println("  METADATA_TYPE         Metadata service type (sqlite, etcd)")
	fmt.Println("  METADATA_OPTIONS      Options for metadata service (e.g., db path)")
	fmt.Println("  CONTENT_TYPE          Content service type (fs, nw)")
	fmt.Println("  CONTENT_OPTIONS       Options for content service:")
	fmt.Println("                        - For fs: base directory path")
	fmt.Println("                        - For nw: comma-separated list of storage node addresses")
	fmt.Println()
	fmt.Println("Options:")
	flag.PrintDefaults()
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  ./program sqlite db.db fs /path/to/videos")
	fmt.Println("  ./program sqlite db.db nw localhost:8080,localhost:8081")
}

func main() {
	// Define flags
	port := flag.Int("port", 8080, "Port number for the web server")
	host := flag.String("host", "localhost", "Host address for the web server")

	// Set custom usage message
	flag.Usage = printUsage

	// Parse flags
	flag.Parse()

	// Check if the correct number of positional arguments is provided
	if len(flag.Args()) != 4 {
		fmt.Println("Error: Incorrect number of arguments")
		printUsage()
		return
	}

	// Parse positional arguments
	metadataServiceType := flag.Arg(0)
	metadataServiceOptions := flag.Arg(1)
	contentServiceType := flag.Arg(2)
	contentServiceOptions := flag.Arg(3)

	// Validate port number
	if *port <= 0 {
		fmt.Println("Error: Invalid port number:", *port)
		printUsage()
		return
	}

	// Construct metadata service
	var metadataService web.VideoMetadataService
	fmt.Println("Creating metadata service of type", metadataServiceType, "with options", metadataServiceOptions)
	switch metadataServiceType {
	case "sqlite":
		var err error
		metadataService, err = web.NewSQLiteVideoMetadataService(metadataServiceOptions)
		if err != nil {
			fmt.Println("Error creating SQLite metadata service:", err)
			return
		}
	default:
		fmt.Println("Error: Unsupported metadata service type:", metadataServiceType)
		printUsage()
		return
	}

	// Construct content service
	var contentService web.VideoContentService
	fmt.Println("Creating content service of type", contentServiceType, "with options", contentServiceOptions)
	switch contentServiceType {
	case "fs":
		var err error
		contentService, err = web.NewFSVideoContentService(contentServiceOptions)
		if err != nil {
			fmt.Println("Error creating filesystem content service:", err)
			return
		}
	case "nw":
		// Create network content service
		nwService := web.NewNetworkContentService()

		// Parse addresses
		addresses := strings.Split(contentServiceOptions, ",")
		fmt.Printf("Raw addresses string: %q\n", contentServiceOptions)
		fmt.Printf("Parsed addresses (count: %d): %v\n", len(addresses), addresses)
		
		if len(addresses) < 2 {
			fmt.Println("Error: At least two addresses required (admin server and one storage node)")
			printUsage()
			return
		}

		// First address is the admin server address, skip it
		adminAddr := strings.TrimSpace(addresses[0])
		fmt.Printf("gRPC admin service will be available at: %s\n", adminAddr)

		// Add storage nodes (skip the first address which is the admin server)
		fmt.Println("\nProcessing storage nodes:")
		storageNodes := addresses[1:]
		fmt.Printf("Found %d storage nodes to process\n", len(storageNodes))
		
		for i, node := range storageNodes {
			node = strings.TrimSpace(node)
			fmt.Printf("\nProcessing node %d/%d: %q\n", i+1, len(storageNodes), node)
			
			if node == "" {
				fmt.Printf("  Skipping empty address at index %d\n", i+1)
				continue
			}
			
			fmt.Printf("  Attempting to connect to: %s\n", node)
			migratedCount, err := nwService.AddNode(node)
			if err != nil {
				fmt.Printf("  Error adding storage node %s: %v\n", node, err)
				return
			}
			if migratedCount > 0 {
				fmt.Printf("  Migrated %d files when adding node %s\n", migratedCount, node)
			}
		}

		// List all added nodes
		nodes := nwService.ListNodes()
		fmt.Printf("\nFinal node status - Successfully connected to %d storage nodes:\n", len(nodes))
		for _, node := range nodes {
			fmt.Printf("  - %s\n", node)
		}

		contentService = nwService
	default:
		fmt.Println("Error: Unsupported content service type:", contentServiceType)
		printUsage()
		return
	}

	// Start the server
	server := web.NewServer(metadataService, contentService)
	server.Addr = *host
	server.Port = *port

	listenAddr := fmt.Sprintf("%s:%d", *host, *port)
	lis, err := net.Listen("tcp", listenAddr)
	if err != nil {
		fmt.Println("Error starting listener:", err)
		return
	}
	defer lis.Close()

	fmt.Printf("Starting web server on %s (gRPC server will be on %s:%d)\n", listenAddr, *host, *port+1)
	err = server.Start(lis)
	if err != nil {
		fmt.Println("Error starting server:", err)
		return
	}
}
