package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const browserArtifactMax = 256 << 20

var browserArtifactName = regexp.MustCompile(`^image_[0-9a-f]{24}\.jpg$`)
var browserSessionName = regexp.MustCompile(`^browser_[0-9a-f]{24}$`)

func (b *browserManager) cleanArtifacts() (int64, error) {
	var total int64
	root := filepath.Join(b.config.Dir, "artifacts")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		name := entry.Name()
		owned := browserArtifactName.MatchString(name) || browserSessionName.MatchString(filepath.Base(filepath.Dir(path))) && (downloadGUID.MatchString(name) || strings.HasSuffix(name, ".crdownload"))
		if !owned {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if time.Since(info.ModTime()) > 24*time.Hour && !strings.HasSuffix(path, ".crdownload") {
			if err := os.Remove(path); err != nil {
				return err
			}
			return nil
		}
		total += info.Size()
		return nil
	})
	return total, err
}
