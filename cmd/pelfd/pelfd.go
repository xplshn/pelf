// PELFD. Daemon that automatically "installs" .AppBundles by checking their metadata,
package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/goccy/go-json"
	"github.com/liamg/tml"
)

// Version indicates the current PELFD version
const Version = "1.6"

// Options defines the configuration options for the PELFD daemon.
type Options struct {
	DirectoriesToWalk   []string `json:"directories_to_walk"`   // Directories to scan for .AppBundle and .blob files.
	ProbeInterval       int      `json:"probe_interval"`        // Interval in seconds between directory scans.
	IconDir             string   `json:"icon_dir"`              // Directory to store extracted icons.
	AppDir              string   `json:"app_dir"`               // Directory to store .desktop files.
	ProbeExtensions     []string `json:"probe_extensions"`      // File extensions to probe within directories.
	CorrectDesktopFiles bool     `json:"correct_desktop_files"` // Flag to enable automatic correction of .desktop files.
}

// Config represents the overall configuration structure for PELFD, including scanning options and a tracker for installed bundles.
type Config struct {
	Options Options                 `json:"options"` // PELFD configuration options.
	Tracker map[string]*BundleEntry `json:"tracker"` // Tracker mapping bundle paths to their metadata entries.
}

// BundleEntry represents metadata associated with an installed bundle.
type BundleEntry struct {
	Path      string `json:"path"`                // Full path to the bundle file.
	SHA       string `json:"sha"`                 // SHA256 hash of the bundle file.
	Png       string `json:"png,omitempty"`       // Path to the PNG icon file, if extracted.
	Svg       string `json:"svg,omitempty"`       // Path to the SVG icon file, if extracted.
	Desktop   string `json:"desktop,omitempty"`   // Path to the corrected .desktop file, if processed.
	Thumbnail string `json:"thumbnail,omitempty"` // Path to the 128x128 png thumbnail file, if processed.
}

func main() {
	// Check if the program is running as root
	usr, err := user.Current()
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to get current user: <yellow>%v</yellow>", err))
	}
	if usr.Username == "root" {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> This program cannot run as <yellow>root</yellow>."))
	}
	configDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to determine config directory: <yellow>%v</yellow>", err))
	}
	// Parse command-line flags
	version := flag.Bool("version", false, "Print the version number")
	configFilePath := flag.String("config", filepath.Join(configDir, "pelfd.json"), "Specify a custom configuration file")
	flag.Parse()
	if *version {
		fmt.Printf("Version: %s\n", Version)
		os.Exit(0)
	}

	config := loadConfig(*configFilePath, usr.HomeDir)
	if err := os.MkdirAll(config.Options.IconDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create icons directory: <yellow>%v</yellow>", err))
	}
	if err := os.MkdirAll(config.Options.AppDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create applications directory: <yellow>%v</yellow>", err))
	}
	probeInterval := time.Duration(config.Options.ProbeInterval) * time.Second
	for {
		processBundle(config, usr.HomeDir, *configFilePath)
		time.Sleep(probeInterval)
	}
}

func loadConfig(configPath string, homeDir string) Config {
	config := Config{
		Options: Options{
			DirectoriesToWalk:   []string{"~/Programs"},
			ProbeInterval:       90,
			IconDir:             filepath.Join(homeDir, ".local/share/icons"),
			AppDir:              filepath.Join(homeDir, ".local/share/applications"),
			ProbeExtensions:     []string{".AppBundle", ".blob", ".AppIBundle"},
			CorrectDesktopFiles: true,
		},
		Tracker: make(map[string]*BundleEntry),
	}

	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Config file does not exist: <green>%s</green>, creating a new one", configPath))
			saveConfig(config, configPath)
			return config
		}
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to open config file <yellow>%s</yellow> %v", configPath, err))
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to decode config file: <yellow>%v</yellow>", err))
	}

	return config
}

func saveConfig(config Config, path string) {
	file, err := os.Create(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to save config file: <yellow>%v</yellow>", err))
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to encode config file: <yellow>%v</yellow>", err))
	}
}

