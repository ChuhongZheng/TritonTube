// Lab 8: Implement a network video content service (client using consistent hashing)

package web

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"

	"tritontube/internal/proto"
)

// hashStringToUint64 computes the hash of a string using SHA-256
func hashStringToUint64(s string) uint64 {
	sum := sha256.Sum256([]byte(s))
	return binary.BigEndian.Uint64(sum[:8])
}

// nodeInfo represents a node in the consistent hash ring
type nodeInfo struct {
	address string
	hash    uint64
}

// NetworkContentService implements the network video content service
type NetworkContentService struct {
	ctx     context.Context
	cancel  context.CancelFunc
	clients map[string]proto.StorageServiceClient
	conns   map[string]*grpc.ClientConn
	ring    []nodeInfo
	mu      sync.RWMutex
}

// NewNetworkContentService creates a new network content service
func NewNetworkContentService() *NetworkContentService {
	ctx, cancel := context.WithCancel(context.Background())
	return &NetworkContentService{
		ctx:     ctx,
		cancel:  cancel,
		clients: make(map[string]proto.StorageServiceClient),
		conns:   make(map[string]*grpc.ClientConn),
		ring:    make([]nodeInfo, 0),
	}
}

// findNodeForKey finds the node that should store the given key
func (s *NetworkContentService) findNodeForKey(key string) string {
	if len(s.ring) == 0 {
		return ""
	}

	keyHash := hashStringToUint64(key)

	// Binary search to find the first node with hash > keyHash
	idx := sort.Search(len(s.ring), func(i int) bool {
		return s.ring[i].hash > keyHash
	})

	// If no node has hash > keyHash, wrap around to the first node
	if idx == len(s.ring) {
		idx = 0
	}

	return s.ring[idx].address
}

// updateRing updates the consistent hash ring with the current nodes
func (s *NetworkContentService) updateRing() {
	s.ring = make([]nodeInfo, 0, len(s.conns))
	for addr := range s.conns {
		s.ring = append(s.ring, nodeInfo{
			address: addr,
			hash:    hashStringToUint64(addr),
		})
	}
	// Sort nodes by hash value
	sort.Slice(s.ring, func(i, j int) bool {
		return s.ring[i].hash < s.ring[j].hash
	})
}

