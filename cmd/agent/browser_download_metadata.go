package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/http"
	"os"
)

const browserDownloadMaxBytes = 64 << 20

func saveBrowserDownloadedFile(source, destination, downloadID string) (map[string]any, error) {
	if downloadID == "" || len(downloadID) > 64 {
		return nil, toolErr("invalid_arguments", "invalid download identifier")
	}
	sourceInfo, err := os.Lstat(source)
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return nil, toolErr("invalid_path", "download source is unavailable")
	}
	data, info, err := readRegularFile(source, browserDownloadMaxBytes)
	if err != nil {
		if errors.Is(err, errFileTooLarge) {
			return nil, toolErr("too_large", "download exceeds the 64 MiB limit")
		}
		var coded *codedError
		if errors.As(err, &coded) && coded.code == "too_large" {
			return nil, toolErr("too_large", "download exceeds the 64 MiB limit")
		}
		return nil, toolErr("invalid_path", "download source could not be read")
	}
	if !info.Mode().IsRegular() {
		return nil, toolErr("invalid_path", "download source is unavailable")
	}
	if destination == "" {
		return nil, toolErr("invalid_path", "download destination is invalid")
	}

	hash := sha256.Sum256(data)
	digest := hex.EncodeToString(hash[:])
	duplicate := false
	destinationInfo, statErr := os.Lstat(destination)
	if statErr == nil {
		if !destinationInfo.Mode().IsRegular() {
			return nil, toolErr("invalid_path", "download destination is not a regular file")
		}
		existing, _, readErr := readRegularFile(destination, browserDownloadMaxBytes)
		if readErr != nil {
			return nil, toolErr("invalid_path", "download destination could not be read")
		}
		existingHash := sha256.Sum256(existing)
		if existingHash != hash {
			return nil, toolErr("download_conflict", "destination already contains different data")
		}
		duplicate = true
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return nil, toolErr("invalid_path", "download destination is unavailable")
	} else if err := atomicCreate(destination, data); err != nil {
		var coded *codedError
		if errors.As(err, &coded) && coded.code == "already_exists" {
			// Another writer won the create race. Accept only byte-identical data.
			existing, existingInfo, readErr := readRegularFile(destination, browserDownloadMaxBytes)
			if readErr != nil || !existingInfo.Mode().IsRegular() {
				return nil, toolErr("invalid_path", "download destination is unavailable")
			}
			existingHash := sha256.Sum256(existing)
			if existingHash != hash {
				return nil, toolErr("download_conflict", "destination already contains different data")
			}
			duplicate = true
		} else {
			return nil, toolErr("io_error", "download could not be saved")
		}
	}

	metadata := map[string]any{
		"path": destination, "download_id": downloadID, "bytes": len(data), "sha256": digest,
		"mime_type": http.DetectContentType(data), "verified": true,
		"source_original_verified": false, "duplicate": duplicate,
	}
	if config, _, decodeErr := image.DecodeConfig(bytes.NewReader(data)); decodeErr == nil && config.Width > 0 && config.Height > 0 {
		metadata["width"], metadata["height"] = config.Width, config.Height
	}
	return metadata, nil
}
