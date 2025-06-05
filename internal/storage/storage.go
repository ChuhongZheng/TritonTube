// Lab 8: Implement a network video content service (server)

package storage

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"

	pb "internal/proto"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Server represents a storage server instance
type Server struct {
	pb.UnimplementedStorageServiceServer
	baseDir string
	grpcServer *grpc.Server
}

// NewServer creates a new storage server instance
func NewServer(baseDir string) (*Server, error) {
	// Ensure base directory exists
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create base directory: %v", err)
	}

	return &Server{
		baseDir: baseDir,
		grpcServer: grpc.NewServer(),
	}, nil
}

// Start starts the storage server on the given listener
func (s *Server) Start(lis net.Listener) error {
	pb.RegisterStorageServiceServer(s.grpcServer, s)
	return s.grpcServer.Serve(lis)
}

// Stop stops the storage server
func (s *Server) Stop() {
	if s.grpcServer != nil {
		s.grpcServer.GracefulStop()
	}
}

// Read implements the Read RPC method
func (s *Server) Read(req *pb.ReadRequest, stream pb.StorageService_ReadServer) error {
	// Construct file path
	filePath := filepath.Join(s.baseDir, req.VideoId, req.Filename)

	// Open file
	file, err := os.Open(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return status.Error(codes.NotFound, "file not found")
		}
		return status.Errorf(codes.Internal, "failed to open file: %v", err)
	}
	defer file.Close()

	// Read file in chunks
	buffer := make([]byte, 1024*1024) // 1MB chunks
	for {
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return status.Errorf(codes.Internal, "failed to read file: %v", err)
		}
		if n > 0 {
			if err := stream.Send(&pb.ReadResponse{Data: buffer[:n]}); err != nil {
				return status.Errorf(codes.Internal, "failed to send data: %v", err)
			}
		}
		if err == io.EOF {
			break
		}
	}

	return nil
}

// Write implements the Write RPC method
func (s *Server) Write(stream pb.StorageService_WriteServer) error {
	var req *pb.WriteRequest
	var err error

	// Get first message to get video_id and filename
	req, err = stream.Recv()
	if err != nil {
		return status.Errorf(codes.Internal, "failed to receive first message: %v", err)
	}

	// Create video directory
	videoDir := filepath.Join(s.baseDir, req.VideoId)
	if err := os.MkdirAll(videoDir, 0755); err != nil {
		return status.Errorf(codes.Internal, "failed to create video directory: %v", err)
	}

	// Create file
	filePath := filepath.Join(videoDir, req.Filename)
	file, err := os.Create(filePath)
	if err != nil {
		return status.Errorf(codes.Internal, "failed to create file: %v", err)
	}
	defer file.Close()

	// Write first chunk
	if _, err := file.Write(req.Data); err != nil {
		return status.Errorf(codes.Internal, "failed to write first chunk: %v", err)
	}

	// Write remaining chunks
	for {
		req, err = stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return status.Errorf(codes.Internal, "failed to receive chunk: %v", err)
		}

		if _, err := file.Write(req.Data); err != nil {
			return status.Errorf(codes.Internal, "failed to write chunk: %v", err)
		}
	}

	// Send response
	return stream.SendAndClose(&pb.WriteResponse{})
}

// Delete implements the Delete RPC method
func (s *Server) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	filePath := filepath.Join(s.baseDir, req.VideoId, req.Filename)

	// Check if file exists
	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			return nil, status.Error(codes.NotFound, "file not found")
		}
		return nil, status.Errorf(codes.Internal, "failed to check file: %v", err)
	}

	// Delete file
	if err := os.Remove(filePath); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete file: %v", err)
	}

	// Try to remove video directory if empty
	videoDir := filepath.Join(s.baseDir, req.VideoId)
	os.Remove(videoDir) // Ignore error if directory is not empty

	return &pb.DeleteResponse{}, nil
}