// getFilesToMigrateForAdd returns a map of files that need to be migrated when a new node is added
func (s *NetworkContentService) getFilesToMigrateForAdd(newNode string) map[string][]string {
	// Map of node address to list of files that should be on that node
	filesToMigrate := make(map[string][]string)

	// Get all video IDs from all existing nodes
	allVideoIds := make(map[string]struct{})
	for addr, client := range s.clients {
		if addr == newNode {
			continue // Skip the new node
		}

		listResp, err := client.List(s.ctx, &proto.ListRequest{})
		if err != nil {
			log.Printf("Warning: failed to list videos on node %s: %v", addr, err)
			continue
		}

		for _, videoId := range listResp.VideoIds {
			allVideoIds[videoId] = struct{}{}
		}
	}

	log.Printf("Found %d videos across existing nodes", len(allVideoIds))

	// For each video, find all its files and determine where they should go
	for videoId := range allVideoIds {
		log.Printf("Processing video %s for node addition", videoId)

		// Try to find manifest file on any existing node
		var manifestData []byte
		var manifestNode string
		for addr, client := range s.clients {
			if addr == newNode {
				continue
			}

			resp, err := client.Read(s.ctx, &proto.ReadRequest{
				VideoId:  videoId,
				Filename: "manifest.mpd",
			})
			if err == nil {
				manifestData = resp.Data
				manifestNode = addr
				log.Printf("Found manifest for video %s on node %s", videoId, addr)
				break
			}
		}

		if manifestData == nil {
			log.Printf("Warning: could not find manifest for video %s on any existing node", videoId)
			continue
		}

		// Parse manifest to get file list
		manifestStr := string(manifestData)
		var requiredFiles []string
		requiredFiles = append(requiredFiles, "manifest.mpd") // Always include manifest

		// Extract representation IDs from manifest
		repIDs := make(map[string]struct{})
		repPattern := regexp.MustCompile(`id="([^"]+)"`)
		repMatches := repPattern.FindAllStringSubmatch(manifestStr, -1)
		for _, match := range repMatches {
			if len(match) > 1 {
				repIDs[match[1]] = struct{}{}
			}
		}

		if len(repIDs) == 0 {
			// If no representation IDs found, try default values
			repIDs["0"] = struct{}{}
			repIDs["1"] = struct{}{}
		}

		log.Printf("Found representation IDs: %v", repIDs)

		// For each representation ID, try to find init and chunk files
		for repID := range repIDs {
			// Try init file
			initFilename := fmt.Sprintf("%s_init-%s.m4s", videoId, repID)
			requiredFiles = append(requiredFiles, initFilename)

			// Try to find chunk files
			// First try to find them in the manifest
			chunkPattern := regexp.MustCompile(fmt.Sprintf(`%s_chunk-%s-(\d+)\.m4s`, videoId, repID))
			chunkMatches := chunkPattern.FindAllStringSubmatch(manifestStr, -1)
			for _, match := range chunkMatches {
				if len(match) > 1 {
					num, _ := strconv.Atoi(match[1])
					chunkFilename := fmt.Sprintf("%s_chunk-%s-%05d.m4s", videoId, repID, num)
					requiredFiles = append(requiredFiles, chunkFilename)
				}
			}

			// If no chunks found in manifest, try to find them directly
			if len(chunkMatches) == 0 {
				consecutiveMisses := 0
				for i := 0; consecutiveMisses < 3; i++ {
					chunkFilename := fmt.Sprintf("%s_chunk-%s-%05d.m4s", videoId, repID, i)
					// Try to read the chunk file from the manifest node
					_, err := s.clients[manifestNode].Read(s.ctx, &proto.ReadRequest{
						VideoId:  videoId,
						Filename: chunkFilename,
					})
					if err == nil {
						requiredFiles = append(requiredFiles, chunkFilename)
						consecutiveMisses = 0
					} else {
						consecutiveMisses++
					}
				}
			}
		}

		log.Printf("Found %d files for video %s: %v", len(requiredFiles), videoId, requiredFiles)

		// For each file, determine if it needs to be migrated to the new node
		for _, filename := range requiredFiles {
			key := fmt.Sprintf("%s/%s", videoId, filename)
			targetNode := s.findNodeForKey(key)

			// If the file should be on the new node, add it to the migration list
			if targetNode == newNode {
				log.Printf("File %s needs to be migrated to new node %s", key, newNode)
				filesToMigrate[newNode] = append(filesToMigrate[newNode], key)
			} else {
				log.Printf("File %s should stay on node %s", key, targetNode)
			}
		}
	}

	return filesToMigrate
}

