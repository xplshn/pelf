package main

import (
	"path/filepath"
	"strings"
	"fmt"
	"os"
	"os/exec"
)

func integrateAppImage(path, appPath string, entry *BundleEntry) {
	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entry.Png = extractAppImageMetadata("icon", path, filepath.Join(appPath, baseName+".png"))
	entry.Desktop = extractAppImageMetadata("desktop", path, filepath.Join(appPath, baseName+".desktop"))
}

func extractAppImageMetadata(metadataType, appImagePath, outputFile string) string {
	logMessage("INF", fmt.Sprintf("Extracting %s from AppImage: %s", metadataType, appImagePath))

	// Create a temporary directory for extraction
	tempDir, err := os.MkdirTemp("", "appimage-extract-")
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to create temporary directory: %v", err))
		return ""
	}
	// Defer the removal of the tempDir to ensure it is deleted at the end of the function
	defer func() {
		if err := os.RemoveAll(tempDir); err != nil {
			logMessage("ERR", fmt.Sprintf("Failed to remove temporary directory: %v", err))
		}
	}()

	if err := os.Chdir(tempDir); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to change directory to %s: %v", tempDir, err))
		return ""
	}

	var metadataPath string

	switch metadataType {
	case "icon":
		cmd := exec.Command("sh", "-c", fmt.Sprintf("%s --appimage-extract .DirIcon", appImagePath))
		if err := cmd.Run(); err != nil {
			logMessage("WRN", fmt.Sprintf("Failed to extract .DirIcon from AppImage: %s", appImagePath))
			return ""
		}
		metadataPath = filepath.Join(tempDir, "squashfs-root", ".DirIcon")
	case "desktop":
		cmd := exec.Command("sh", "-c", fmt.Sprintf("%s --appimage-extract *.desktop", appImagePath))
		if err := cmd.Run(); err != nil {
			logMessage("WRN", fmt.Sprintf("Failed to extract .desktop from AppImage: %s", appImagePath))
			return ""
		}
		// Find the first .desktop file in the directory
		files, err := filepath.Glob(filepath.Join(tempDir, "squashfs-root", "*.desktop"))
		if err != nil || len(files) == 0 {
			logMessage("WRN", fmt.Sprintf(".desktop file not found in AppImage: %s", appImagePath))
			return ""
		}
		metadataPath = files[0]
	default:
		logMessage("ERR", fmt.Sprintf("Unknown metadata type: %s", metadataType))
		return ""
	}

	if !fileExists(metadataPath) {
		logMessage("WRN", fmt.Sprintf("%s not found in AppImage: %s", strings.Title(metadataType), appImagePath))
		return ""
	}

	if err := copyFile(metadataPath, outputFile); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to copy %s file: %v", metadataType, err))
		return ""
	}

	logMessage("INF", fmt.Sprintf("Successfully extracted %s to: %s", metadataType, outputFile))
	return outputFile
}
