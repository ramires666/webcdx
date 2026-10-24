package main

import "testing"

func TestBrowserDownloadProgressBeforeBeginNeverCancelsCompletedFile(t *testing.T) {
	s := &browserSession{downloads: map[string]*browserDownload{}}
	if recordManagedDownload(s, "Browser.downloadProgress", "file", "completed", 1234) {
		t.Fatal("out-of-order progress cancelled a permitted download")
	}
	if recordManagedDownload(s, "Browser.downloadWillBegin", "file", "", 0) {
		t.Fatal("late begin cancelled the completed file")
	}
	if s.downloads["file"].Status != "completed" || s.downloads["file"].Bytes != 1234 {
		t.Fatalf("late begin reset download metadata: %+v", s.downloads["file"])
	}
	if !recordManagedDownload(s, "Browser.downloadProgress", "large", "inProgress", browserDownloadMaxBytes+1) {
		t.Fatal("oversized download was not cancelled")
	}
}