// getFilesToMigrateForRemove returns a map of files that need to be migrated when a node is removed
func (s *NetworkContentService) getFilesToMigrateForRemove(oldNode string) map[string][]string {
	// Map of node address to list of files that should be on that node
	filesToMigrate := make(map[string][]string)

	// Get all videos from the node being removed
	var videoIds []string
	oldClient := s.clients[oldNode]
	if oldClient != nil {
		listResp, err := oldClient.List(s.ctx, &proto.ListRequest{})
		if err != nil {
			log.Printf("Warning: failed to list videos on node %s: %v", oldNode, err)
			return filesToMigrate
		}
		videoIds = listResp.VideoIds
	}

	log.Printf("Found %d videos on node %s to be removed", len(videoIds), oldNode)

	// For each video on the node being removed
	for _, videoId := range videoIds {
		log.Printf("Processing video %s for node removal", videoId)

		// First, try to list all files for this video
		var filesOnNode []string
		log.Printf("Listing all files on node %s for video %s", oldNode, videoId)
		listResp, err := oldClient.List(s.ctx, &proto.ListRequest{})
		if err == nil {
			log.Printf("Found %d total files on node %s", len(listResp.VideoIds), oldNode)
			log.Printf("Full list of files on node %s: %v", oldNode, listResp.VideoIds)

			// Create a pattern to match all files for this video
			videoPattern := fmt.Sprintf("%s_*.m4s", videoId)
			log.Printf("Using pattern %s to match video files", videoPattern)
			for _, filename := range listResp.VideoIds {
				// Skip the video ID itself
				if filename == videoId {
					continue
				}
				// Check if it's a manifest file
				if filename == "manifest.mpd" {
					if !contains(filesOnNode, "manifest.mpd") {
						filesOnNode = append(filesOnNode, "manifest.mpd")
						log.Printf("Found manifest file through listing")
					}
					continue
				}
				// Check if it's a video file
				if matched, _ := filepath.Match(videoPattern, filename); matched {
					if !contains(filesOnNode, filename) {
						filesOnNode = append(filesOnNode, filename)
						log.Printf("Found file through listing: %s", filename)
					}
				}
			}
		}

		// Try to read manifest
		log.Printf("Attempting to read manifest for video %s on node %s", videoId, oldNode)
		manifestResp, err := oldClient.Read(s.ctx, &proto.ReadRequest{
			VideoId:  videoId,
			Filename: "manifest.mpd",
		})
		if err == nil {
			log.Printf("Successfully read manifest for video %s", videoId)
			if !contains(filesOnNode, "manifest.mpd") {
				filesOnNode = append(filesOnNode, "manifest.mpd")
			}
			manifestStr := string(manifestResp.Data)
			log.Printf("Manifest content: %s", manifestStr)

			// Extract representation IDs from manifest
			repIDs := make(map[string]struct{})
			repPattern := regexp.MustCompile(`id="([^"]+)"`)
			repMatches := repPattern.FindAllStringSubmatch(manifestStr, -1)
			for _, match := range repMatches {
				if len(match) > 1 {
					repIDs[match[1]] = struct{}{}
				}
			}

			if len(repIDs) == 0 {
				// If no representation IDs found, try default values
				log.Printf("No representation IDs found in manifest, using default values 0 and 1")
				repIDs["0"] = struct{}{}
				repIDs["1"] = struct{}{}
			}

			log.Printf("Found representation IDs: %v", repIDs)

			// Try to find init and chunk files
			for repID := range repIDs {
				log.Printf("Searching for files with representation ID %s", repID)
				// Try init file
				initFilename := fmt.Sprintf("%s_init-%s.m4s", videoId, repID)
				log.Printf("Trying to read init file: %s", initFilename)
				_, err := oldClient.Read(s.ctx, &proto.ReadRequest{
					VideoId:  videoId,
					Filename: initFilename,
				})
				if err == nil && !contains(filesOnNode, initFilename) {
					filesOnNode = append(filesOnNode, initFilename)
					log.Printf("Found init file: %s", initFilename)
				} else if err != nil {
					log.Printf("Could not find init file %s: %v", initFilename, err)
				}

				// Try chunk files with a larger range
				consecutiveMisses := 0
				for i := 0; consecutiveMisses < 10; i++ { // Increased consecutive misses threshold to 10
					chunkFilename := fmt.Sprintf("%s_chunk-%s-%05d.m4s", videoId, repID, i)
					log.Printf("Trying to read chunk file: %s", chunkFilename)
					_, err := oldClient.Read(s.ctx, &proto.ReadRequest{
						VideoId:  videoId,
						Filename: chunkFilename,
					})
					if err == nil && !contains(filesOnNode, chunkFilename) {
						filesOnNode = append(filesOnNode, chunkFilename)
						log.Printf("Found chunk file: %s", chunkFilename)
						consecutiveMisses = 0
					} else if err != nil {
						log.Printf("Could not find chunk file %s: %v", chunkFilename, err)
						consecutiveMisses++
					}
				}
			}
		} else {
			// If manifest not found, try to find files directly
			log.Printf("Manifest not found for video %s on node %s, trying to find files directly", videoId, oldNode)

			// Try to find init files for both representation IDs
			for _, repID := range []string{"0", "1"} {
				log.Printf("Searching for files with representation ID %s (direct search)", repID)
				initFilename := fmt.Sprintf("%s_init-%s.m4s", videoId, repID)
				log.Printf("Trying to read init file: %s", initFilename)
				_, err := oldClient.Read(s.ctx, &proto.ReadRequest{
					VideoId:  videoId,
					Filename: initFilename,
				})
				if err == nil && !contains(filesOnNode, initFilename) {
					filesOnNode = append(filesOnNode, initFilename)
					log.Printf("Found init file: %s", initFilename)
				} else if err != nil {
					log.Printf("Could not find init file %s: %v", initFilename, err)
				}

				// Try to find chunk files with a larger range
				consecutiveMisses := 0
				for i := 0; consecutiveMisses < 10; i++ { // Increased consecutive misses threshold to 10
					chunkFilename := fmt.Sprintf("%s_chunk-%s-%05d.m4s", videoId, repID, i)
					log.Printf("Trying to read chunk file: %s", chunkFilename)
					_, err := oldClient.Read(s.ctx, &proto.ReadRequest{
						VideoId:  videoId,
						Filename: chunkFilename,
					})
					if err == nil && !contains(filesOnNode, chunkFilename) {
						filesOnNode = append(filesOnNode, chunkFilename)
						log.Printf("Found chunk file: %s", chunkFilename)
						consecutiveMisses = 0
					} else if err != nil {
						log.Printf("Could not find chunk file %s: %v", chunkFilename, err)
						consecutiveMisses++
					}
				}
			}
		}

		log.Printf("Found %d files for video %s: %v", len(filesOnNode), videoId, filesOnNode)

		// For each file, determine where it should go after node removal
		for _, filename := range filesOnNode {
			key := fmt.Sprintf("%s/%s", videoId, filename)
			targetNode := s.findNodeForKey(key)

			if targetNode == "" {
				log.Printf("Skipping file %s as no target node found", key)
				continue
			}

			// If the file should go to a different node, add it to the migration list
			if targetNode != oldNode {
				log.Printf("File %s needs to be migrated from %s to %s", key, oldNode, targetNode)
				filesToMigrate[targetNode] = append(filesToMigrate[targetNode], key)
			} else {
				log.Printf("File %s should stay on node %s", key, targetNode)
			}
		}
	}

	return filesToMigrate
}

