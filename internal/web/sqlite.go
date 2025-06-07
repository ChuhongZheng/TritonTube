// Lab 7: Implement a SQLite video metadata service

package web

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type SQLiteVideoMetadataService struct {
	db *sql.DB
}

// NewSQLiteVideoMetadataService creates a new SQLiteVideoMetadataService instance
func NewSQLiteVideoMetadataService(dbPath string) (*SQLiteVideoMetadataService, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("failed to open database: %v", err)
	}

	// Create videos table if it doesn't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS videos (
			id TEXT PRIMARY KEY,
			uploaded_at TIMESTAMP NOT NULL
		)
	`)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create videos table: %v", err)
	}

	return &SQLiteVideoMetadataService{db: db}, nil
}

// Create implements VideoMetadataService.Create
func (s *SQLiteVideoMetadataService) Create(videoId string, uploadedAt time.Time) error {
	_, err := s.db.Exec("INSERT INTO videos (id, uploaded_at) VALUES (?, ?)",
		videoId, uploadedAt)
	if err != nil {
		return fmt.Errorf("failed to create video metadata: %v", err)
	}
	return nil
}

// List implements VideoMetadataService.List
func (s *SQLiteVideoMetadataService) List() ([]VideoMetadata, error) {
	rows, err := s.db.Query("SELECT id, uploaded_at FROM videos ORDER BY uploaded_at DESC")
	if err != nil {
		return nil, fmt.Errorf("failed to list videos: %v", err)
	}
	defer rows.Close()

	var videos []VideoMetadata
	for rows.Next() {
		var video VideoMetadata
		if err := rows.Scan(&video.Id, &video.UploadedAt); err != nil {
			return nil, fmt.Errorf("failed to scan video row: %v", err)
		}
		videos = append(videos, video)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating video rows: %v", err)
	}

	return videos, nil
}

// Read implements VideoMetadataService.Read
func (s *SQLiteVideoMetadataService) Read(id string) (*VideoMetadata, error) {
	var video VideoMetadata
	err := s.db.QueryRow("SELECT id, uploaded_at FROM videos WHERE id = ?", id).
		Scan(&video.Id, &video.UploadedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to read video metadata: %v", err)
	}
	return &video, nil
}

// Delete implements VideoMetadataService.Delete
func (s *SQLiteVideoMetadataService) Delete(id string) error {
	_, err := s.db.Exec("DELETE FROM videos WHERE id = ?", id)
	if err != nil {
		return fmt.Errorf("failed to delete video metadata: %v", err)
	}
	return nil
}

// Close closes the database connection
func (s *SQLiteVideoMetadataService) Close() error {
	return s.db.Close()
}

// Ensure SQLiteVideoMetadataService implements VideoMetadataService
var _ VideoMetadataService = (*SQLiteVideoMetadataService)(nil)
