package main

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/goccy/go-json"
	"github.com/liamg/tml"
	"github.com/zeebo/blake3"
)

var (
	progressDialog *dialog.CustomDialog
	messageLabel   *widget.Label
	progressBar    *widget.ProgressBar
	dialogMutex    sync.Mutex
	lastUpdate     time.Time
	fyneApp        = app.New()
	fyneWindow     = fyneApp.NewWindow("pelfd is working...")
)

func logMessage(level, message string) string {
	logColors := map[string]string{
		"INF": "<blue><bold>INF:</bold></blue>",
		"WRN": "<yellow><bold>WRN:</bold></yellow>",
		"ERR": "<red><bold>ERR:</bold></red>",
	}

	color, exists := logColors[level]
	if !exists {
		color = "<white><bold>LOG:</bold></white>"
	}

	formattedMessage := tml.Sprintf(fmt.Sprintf("%s %s", color, message))
	log.Println(formattedMessage)

	// Reset progress and timestamp on each log message call
	dialogMutex.Lock()
	defer dialogMutex.Unlock()

	// Initialize the progress bar if not already created
	if progressBar == nil {
		progressBar = widget.NewProgressBar()
		progressBar.SetValue(0.05) // Set initial value to 5%

		messageLabel = widget.NewLabel("")

		fyneWindow.SetContent(container.NewVBox(messageLabel, progressBar))
		fyneWindow.Resize(fyne.NewSize(400, 100))
		fyneWindow.Show()
		lastUpdate = time.Now()
	}

	// Update the message label with the current message
	messageLabel.SetText(removeAnsi(formattedMessage))

	// Start a goroutine for continuous update
	go updateProgressBar()

	return fmt.Sprintf("%s %s", level, message)
}

func updateProgressBar() {
	for {
		time.Sleep(40 * time.Millisecond) // Update interval of 40ms
		dialogMutex.Lock()

		// Check if 2 seconds have passed since the last logMessage call
		if time.Since(lastUpdate) > 2*time.Second {
			if progressBar.Value < 1.0 {
				progressBar.SetValue(progressBar.Value + 0.05) // Increment progress by 5%
			} else {
				// Hide the window when progress reaches 100%
				fyneWindow.Hide()
				dialogMutex.Unlock()
				break // Stop the goroutine when progress reaches 100%
			}
		} else {
			// Update if there's recent activity, by 15%
			progressBar.SetValue(progressBar.Value + 0.15)
		}
		dialogMutex.Unlock()
	}
}

func createThumbnailForBundle(entry *BundleEntry, path string) {
	if entry.Png != "" {
		thumbnailPath, err := generateThumbnail(path, entry.Png)
		if err != nil {
			logMessage("ERR", fmt.Sprintf("Failed to create thumbnail file: <red>%v</red>", err))
		}
		entry.Thumbnail = thumbnailPath
		logMessage("INF", fmt.Sprintf("A thumbnail for %s was created at: %s", path, thumbnailPath))
	}
}

func updateDesktopFileIfRequired(path, baseName, appPath string, entry *BundleEntry, cfg Config) {
	desktopPath := filepath.Join(appPath, baseName+".desktop")
	if _, err := os.Stat(desktopPath); err == nil {
		content, err := os.ReadFile(desktopPath)
		if err != nil {
			logMessage("ERR", fmt.Sprintf("Failed to read .desktop file: <red>%v</red>", err))
			return
		}
		if cfg.Options.CorrectDesktopFiles {
			updatedContent, err := updateDesktopFile(string(content), path, entry)
			if err != nil {
				logMessage("ERR", fmt.Sprintf("Failed to update .desktop file: <red>%v</red>", err))
				return
			}
			if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
				logMessage("ERR", fmt.Sprintf("Failed to remove existing .desktop file: <red>%v</red>", err))
				return
			}
			if err := os.WriteFile(desktopPath, []byte(updatedContent), 0644); err != nil {
				logMessage("ERR", fmt.Sprintf("Failed to write updated .desktop file: <red>%v</red>", err))
				return
			}
		}
	}
}

func loadConfig(configPath string, homeDir string) Config {
	config := Config{
		Options: Options{
			DirectoriesToWalk:   []string{"~/Applications"},
			ProbeInterval:       5,
			IconDir:             filepath.Join(homeDir, ".local/share/icons"),
			AppDir:              filepath.Join(homeDir, ".local/share/applications"),
			CorrectDesktopFiles: true,
		},
		Tracker: make(map[string]*BundleEntry),
	}

	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			logMessage("INF", fmt.Sprintf("Config file does not exist: %s, creating a new one", configPath))
			saveConfig(config, configPath)
			return config
		}
		logMessage("ERR", fmt.Sprintf("Failed to open config file %s: <red>%v</red>", configPath, err))
		os.Exit(1)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to decode config file: <red>%v</red>", err))
		os.Exit(1)
	}

	return config
}

func saveConfig(config Config, path string) {
	file, err := os.Create(path)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to save config file: <red>%v</red>", err))
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to encode config file: <red>%v</red>", err))
		os.Exit(1)
	}
}

// fileExists checks if a file exists.
func fileExists(filePath string) bool {
	_, err := os.Stat(filePath)
	if err == nil {
		return true
	}
	if os.IsNotExist(err) {
		return false
	}
	// If there's any other error, we consider that the file doesn't exist for simplicity
	return false
}

func remExtension(filePath string) string {
	return strings.Split(filePath, ".")[0]
}

func expand(filePath, homeDir string) string {
	// Expand the tilde (~) to the user's home directory
	if strings.HasPrefix(filePath, "~") {
		filePath = filepath.Join(homeDir, filePath[1:]) // Replace ~ with the home directory
	}
	return filePath
}

