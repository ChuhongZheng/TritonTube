// Lab 7: Implement a web server

package web

import (
	"html/template"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type server struct {
	Addr string
	Port int

	metadataService VideoMetadataService
	contentService  VideoContentService

	mux *http.ServeMux
}

func NewServer(
	metadataService VideoMetadataService,
	contentService VideoContentService,
) *server {
	return &server{
		metadataService: metadataService,
		contentService:  contentService,
	}
}

func (s *server) Start(lis net.Listener) error {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/upload", s.handleUpload)
	s.mux.HandleFunc("/videos/", s.handleVideo)
	s.mux.HandleFunc("/content/", s.handleVideoContent)
	s.mux.HandleFunc("/", s.handleIndex)

	return http.Serve(lis, s.mux)
}

func (s *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	// Get list of videos
	videos, err := s.metadataService.List()
	if err != nil {
		log.Printf("Failed to list videos: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Create template data with escaped IDs
	type VideoData struct {
		Id         string
		EscapedId  string
		UploadTime string
	}
	var videoData []VideoData
	for _, v := range videos {
		videoData = append(videoData, VideoData{
			Id:         v.Id,
			EscapedId:  template.URLQueryEscaper(v.Id),
			UploadTime: v.UploadedAt.Format("2006-01-02 15:04:05"),
		})
	}

	// Parse and execute template
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		log.Printf("Failed to parse index template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(w, videoData); err != nil {
		log.Printf("Failed to execute index template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (s *server) handleVideo(w http.ResponseWriter, r *http.Request) {
	videoId := r.URL.Path[len("/videos/"):]
	log.Println("Video ID:", videoId)

	// Get video metadata
	video, err := s.metadataService.Read(videoId)
	if err != nil {
		log.Printf("Failed to read video metadata: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if video == nil {
		http.NotFound(w, r)
		return
	}

	// Parse and execute template
	tmpl, err := template.New("video").Parse(videoHTML)
	if err != nil {
		log.Printf("Failed to parse video template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	if err := tmpl.Execute(w, video); err != nil {
		log.Printf("Failed to execute video template: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB max
		log.Printf("Failed to parse multipart form: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}

	// Get uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		log.Printf("Failed to get uploaded file: %v", err)
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Get video ID from filename (without extension)
	videoId := strings.TrimSuffix(header.Filename, filepath.Ext(header.Filename))
	if videoId == "" {
		http.Error(w, "Invalid filename", http.StatusBadRequest)
		return
	}

	// Check if video ID already exists
	existing, err := s.metadataService.Read(videoId)
	if err != nil {
		log.Printf("Failed to check existing video: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if existing != nil {
		http.Error(w, "Video ID already exists", http.StatusConflict)
		return
	}

	// Create temporary directory for video processing
	tempDir, err := os.MkdirTemp("", "tritontube-*")
	if err != nil {
		log.Printf("Failed to create temp directory: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(tempDir)

	// Save uploaded file
	videoPath := filepath.Join(tempDir, header.Filename)
	videoFile, err := os.Create(videoPath)
	if err != nil {
		log.Printf("Failed to create video file: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	defer videoFile.Close()

	if _, err := io.Copy(videoFile, file); err != nil {
		log.Printf("Failed to save video file: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Convert to MPEG-DASH
	manifestPath := filepath.Join(tempDir, "manifest.mpd")
	cmd := exec.Command("ffmpeg",
		"-i", videoPath, // input file
		"-c:v", "libx264", // video codec
		"-c:a", "aac", // audio codec
		"-bf", "1", // max 1 b-frame
		"-keyint_min", "120", // minimum keyframe interval
		"-g", "120", // keyframe every 120 frames
		"-sc_threshold", "0", // scene change threshold
		"-b:v", "3000k", // video bitrate
		"-b:a", "128k", // audio bitrate
		"-f", "dash", // dash format
		"-use_timeline", "1", // use timeline
		"-use_template", "1", // use template
		"-init_seg_name", "init-$RepresentationID$.m4s", // init segment naming
		"-media_seg_name", "chunk-$RepresentationID$-$Number%05d$.m4s", // media segment naming
		"-seg_duration", "4", // segment duration in seconds
		manifestPath) // output file

	if err := cmd.Run(); err != nil {
		log.Printf("Failed to convert video: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Save all generated files
	files, err := os.ReadDir(tempDir)
	if err != nil {
		log.Printf("Failed to read temp directory: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		data, err := os.ReadFile(filepath.Join(tempDir, file.Name()))
		if err != nil {
			log.Printf("Failed to read file %s: %v", file.Name(), err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err := s.contentService.Write(videoId, file.Name(), data); err != nil {
			log.Printf("Failed to write file %s: %v", file.Name(), err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}

	// Save metadata
	if err := s.metadataService.Create(videoId, time.Now()); err != nil {
		log.Printf("Failed to save metadata: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Redirect to home page
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *server) handleVideoContent(w http.ResponseWriter, r *http.Request) {
	// parse /content/<videoId>/<filename>
	videoId := r.URL.Path[len("/content/"):]
	parts := strings.Split(videoId, "/")
	if len(parts) != 2 {
		http.Error(w, "Invalid content path", http.StatusBadRequest)
		return
	}
	videoId = parts[0]
	filename := parts[1]
	log.Println("Video ID:", videoId, "Filename:", filename)

	// Check if video exists
	video, err := s.metadataService.Read(videoId)
	if err != nil {
		log.Printf("Failed to read video metadata: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	if video == nil {
		http.NotFound(w, r)
		return
	}

	// Read and serve file
	data, err := s.contentService.Read(videoId, filename)
	if err != nil {
		log.Printf("Failed to read file: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Set content type based on file extension
	switch filepath.Ext(filename) {
	case ".mpd":
		w.Header().Set("Content-Type", "application/dash+xml")
	case ".m4s":
		w.Header().Set("Content-Type", "video/mp4")
	default:
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	w.Write(data)
}
