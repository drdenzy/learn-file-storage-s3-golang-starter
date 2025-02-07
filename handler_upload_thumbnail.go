package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
)

func (cfg *apiConfig) handlerUploadThumbnail(w http.ResponseWriter, r *http.Request) {
	// Parse video ID from URL
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

	// Parse multipart form (10MB limit)
	const maxMemory = 10 << 20 // 10MB
	if err := r.ParseMultipartForm(maxMemory); err != nil {
		respondWithError(w, http.StatusBadRequest, "Error parsing form", err)
		return
	}

	// Get thumbnail file from form
	file, header, err := r.FormFile("thumbnail")
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Missing thumbnail file", err)
		return
	}
	defer file.Close()

	// Get media type and validate
	mediaTypeWithParams := header.Header.Get("Content-Type")
	parsedMediaType, _, err := mime.ParseMediaType(mediaTypeWithParams)
	//ext, err := getImageExtensionFromMIME(mediaType)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Invalid Content-Type header", err)
		return
	}

	// Allow only JPEG and PNG
	if parsedMediaType != "image/jpeg" && parsedMediaType != "image/png" {
		respondWithError(w, http.StatusUnsupportedMediaType, "Only JPEG and PNG images are allowed", nil)
		return
	}

	// Determine file extension
	var ext string
	switch parsedMediaType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	}

	// Generate random filename
	randomBytes := make([]byte, 32)
	if _, err := rand.Read(randomBytes); err != nil {
		respondWithError(w, http.StatusInternalServerError,
			"Failed to generate filename", err)
		return
	}

	// Create filename and path
	filename := base64.RawURLEncoding.EncodeToString(randomBytes) + ext
	filePath := filepath.Join(cfg.assetsRoot, filename)

	// Create destination file
	dst, err := os.Create(filePath)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to create file", err)
		return
	}
	defer dst.Close()

	// Copy file contents
	if _, err := io.Copy(dst, file); err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to save file", err)
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

	// Update database with new URL
	thumbnailURL := fmt.Sprintf("http://localhost:%s/assets/%s", cfg.port, filename)
	video.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video", err)
		return
	}

	respondWithJSON(w, http.StatusOK, video)
}