func processBundle(config Config, homeDir string, configFilePath string) {
	existing := make(map[string]struct{})
	options := config.Options
	entries := config.Tracker
	changed := false
	for _, dir := range options.DirectoriesToWalk {
		dir = strings.Replace(dir, "~", homeDir, 1)
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Scanning directory: <green>%s</green>", dir))
		for _, ext := range options.ProbeExtensions {
			bundles, err := filepath.Glob(filepath.Join(dir, "*"+ext))
			if err != nil {
				log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to scan directory <yellow>%s</yellow> for <yellow>%s</yellow> files: %v", dir, ext, err))
			}
			for _, bundle := range bundles {
				existing[bundle] = struct{}{}
				sha := computeSHA(bundle)
				if entry, checked := entries[bundle]; checked {
					if entry == nil {
						continue
					}
					// Check if the SHA has changed
					if entry.SHA != sha {
						log.Println(tml.Sprintf("<yellow><bold>WRN:</yellow></red> The SHA of <blue>%s</blue> has changed. Refreshing entry and files...", filepath.Base(bundle)))
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
							changed = true
						} else {
							entries[bundle] = nil
						}
						// Or if at least one of the required files are missing
					} else if (entry.Desktop != "" && !fileExists(entry.Desktop)) ||
						(entry.Png != "" && !fileExists(entry.Png)) ||
						(entry.Svg != "" && !fileExists(entry.Svg)) {
						log.Println(tml.Sprintf("<yellow><bold>WRN:</yellow></red> One or more required files for <blue>%s</blue> are missing. Refreshing entry and files...", filepath.Base(bundle)))
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
							changed = true
						} else {
							entries[bundle] = nil
						}
					}
					// Check if the bundle's thumbnail has been removed
					if entry.Thumbnail != "" && !fileExists(entry.Thumbnail) {
						log.Println(tml.Sprintf("<yellow><bold>WRN:</yellow></red> The thumbnail file for <blue>%s</blue> doesn't exist anymore. Generating new thumbnail...", filepath.Base(bundle)))
						thumbnailPath, err := generateThumbnail(bundle, entry.Png)
						if err != nil {
							log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create thumbnail file: <yellow>%v</yellow>", err))
						}
						entry.Thumbnail = thumbnailPath
						log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> A new thumbnail for <green>%s</green> was created", filepath.Base(bundle)))
						changed = true
					}
				} else {
					// The bundle is not an entry in the config's tracker
					log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> New bundle detected: <green>%s</green>", filepath.Base(bundle)))
					if isExecutable(bundle) {
						processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
						changed = true
					} else {
						entries[bundle] = nil
					}
				}
			}
		}
	}
	for path := range entries {
		if _, found := existing[path]; !found {
			log.Println(tml.Sprintf("<yellow><bold>WRN:</yellow></red> <blue>%s</blue> no longer exists", path))
			cleanupBundle(path, entries)
			changed = true
		}
	}
	if changed {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Updating <green>%s</green>", configFilePath))
		saveConfig(config, configFilePath)
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to stat file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return false
	}
	mode := info.Mode()
	return mode&0111 != 0
}

func computeSHA(path string) string {
	file, err := os.Open(path)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to open file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return ""
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to compute SHA256 of <yellow>%s</yellow>: <red>%v</red>", path, err))
		return ""
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

func processBundles(path, sha string, entries map[string]*BundleEntry, iconPath, appPath string, cfg Config) {
	entry := &BundleEntry{Path: path, SHA: sha}
	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	entry.Png = executeBundle(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Svg = executeBundle(path, "--pbundle_svgIcon", filepath.Join(iconPath, baseName+".svg"))
	entry.Desktop = executeBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Svg != "" || entry.Desktop != "" {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Adding bundle to entries: <green>%s</green>", path))
		entries[path] = entry
	} else {
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle does not contain any metadata files. Skipping: <blue>%s</blue>", path))
		entries[path] = nil
	}

	// Create a thumbnail for file managers. See: https://specifications.freedesktop.org/thumbnail-spec/thumbnail-spec-latest.html#CREATION for details
	if entry.Png != "" {
		thumbnailPath, err := generateThumbnail(path, entry.Png)
		if err != nil {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create thumbnail file: <yellow>%v</yellow>", err))
		}
		entry.Thumbnail = thumbnailPath
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> A thumbnail for <green>%s</green> was created at: <cyan>%s</cyan>", path, thumbnailPath))
	}

	// Handle .desktop files
	desktopPath := filepath.Join(appPath, baseName+".desktop")
	if _, err := os.Stat(desktopPath); err == nil {
		content, err := os.ReadFile(desktopPath)
		if err != nil {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to read .desktop file: <yellow>%v</yellow>", err))
			return
		}
		if cfg.Options.CorrectDesktopFiles {
			updatedContent, err := updateDesktopFile(string(content), path, entry)
			if err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to update .desktop file: <yellow>%v</yellow>", err))
				return
			}
			if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove existing .desktop file: <yellow>%v</yellow>", err))
				return
			}
			// Write the updated content back to the .desktop file
			if err := os.WriteFile(desktopPath, []byte(updatedContent), 0644); err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write updated .desktop file: <yellow>%v</yellow>", err))
				return
			}
		}
	}
}