// HashURI computes the MD5 hash of the canonical URI.
func HashURI(uri string) string {
	hash := md5.Sum([]byte(uri))
	return hex.EncodeToString(hash[:])
}

// CanonicalURI generates the canonical URI for a given file path.
func CanonicalURI(filePath string) (string, error) {
	absPath, err := filepath.Abs(filePath)
	if err != nil {
		return "", err
	}
	uri := url.URL{Scheme: "file", Path: absPath}
	return uri.String(), nil
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to stat file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return false
	}
	mode := info.Mode()
	return mode&0111 != 0
}

// isDirectory checks if the given path is a directory.
func isDirectory(path string) bool {
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false // Path does not exist
	}
	return err == nil && info.IsDir() // Check for error and if it's a directory
}

// computeB3SUM computes the Blake3 hash of the file at the given path.
func computeB3SUM(path string) string {
	file, err := os.Open(path)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to open file %s: <red>%v</red>", path, err))
		return ""
	}
	defer file.Close()

	hasher := blake3.New()
	if _, err := io.Copy(hasher, file); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to compute Blake3 hash of %s: <red>%v</red>", path, err))
		os.Exit(1)
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

// ThumbnailPath returns the path where the thumbnail should be saved.
func getThumbnailPath(fileMD5 string, thumbnailType string) (string, error) {
	// Determine the base directory for thumbnails
	baseDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	thumbnailDir := filepath.Join(baseDir, "thumbnails")

	// Determine the size directory based on thumbnail type
	sizeDir := ""
	switch thumbnailType {
	case "normal":
		sizeDir = "normal"
	case "large":
		sizeDir = "large"
	default:
		return "", fmt.Errorf("invalid thumbnail type: %s", thumbnailType)
	}

	// Create the full directory path
	fullDir := filepath.Join(thumbnailDir, sizeDir)
	err = os.MkdirAll(fullDir, os.ModePerm)
	if err != nil {
		return "", err
	}

	// Create the final path for the thumbnail
	thumbnailPath := filepath.Join(fullDir, fileMD5+".png")

	return thumbnailPath, nil
}

// copyFile copies a file from src to dst.
func copyFile(src, dst string) error {
	// Open the source file
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	// Create the destination file
	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	// Copy the content from source to destination
	_, err = io.Copy(dstFile, srcFile)
	if err != nil {
		return err
	}

	return nil
}

func updateDesktopFile(content, bundlePath string, entry *BundleEntry) (string, error) {
	// Correct Exec line
	updatedExec := fmt.Sprintf("Exec=%s", bundlePath)

	// Define a regular expression to match the Exec line.
	reExec := regexp.MustCompile(`(?m)^Exec=.*$`)
	content = reExec.ReplaceAllString(content, updatedExec)
	logMessage("WRN", fmt.Sprintf("The bundled .desktop file (%s) had an incorrect \"Exec=\" line. It has been corrected", bundlePath))

	// Determine the icon format based on the available icon paths
	var icon string
	if entry.Png != "" {
		icon = entry.Png
	} else if entry.Svg != "" {
		icon = entry.Svg
	}

	// Correct Icon line
	reIcon := regexp.MustCompile(`(?m)^Icon=.*$`)
	if icon != "" {
		newIconLine := fmt.Sprintf("Icon=%s", icon)
		content = reIcon.ReplaceAllString(content, newIconLine)
		logMessage("WRN", fmt.Sprintf("The bundled .desktop file (%s) had an incorrect \"Icon=\" line. It has been corrected", bundlePath))
	}

	// Only update the TryExec line if it is present
	reTryExec := regexp.MustCompile(`(?m)^TryExec=.*$`)
	if reTryExec.MatchString(content) {
		newTryExecLine := fmt.Sprintf("TryExec=%s", filepath.Base(bundlePath))
		content = reTryExec.ReplaceAllString(content, newTryExecLine)
		logMessage("WRN", fmt.Sprintf("The bundled .desktop file (%s) had an incorrect \"TryExec=\" line. It has been corrected", bundlePath))
	}

	return content, nil
}

func generateThumbnail(path string, png string) (string, error) {
	// Generate the canonical URI for the file path
	canonicalURI, err := CanonicalURI(path)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Couldn't generate canonical URI: <red>%v</red>", err))
		return "", err
	}

	// Compute the MD5 hash of the canonical URI
	fileMD5 := HashURI(canonicalURI)

	// Determine the thumbnail path
	getThumbnailPath, err := getThumbnailPath(fileMD5, "normal")
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Couldn't generate an appropriate thumbnail path: <red>%v</red>", err))
		return "", err
	}

	// Copy the PNG file to the thumbnail path
	err = copyFile(png, getThumbnailPath)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to create thumbnail file: <red>%v</red>", err))
		return "", err
	}

	return getThumbnailPath, nil
}

// hashChanged checks if the file at filePath has a different hash than what's recorded in config.
// Returns true if the hash is different or the file is not tracked.
func hashChanged(filePath string, config Config) bool {
	// Check if the filePath exists in the config's tracker
	entry, exists := config.Tracker[filePath]
	if !exists {
		return true // File is not tracked, treat as a change
	}

	// Compute the current hash of the file
	currentHash := computeB3SUM(filePath)

	// Compare with the stored hash
	return entry.B3SUM != currentHash
}

// removeNonPrintable removes non-printable characters from a string, including ANSI escape codes.
func removeAnsi(s string) string {
	ansiEscape := regexp.MustCompile(`\x1B\[[0-?9;]*[mK]`)
	s = ansiEscape.ReplaceAllString(s, "")
	return s
}
