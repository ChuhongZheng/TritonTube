// Lab 7: Implement a web server

package web

import (
	"bytes"
	"context"
	"fmt"
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

	"tritontube/internal/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type server struct {
	Addr string
	Port int

	metadataService VideoMetadataService
	contentService  VideoContentService

	mux        *http.ServeMux
	grpcServer *grpc.Server
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
	// Create HTTP server
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("/upload", s.handleUpload)
	s.mux.HandleFunc("/videos/", s.handleVideo)
	s.mux.HandleFunc("/content/", s.handleVideoContent)
	s.mux.HandleFunc("/", s.handleIndex)

	// Create gRPC server
	s.grpcServer = grpc.NewServer()
	proto.RegisterVideoContentAdminServiceServer(s.grpcServer, &adminServer{
		contentService: s.contentService,
	})

	// Create a new listener for gRPC server
	grpcAddr := fmt.Sprintf("%s:%d", s.Addr, s.Port+1)
	grpcLis, err := net.Listen("tcp", grpcAddr)
	if err != nil {
		return fmt.Errorf("failed to create gRPC listener: %v", err)
	}

	// Start both servers
	go func() {
		log.Printf("Starting gRPC server on %s", grpcAddr)
		if err := s.grpcServer.Serve(grpcLis); err != nil {
			log.Printf("gRPC server error: %v", err)
		}
	}()

	log.Printf("Starting HTTP server on %s", lis.Addr().String())
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

	// Verify each video exists
	for _, v := range videos {
		// Try to read manifest.mpd to verify video exists
		_, err := s.contentService.Read(v.Id, "manifest.mpd")
		if err != nil {
			// If file doesn't exist, remove from database
			log.Printf("Video %s not found in storage, removing from database", v.Id)
			if err := s.metadataService.Delete(v.Id); err != nil {
				log.Printf("Failed to remove video %s from database: %v", v.Id, err)
			}
			continue
		}

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

// handleUpload handles video upload requests
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

	// Save uploaded file temporarily for processing
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

	// Capture both stdout and stderr
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		log.Printf("Failed to convert video: %v", err)
		log.Printf("ffmpeg stdout: %s", stdout.String())
		log.Printf("ffmpeg stderr: %s", stderr.String())
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// Log successful conversion
	log.Printf("Video conversion successful")
	log.Printf("ffmpeg stdout: %s", stdout.String())
	log.Printf("ffmpeg stderr: %s", stderr.String())

	// Save all generated files
	files, err := os.ReadDir(tempDir)
	if err != nil {
		log.Printf("Failed to read temp directory: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// First, find and save the manifest file
	for _, file := range files {
		if file.IsDir() {
			continue
		}

		// Skip the original MP4 file
		if strings.HasSuffix(file.Name(), ".mp4") {
			continue
		}

		if file.Name() == "manifest.mpd" {
			data, err := os.ReadFile(filepath.Join(tempDir, file.Name()))
			if err != nil {
				log.Printf("Failed to read manifest file: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}

			// Update manifest content to use video ID prefix
			manifestContent := string(data)
			manifestContent = strings.ReplaceAll(manifestContent, "init-", fmt.Sprintf("%s_init-", videoId))
			manifestContent = strings.ReplaceAll(manifestContent, "chunk-", fmt.Sprintf("%s_chunk-", videoId))

			if err := s.contentService.Write(videoId, file.Name(), []byte(manifestContent)); err != nil {
				log.Printf("Failed to write manifest file: %v", err)
				http.Error(w, "Internal Server Error", http.StatusInternalServerError)
				return
			}
			break
		}
	}

	// Then save all other files with video ID prefix
	for _, file := range files {
		if file.IsDir() || file.Name() == "manifest.mpd" || strings.HasSuffix(file.Name(), ".mp4") {
			continue
		}

		// Add video ID prefix to filename
		newName := file.Name()
		if strings.HasPrefix(newName, "init-") {
			newName = fmt.Sprintf("%s_%s", videoId, newName)
		} else if strings.HasPrefix(newName, "chunk-") {
			newName = fmt.Sprintf("%s_%s", videoId, newName)
		}

		data, err := os.ReadFile(filepath.Join(tempDir, file.Name()))
		if err != nil {
			log.Printf("Failed to read file %s: %v", file.Name(), err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		if err := s.contentService.Write(videoId, newName, data); err != nil {
			log.Printf("Failed to write file %s: %v", newName, err)
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

// adminServer implements the VideoContentAdminService
type adminServer struct {
	proto.UnimplementedVideoContentAdminServiceServer
	contentService VideoContentService
}

func (s *adminServer) AddNode(ctx context.Context, req *proto.AddNodeRequest) (*proto.AddNodeResponse, error) {
	// Type assert to NetworkContentService
	nwService, ok := s.contentService.(*NetworkContentService)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "content service is not a network service")
	}

	// Add the node
	migratedCount, err := nwService.AddNode(req.NodeAddress)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to add node: %v", err))
	}

	return &proto.AddNodeResponse{
		MigratedFileCount: migratedCount,
	}, nil
}

func (s *adminServer) RemoveNode(ctx context.Context, req *proto.RemoveNodeRequest) (*proto.RemoveNodeResponse, error) {
	// Type assert to NetworkContentService
	nwService, ok := s.contentService.(*NetworkContentService)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "content service is not a network service")
	}

	// Remove the node
	migratedCount, err := nwService.RemoveNode(req.NodeAddress)
	if err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("failed to remove node: %v", err))
	}

	return &proto.RemoveNodeResponse{
		MigratedFileCount: migratedCount,
	}, nil
}

func (s *adminServer) ListNodes(ctx context.Context, req *proto.ListNodesRequest) (*proto.ListNodesResponse, error) {
	// Type assert to NetworkContentService
	nwService, ok := s.contentService.(*NetworkContentService)
	if !ok {
		return nil, status.Error(codes.FailedPrecondition, "content service is not a network service")
	}

	// Get list of nodes
	nodes := nwService.ListNodes()
	return &proto.ListNodesResponse{
		Nodes: nodes,
	}, nil
}
