package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var onePixelPNG = mustDecodeDownloadFixture("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j9WQAAAAASUVORK5CYII=")

func mustDecodeDownloadFixture(encoded string) []byte {
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		panic(err)
	}
	return data
}

func downloadMetadataTestPaths(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "owned-download")
	if err := os.WriteFile(source, onePixelPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	return source, root
}

func TestSaveBrowserDownloadedFilePreservesBytesAndVerifiedMetadata(t *testing.T) {
	source, root := downloadMetadataTestPaths(t)
	destination := filepath.Join(root, "saved.png")
	metadata, err := saveBrowserDownloadedFile(source, destination, "download-1")
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(saved) != string(onePixelPNG) {
		t.Fatal("saved download bytes were transformed")
	}
	hash := sha256.Sum256(onePixelPNG)
	if metadata["sha256"] != hex.EncodeToString(hash[:]) || metadata["bytes"] != len(onePixelPNG) {
		t.Fatalf("metadata does not describe exact saved bytes: %#v", metadata)
	}
	if metadata["mime_type"] != "image/png" || metadata["width"] != 1 || metadata["height"] != 1 {
		t.Fatalf("PNG metadata not detected: %#v", metadata)
	}
	if metadata["verified"] != true || metadata["source_original_verified"] != false || metadata["duplicate"] != false {
		t.Fatalf("verification claims are incorrect: %#v", metadata)
	}
	if metadata["path"] != destination || metadata["download_id"] != "download-1" {
		t.Fatalf("identity metadata incorrect: %#v", metadata)
	}
}

func TestSaveBrowserDownloadedFileDuplicateAndConflictNeverOverwrite(t *testing.T) {
	source, root := downloadMetadataTestPaths(t)
	destination := filepath.Join(root, "saved.png")
	for i := 0; i < 10; i++ {
		metadata, err := saveBrowserDownloadedFile(source, destination, "download-repeat")
		if err != nil {
			t.Fatalf("save attempt %d: %v", i, err)
		}
		if metadata["duplicate"] != (i > 0) {
			t.Fatalf("duplicate flag on attempt %d: %#v", i, metadata)
		}
	}
	before, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := saveBrowserDownloadedFile(source, destination, "download-repeat"); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("duplicate save rewrote destination: before=%s after=%s", before.ModTime(), after.ModTime())
	}

	conflict := filepath.Join(root, "conflict.png")
	if err := os.WriteFile(conflict, []byte("existing destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = saveBrowserDownloadedFile(source, conflict, "download-conflict")
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "download_conflict" {
		t.Fatalf("different destination should be preserved with download_conflict, got %v", err)
	}
	preserved, err := os.ReadFile(conflict)
	if err != nil || string(preserved) != "existing destination" {
		t.Fatalf("conflicting destination changed: %q, %v", preserved, err)
	}
}

func TestSaveBrowserDownloadedFileRejectsInvalidAndOversizedSources(t *testing.T) {
	_, root := downloadMetadataTestPaths(t)
	destination := filepath.Join(root, "invalid-target")
	directory := filepath.Join(root, "directory-source")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := saveBrowserDownloadedFile(directory, destination, "directory-id"); err == nil {
		t.Fatal("directory source should be rejected")
	}
	oversized := filepath.Join(root, "oversized-source")
	file, err := os.Create(oversized)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(browserDownloadMaxBytes + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = saveBrowserDownloadedFile(oversized, destination, "oversized-id")
	var coded *codedError
	if !errors.As(err, &coded) || coded.code != "too_large" {
		t.Fatalf("oversized source should return too_large, got %v", err)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("invalid download created destination: %v", err)
	}
}