func executeBundle(bundle, param, outputFile string) string {
	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Retrieving metadata from <green>%s</green> with parameter: <cyan>%s</cyan>", bundle, param))
	// Prepend `sh -c` to the bundle execution
	cmd := exec.Command("sh", "-c", bundle+" "+param)
	output, err := cmd.Output()
	if err != nil {
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle <blue>%s</blue> with parameter <cyan>%s</cyan> didn't return a metadata file", bundle, param))
		return ""
	}

    outputStr := string(output)

	// Remove the escape sequence "^[[1F^[[2K"
	// Remove the escape sequence from the output
	outputStr = strings.ReplaceAll(outputStr, "\x1b[1F\x1b[2K", "")

	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to decode base64 output for <yellow>%s</yellow> <yellow>%s</yellow>: <red>%v</red>", bundle, param, err))
		return ""
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write file <yellow>%s</yellow>: <red>%v</red>", outputFile, err))
		return ""
	}

	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Successfully wrote file: <green>%s</green>", outputFile))
	return outputFile
}

func cleanupBundle(path string, entries map[string]*BundleEntry) {
	entry := entries[path]
	if entry == nil {
		return
	}
	filesToRemove := []string{entry.Png, entry.Svg, entry.Desktop, entry.Thumbnail}
	for _, file := range filesToRemove {
		if file == "" {
			continue
		}
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove file: <yellow>%s</yellow> <red>%v</red>", file, err))
		} else {
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Removed file: <green>%s</green>", file))
		}
	}
	delete(entries, path)
}

func updateDesktopFile(content, bundlePath string, entry *BundleEntry) (string, error) {
	// Correct Exec line
	updatedExec := fmt.Sprintf("Exec=%s", bundlePath)

	// Define a regular expression to match the Exec line.
	reExec := regexp.MustCompile(`(?m)^Exec=.*$`)
	content = reExec.ReplaceAllString(content, updatedExec)
	log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"Exec=\" line. <green>It has been corrected</green>", bundlePath))

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
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"Icon=\" line. <green>It has been corrected</green>", bundlePath))
	}

	// Only update the TryExec line if it is present
	reTryExec := regexp.MustCompile(`(?m)^TryExec=.*$`)
	if reTryExec.MatchString(content) {
		newTryExecLine := fmt.Sprintf("TryExec=%s", filepath.Base(bundlePath))
		content = reTryExec.ReplaceAllString(content, newTryExecLine)
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> The bundled .desktop file (<blue>%s</blue>) had an incorrect \"TryExec=\" line. <green>It has been corrected</green>", bundlePath))
	}

	return content, nil
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

func generateThumbnail(path string, png string) (string, error) {
	// Generate the canonical URI for the file path
	canonicalURI, err := CanonicalURI(path)
	if err != nil {
		log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Couldn't generate canonical URI: <yellow>%v</yellow>", err))
		return "", err
	}

	// Compute the MD5 hash of the canonical URI
	fileMD5 := HashURI(canonicalURI)

	// Determine the thumbnail path
	getThumbnailPath, err := getThumbnailPath(fileMD5, "normal")
	if err != nil {
		log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Couldn't generate an appropriate thumbnail path: <yellow>%v</yellow>", err))
		return "", err
	}

	// Copy the PNG file to the thumbnail path
	err = CopyFile(png, getThumbnailPath)
	if err != nil {
		log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create thumbnail file: <yellow>%v</yellow>", err))
		return "", err
	}

	return getThumbnailPath, nil
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

// CopyFile copies a file from src to dst.
func CopyFile(src, dst string) error {
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
