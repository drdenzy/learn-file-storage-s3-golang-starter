// handler_upload_video.go
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
)

// ffprobeOutput struct
type ffprobeOutput struct {
	Streams []struct {
		CodecType string `json:"codec_type"`
		Width     int    `json:"width"`
		Height    int    `json:"height"`
	} `json:"streams"`
}

// getVideoAspectRatio determines video aspect ratio using ffprobe
func getVideoAspectRatio(filePath string) (string, error) {
	cmd := exec.Command("ffprobe", "-v", "error",
		"-print_format", "json",
		"-show_streams", filePath)

	var stdout bytes.Buffer
	cmd.Stdout = &stdout

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffprobe failed: %w", err)
	}

	var output ffprobeOutput
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		return "", fmt.Errorf("failed to parse ffprobe output: %w", err)
	}

	// Find first video stream
	var width, height int
	for _, stream := range output.Streams {
		if stream.CodecType == "video" {
			width = stream.Width
			height = stream.Height
			break
		}
	}

	if width == 0 || height == 0 {
		return "", fmt.Errorf("no video stream found")
	}

	// Calculate aspect ratio with tolerance
	ratio := float64(width) / float64(height)
	const tolerance = 0.05
	landscapeTarget := 16.0 / 9.0
	portraitTarget := 9.0 / 16.0

	switch {
	case math.Abs(ratio-landscapeTarget) <= landscapeTarget*tolerance:
		return "landscape", nil
	case math.Abs(ratio-portraitTarget) <= portraitTarget*tolerance:
		return "portrait", nil
	default:
		return "other", nil
	}
}

// processVideoForFastStart processes video for streaming optimization
func processVideoForFastStart(filePath string) (string, error) {
	outputPath := filePath + ".processing"

	cmd := exec.Command("ffmpeg",
		"-i", filePath, // Input file
		"-c", "copy", // Copy codec without re-encoding
		"-movflags", "faststart", // Move metadata to beginning
		"-f", "mp4", // Force MP4 format
		outputPath, // Output file
	)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ffmpeg failed: %w\nStderr: %s", err, stderr.String())
	}

	return outputPath, nil
}

func (cfg *apiConfig) handlerUploadVideo(w http.ResponseWriter, r *http.Request) {
	// Set 1GB upload limit
	r.Body = http.MaxBytesReader(w, r.Body, 1<<30)

	// Get video ID from URL
	videoIDString := r.PathValue("videoID")
	videoID, err := uuid.Parse(videoIDString)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid video ID", err)
		return
	}

	// Authenticate user
	token, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Couldn't find JWT", err)
		return
	}

	userID, err := auth.ValidateJWT(token, cfg.jwtSecret)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid JWT", err)
		return
	}

	// Get video metadata
	video, err := cfg.db.GetVideo(videoID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			respondWithError(w, http.StatusNotFound, "Video not found", nil)
			return
		}
		respondWithError(w, http.StatusInternalServerError, "Database error", err)
		return
	}

	// Verify ownership
	userUUID, err := uuid.Parse(userID.String())
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid user ID", err)
		return
	}

	if video.UserID != userUUID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized access", nil)
		return
	}

	// Parse multipart form
	if err := r.ParseMultipartForm(10 << 20); err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form", err)
		return
	}

	// Get video file from form
	file, header, err := r.FormFile("video")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing video file", err)
		return
	}
	defer file.Close()

	// Validate MIME type
	mediaTypeWithParams := header.Header.Get("Content-Type")
	parsedMediaType, _, err := mime.ParseMediaType(mediaTypeWithParams)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type", err)
		return
	}

	if parsedMediaType != "video/mp4" {
		respondWithError(w, http.StatusUnsupportedMediaType, "Only MP4 videos are allowed", nil)
		return
	}

	// Create temp file
	tempFile, err := os.CreateTemp("", "tubely-upload-*.mp4")
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create temp file", err)
		return
	}
	defer os.Remove(tempFile.Name())
	defer tempFile.Close()

	// Copy to temp file
	if _, err := io.Copy(tempFile, file); err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to save video", err)
		return
	}

	// Reset file pointer
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to process video", err)
		return
	}

	// Get aspect ratio
	aspect, err := getVideoAspectRatio(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to analyze video", err)
		return
	}

	//// Generate random filename with prefix
	//randomBytes := make([]byte, 32)
	//if _, err := rand.Read(randomBytes); err != nil {
	//	respondWithError(w, http.StatusInternalServerError,
	//		"Failed to generate filename", err)
	//	return
	//}
	//baseName := base64.RawURLEncoding.EncodeToString(randomBytes)
	//objectKey := fmt.Sprintf("%s/%s.mp4", aspect, baseName)
	//
	//// Upload to S3
	//_, err = cfg.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
	//	Bucket:      aws.String(cfg.s3Bucket),
	//	Key:         aws.String(objectKey),
	//	Body:        tempFile,
	//	ContentType: aws.String("video/mp4"),
	//})
	//if err != nil {
	//	respondWithError(w, http.StatusInternalServerError,
	//		"Failed to upload to S3", err)
	//	return
	//}

	// Process video for fast start
	processedPath, err := processVideoForFastStart(tempFile.Name())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Video processing failed", err)
		return
	}
	defer os.Remove(processedPath)

	// Open processed file
	processedFile, err := os.Open(processedPath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to open processed video", err)
		return
	}
	defer processedFile.Close()

	// Reset file pointer
	if _, err := processedFile.Seek(0, io.SeekStart); err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to read processed video", err)
		return
	}

	// Generate S3 key with aspect prefix
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to generate filename", err)
		return
	}
	baseName := base64.RawURLEncoding.EncodeToString(randomBytes)
	objectKey := fmt.Sprintf("%s/%s.mp4", aspect, baseName)

	// Upload processed file to S3
	_, err = cfg.s3Client.PutObject(context.Background(), &s3.PutObjectInput{
		Bucket:      aws.String(cfg.s3Bucket),
		Key:         aws.String(objectKey),
		Body:        processedFile,
		ContentType: aws.String("video/mp4"),
	})
	if err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to upload to S3", err)
		return
	}

	// Update database
	videoURL := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s",
		cfg.s3Bucket, cfg.s3Region, objectKey)
	video.VideoURL = &videoURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