// Helper function to check if a slice contains a string
func contains(slice []string, str string) bool {
	for _, s := range slice {
		if s == str {
			return true
		}
	}
	return false
}

// migrateFiles migrates files between nodes based on the consistent hash ring
func (s *NetworkContentService) migrateFiles(filesToMigrate map[string][]string) (int32, error) {
	var totalMigrated int32

	for targetNode, files := range filesToMigrate {
		targetClient := s.clients[targetNode]
		if targetClient == nil {
			return 0, fmt.Errorf("target node %s not found", targetNode)
		}

		// Group files by video ID for better logging
		filesByVideo := make(map[string][]string)
		for _, fileKey := range files {
			videoId := filepath.Dir(fileKey)
			filename := filepath.Base(fileKey)
			filesByVideo[videoId] = append(filesByVideo[videoId], filename)
		}

		// Process each video's files
		for videoId, filenames := range filesByVideo {
			log.Printf("Migrating files for video %s to node %s", videoId, targetNode)

			// First, try to read all files from the source node
			var sourceNode string
			var fileDataMap = make(map[string][]byte)

			// Try each node as potential source
			for addr, client := range s.clients {
				if addr == targetNode {
					continue // Skip the target node
				}

				// Try to read all files from this node
				allFilesFound := true
				for _, filename := range filenames {
					resp, err := client.Read(s.ctx, &proto.ReadRequest{
						VideoId:  videoId,
						Filename: filename,
					})
					if err != nil {
						allFilesFound = false
						break
					}
					fileDataMap[filename] = resp.Data
				}

				if allFilesFound {
					sourceNode = addr
					break
				}
			}

			if sourceNode == "" {
				log.Printf("Warning: could not find all files for video %s on any single node", videoId)
				continue
			}

			// Write all files to target node
			for _, filename := range filenames {
				fileData := fileDataMap[filename]
				if fileData == nil {
					log.Printf("Warning: no data found for file %s/%s", videoId, filename)
					continue
				}

				// Write to target node
				_, err := targetClient.Write(s.ctx, &proto.WriteRequest{
					VideoId:  videoId,
					Filename: filename,
					Data:     fileData,
				})
				if err != nil {
					log.Printf("Warning: failed to write file %s/%s to target node %s: %v",
						videoId, filename, targetNode, err)
					continue
				}

				// Delete from source node
				_, err = s.clients[sourceNode].Delete(s.ctx, &proto.DeleteRequest{
					VideoId:  videoId,
					Filename: filename,
				})
				if err != nil {
					log.Printf("Warning: failed to delete file %s/%s from source node %s: %v",
						videoId, filename, sourceNode, err)
					continue
				}

				totalMigrated++
				log.Printf("Successfully migrated file %s/%s from %s to %s",
					videoId, filename, sourceNode, targetNode)
			}
		}
	}

	return totalMigrated, nil
}

