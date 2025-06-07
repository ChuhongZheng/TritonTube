// Lab 8: Implement a network video content service (server)

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"tritontube/internal/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	proto.UnimplementedStorageServiceServer
	rootDir string
	mu      sync.RWMutex
}

func NewServer(rootDir string) (*Server, error) {
	// Create root directory if it doesn't exist
	if err := os.MkdirAll(rootDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create root directory: %v", err)
	}

	return &Server{
		rootDir: rootDir,
	}, nil
}

// getFilePath returns the full path for a file, using videoId/filename directory structure
func (s *Server) getFilePath(videoId string, filename string) string {
	// Replace any path separators in videoId and filename with underscores for safety
	safeVideoId := strings.ReplaceAll(videoId, string(os.PathSeparator), "_")
	safeFilename := strings.ReplaceAll(filename, string(os.PathSeparator), "_")

	// Use directory structure: rootDir/videoId/filename
	return filepath.Join(s.rootDir, safeVideoId, safeFilename)
}

func (s *Server) Read(ctx context.Context, req *proto.ReadRequest) (*proto.ReadResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Get file path
	filePath := s.getFilePath(req.VideoId, req.Filename)

	// Read file
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to read file: %v", err))
	}

	return &proto.ReadResponse{
		Data: data,
	}, nil
}

func (s *Server) Write(ctx context.Context, req *proto.WriteRequest) (*proto.WriteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get file path
	filePath := s.getFilePath(req.VideoId, req.Filename)

	// Create parent directory if it doesn't exist
	parentDir := filepath.Dir(filePath)
	if err := os.MkdirAll(parentDir, 0755); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to create parent directory: %v", err))
	}

	// Write file
	if err := os.WriteFile(filePath, req.Data, 0644); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to write file: %v", err))
	}

	return &proto.WriteResponse{
		Size: int64(len(req.Data)),
	}, nil
}

func (s *Server) Delete(ctx context.Context, req *proto.DeleteRequest) (*proto.DeleteResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Get file path
	filePath := s.getFilePath(req.VideoId, req.Filename)

	// Delete file
	if err := os.Remove(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to delete file: %v", err))
	}

	return &proto.DeleteResponse{}, nil
}

func (s *Server) List(ctx context.Context, req *proto.ListRequest) (*proto.ListResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// List all directories in the root directory
	entries, err := os.ReadDir(s.rootDir)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to list files: %v", err))
	}

	// Extract video IDs from directory names
	var videoIds []string
	for _, entry := range entries {
		if entry.IsDir() {
			videoIds = append(videoIds, entry.Name())
		}
	}

	return &proto.ListResponse{
		VideoIds: videoIds,
	}, nil
}
