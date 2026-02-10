package routes

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultImageCacheDir = "./images"
	imageCacheDirEnvName = "IMAGE_DIR"
	thumbDirEnvName      = "THUMB_DIR"
)

func getImageCacheBaseDir() string {
	if envDir := strings.TrimSpace(os.Getenv(imageCacheDirEnvName)); envDir != "" {
		return filepath.Clean(envDir)
	}
	// Backward compatibility: if only THUMB_DIR is configured,
	// reuse it as the base image cache for both /thumb and /direct photo.
	if envThumbDir := strings.TrimSpace(os.Getenv(thumbDirEnvName)); envThumbDir != "" {
		return filepath.Clean(envThumbDir)
	}
	return filepath.Clean(defaultImageCacheDir)
}

func getThumbCacheDir() string {
	return getImageCacheBaseDir()
}

func getDirectPhotoCachePath(messageID int) string {
	return filepath.Join(getImageCacheBaseDir(), fmt.Sprintf("%d.jpg", messageID))
}

func writeBytesAtomically(targetFile string, data []byte, perm os.FileMode) error {
	cacheDir := filepath.Dir(targetFile)
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return err
	}

	tmpFile, err := os.CreateTemp(cacheDir, filepath.Base(targetFile)+".*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmpFile.Name()
	defer func() {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
	}()

	if _, err := tmpFile.Write(data); err != nil {
		return err
	}
	if err := tmpFile.Chmod(perm); err != nil {
		return err
	}
	if err := tmpFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, targetFile); err != nil {
		return err
	}
	return nil
}