// AddNode adds a new node to the network and migrates files as needed
func (s *NetworkContentService) AddNode(nodeAddress string) (int32, error) {
	// Check if node already exists
	if _, exists := s.clients[nodeAddress]; exists {
		return 0, fmt.Errorf("node %s already exists", nodeAddress)
	}

	// Try to connect to the node with a timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := grpc.DialContext(ctx, nodeAddress, grpc.WithInsecure(), grpc.WithBlock())
	if err != nil {
		if err == context.DeadlineExceeded {
			return 0, fmt.Errorf("failed to connect to node %s: connection timed out - node may not be running", nodeAddress)
		}
		return 0, fmt.Errorf("failed to connect to node %s: %v", nodeAddress, err)
	}

	// Create client and verify connection by making a simple RPC call
	client := proto.NewStorageServiceClient(conn)
	_, err = client.List(ctx, &proto.ListRequest{})
	if err != nil {
		conn.Close()
		return 0, fmt.Errorf("node %s is not responding correctly: %v", nodeAddress, err)
	}

	// Store the connection and client
	s.clients[nodeAddress] = client
	s.conns[nodeAddress] = conn

	// Update hash ring
	s.updateRing()

	// Get files that need to be migrated
	filesToMigrate := s.getFilesToMigrateForAdd(nodeAddress)

	// Migrate files
	migratedCount, err := s.migrateFiles(filesToMigrate)
	if err != nil {
		// If migration fails, remove the node
		s.updateRing()
		delete(s.clients, nodeAddress)
		if conn := s.conns[nodeAddress]; conn != nil {
			conn.Close()
		}
		delete(s.conns, nodeAddress)
		return 0, fmt.Errorf("failed to migrate files: %v", err)
	}

	return migratedCount, nil
}

// RemoveNode removes a node from the network and migrates its files
func (s *NetworkContentService) RemoveNode(nodeAddress string) (int32, error) {
	// Check if node exists
	if _, exists := s.clients[nodeAddress]; !exists {
		return 0, fmt.Errorf("node %s does not exist", nodeAddress)
	}

	// Get the client for the node to be removed
	client := s.clients[nodeAddress]

	// First, get all files from the node to be removed
	log.Printf("Listing files on node %s before removal", nodeAddress)
	listResp, err := client.List(s.ctx, &proto.ListRequest{})
	if err != nil {
		log.Printf("Warning: failed to list files on node %s: %v", nodeAddress, err)
		return 0, fmt.Errorf("failed to list files on node %s: %v", nodeAddress, err)
	}

	log.Printf("Found %d videos on node %s to be removed", len(listResp.VideoIds), nodeAddress)
	for _, videoId := range listResp.VideoIds {
		log.Printf("Video on node %s: %s", nodeAddress, videoId)
	}

	// Create a temporary copy of the ring without the node to be removed
	log.Printf("Temporarily updating hash ring to calculate migration targets")
	tempRing := make([]nodeInfo, 0, len(s.ring)-1)
	for _, node := range s.ring {
		if node.address != nodeAddress {
			tempRing = append(tempRing, node)
		}
	}

	// Store the original ring and set the temporary ring
	originalRing := s.ring
	s.ring = tempRing

	// Calculate which files need to be migrated using the temporary ring
	log.Printf("Calculating files that need to be migrated from node %s", nodeAddress)
	filesToMigrate := s.getFilesToMigrateForRemove(nodeAddress)

	// Restore the original ring
	s.ring = originalRing

	// Log migration plan
	var totalFilesToMigrate int32 = 0
	for targetNode, files := range filesToMigrate {
		log.Printf("Node %s will receive %d files from %s", targetNode, len(files), nodeAddress)
		totalFilesToMigrate += int32(len(files))
	}
	log.Printf("Total files to migrate from %s: %d", nodeAddress, totalFilesToMigrate)

	// Migrate files
	log.Printf("Starting file migration from node %s", nodeAddress)
	migratedCount, err := s.migrateFiles(filesToMigrate)
	if err != nil {
		log.Printf("Warning: file migration failed: %v", err)
		// Don't return error, as we still want to remove the node
	} else {
		log.Printf("Successfully migrated %d/%d files from %s", migratedCount, totalFilesToMigrate, nodeAddress)
	}

	// Remove node from maps and close connection
	delete(s.clients, nodeAddress)
	if conn := s.conns[nodeAddress]; conn != nil {
		conn.Close()
	}
	delete(s.conns, nodeAddress)

	// Update the hash ring
	s.updateRing()

	// Verify that all files were migrated
	if migratedCount < totalFilesToMigrate {
		log.Printf("Warning: not all files were migrated (%d/%d)", migratedCount, totalFilesToMigrate)
	} else {
		log.Printf("All files were successfully migrated from node %s", nodeAddress)
	}

	return migratedCount, nil
}

