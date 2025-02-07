package main

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/bootdotdev/learn-file-storage-s3-golang-starter/internal/auth"
	"github.com/google/uuid"
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

	// Read file data
	data, err := io.ReadAll(file)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error reading file", err)
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

	// Verify video ownership
	userUUID, err := uuid.Parse(userID.String())
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid user ID", err)
		return
	}

	if video.UserID != userUUID {
		respondWithError(w, http.StatusUnauthorized, "Unauthorized access", nil)
		return
	}

	// Store thumbnail in memory
	mediaType := header.Header.Get("Content-Type")
	videoThumbnails[videoID] = thumbnail{
		data:      data,
		mediaType: mediaType,
	}

	// Update database with thumbnail URL
	thumbnailURL := fmt.Sprintf("http://localhost:%s/api/thumbnails/%s", cfg.port, videoID)
	video.ThumbnailURL = &thumbnailURL

	err = cfg.db.UpdateVideo(video)
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Failed to update video", err)
		return
	}

	// Return updated video metadata
	respondWithJSON(w, http.StatusOK, video)
}
