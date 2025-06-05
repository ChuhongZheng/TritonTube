// Lab 7: Implement a local filesystem video content service

package web

import (
	"fmt"
	"os"
	"path/filepath"
)

// FSVideoContentService implements VideoContentService using the local filesystem.
type FSVideoContentService struct {
	baseDir string
}

// NewFSVideoContentService creates a new FSVideoContentService instance
func NewFSVideoContentService(baseDir string) (*FSVideoContentService, error) {
	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %v", err)
	}
	return &FSVideoContentService{baseDir: baseDir}, nil
}

// Read implements VideoContentService.Read
func (s *FSVideoContentService) Read(videoId string, filename string) ([]byte, error) {
	path := filepath.Join(s.baseDir, videoId, filename)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read file %s: %v", path, err)
	}
	return data, nil
}

// Write implements VideoContentService.Write
func (s *FSVideoContentService) Write(videoId string, filename string, data []byte) error {
	// Create video directory if it doesn't exist
	videoDir := filepath.Join(s.baseDir, videoId)
	if err := os.MkdirAll(videoDir, 0755); err != nil {
		return fmt.Errorf("failed to create video directory: %v", err)
	}

	// Write file
	path := filepath.Join(videoDir, filename)
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("failed to write file %s: %v", path, err)
	}
	return nil
}

// Ensure FSVideoContentService implements VideoContentService
var _ VideoContentService = (*FSVideoContentService)(nil)