// ListNodes returns a list of all storage nodes in sorted order
func (s *NetworkContentService) ListNodes() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	nodes := make([]string, len(s.ring))
	for i, node := range s.ring {
		nodes[i] = node.address
	}
	return nodes
}

// Close closes all connections to storage nodes
func (s *NetworkContentService) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	var lastErr error
	for address, conn := range s.conns {
		if err := conn.Close(); err != nil {
			lastErr = err
			log.Printf("Warning: failed to close connection to node %s: %v", address, err)
		}
	}

	s.clients = make(map[string]proto.StorageServiceClient)
	s.conns = make(map[string]*grpc.ClientConn)
	return lastErr
}

// Read implements VideoContentService.Read
func (s *NetworkContentService) Read(videoId string, filename string) ([]byte, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if we have any nodes
	if len(s.clients) == 0 {
		return nil, fmt.Errorf("no storage nodes available")
	}

	// Find the node that should have this file
	key := fmt.Sprintf("%s/%s", videoId, filename)
	targetNode := s.findNodeForKey(key)
	if targetNode == "" {
		return nil, fmt.Errorf("no storage nodes available")
	}

	// Try to read from the target node
	client := s.clients[targetNode]
	resp, err := client.Read(s.ctx, &proto.ReadRequest{
		VideoId:  videoId,
		Filename: filename,
	})
	if err == nil {
		return resp.Data, nil
	}

	// If the file is not on the target node, try all nodes
	// This can happen during migration or if the file was not properly migrated
	var lastErr error
	for addr, client := range s.clients {
		resp, err := client.Read(s.ctx, &proto.ReadRequest{
			VideoId:  videoId,
			Filename: filename,
		})
		if err == nil {
			return resp.Data, nil
		}
		lastErr = err
		log.Printf("Failed to read from node %s: %v", addr, err)
	}

	return nil, fmt.Errorf("failed to read from any node: %v", lastErr)
}

// Write implements VideoContentService.Write
func (s *NetworkContentService) Write(videoId string, filename string, data []byte) error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if we have any nodes
	if len(s.clients) == 0 {
		return fmt.Errorf("no storage nodes available")
	}

	// Find the node that should store this file
	key := fmt.Sprintf("%s/%s", videoId, filename)
	targetNode := s.findNodeForKey(key)
	if targetNode == "" {
		return fmt.Errorf("no storage nodes available")
	}

	// Write to the target node
	client := s.clients[targetNode]
	_, err := client.Write(s.ctx, &proto.WriteRequest{
		VideoId:  videoId,
		Filename: filename,
		Data:     data,
	})
	if err != nil {
		return fmt.Errorf("failed to write to node %s: %v", targetNode, err)
	}

	return nil
}

// Ensure NetworkContentService implements VideoContentService
var _ VideoContentService = (*NetworkContentService)(nil)
