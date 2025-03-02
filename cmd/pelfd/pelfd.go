package main

import (
	"flag"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

// Version indicates the current PELFD version
const Version = "1.9"

// Options defines the configuration options for the PELFD daemon.
type Options struct {
	DirectoriesToWalk   []string `json:"directories_to_walk"`   // Directories to scan for .AppBundle and .blob files.
	ProbeInterval       int      `json:"probe_interval"`        // Interval in seconds between directory scans.
	IconDir             string   `json:"icon_dir"`              // Directory to store extracted icons.
	AppDir              string   `json:"app_dir"`               // Directory to store .desktop files.
	CorrectDesktopFiles bool     `json:"correct_desktop_files"` // Flag to enable automatic correction of .desktop files.
	IntegrateFormats    []string `json:"integrate_formats"`    // Formats to integrate
}

// Config represents the overall configuration structure for PELFD, including scanning options and a tracker for installed bundles.
type Config struct {
	Options Options                 `json:"options"` // PELFD configuration options.
	Tracker map[string]*BundleEntry `json:"tracker"` // Tracker mapping bundle paths to their metadata entries.
}

// BundleEntry represents metadata associated with an installed bundle.
type BundleEntry struct {
	B3SUM       string `json:"b3sum"`               // B3SUM[0..256] hash of the bundle file.
	Png         string `json:"png,omitempty"`       // Path to the PNG icon file, if extracted.
	Svg         string `json:"svg,omitempty"`       // Path to the SVG icon file, if extracted.
	Desktop     string `json:"desktop,omitempty"`   // Path to the corrected .desktop file, if processed.
	Thumbnail   string `json:"thumbnail,omitempty"` // Path to the 128x128 png thumbnail file, if processed.
	HasMetadata bool   `json:"has_metadata"`        // Indicates if metadata was found.
	// LastUpdated int64  `json:"last_updated"`     // Epoch date when the entry was last updated.
}

func main() {
	usr, err := user.Current()
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to get current user: %v", err))
		return
	}
	if usr.Username == "root" {
		logMessage("ERR", "This program cannot run as root.")
		return
	}

	// User's config directory and config file path
	configDir, err := os.UserConfigDir()
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to determine config directory: %v", err))
		return
	}
	configFilePath := filepath.Join(configDir, "pelfd.json")

	// Command line flags
	version := flag.Bool("version", false, "Print the version number")
	integratePath := flag.String("integrate", "", "Manually integrate a specific file or directory")
	deintegratePath := flag.String("deintegrate", "", "Manually de-integrate a specific file or directory")
	extractPath := flag.String("extract", "", "Extract .DirIcon and .desktop to the specified directory")
	outDir := flag.String("outdir", "", "For use with --extract")
	flag.Parse()

	// Handle version flag
	if *version {
		fmt.Printf("Version: %s\n", Version)
		return
	}

	config := loadConfig(configFilePath, usr.HomeDir)

	// Handle extract flag
	if *extractPath != "" && *outDir != "" {
		if !fileExists(*extractPath) {
			logMessage("ERR", fmt.Sprintf("Specified file for extraction does not exist: %s", *extractPath))
			return
		}
		extractMetadata(*extractPath, config.Options.IconDir, *outDir)
		return
	}

	// Create necessary directories
	os.MkdirAll(config.Options.IconDir, 0755)
	os.MkdirAll(config.Options.AppDir, 0755)

	// Manual integration mode
	if *integratePath != "" {
		integrateBundle(config, []string{*integratePath}, usr.HomeDir, configFilePath)
		return
	}

	// Manual deintegration mode
	if *deintegratePath != "" {
		deintegrateBundle(config, *deintegratePath, configFilePath)
		return
	}

	// Automatic probing loop
	probeInterval := time.Duration(config.Options.ProbeInterval) * time.Second
	for {
		integrateBundle(config, config.Options.DirectoriesToWalk, usr.HomeDir, configFilePath)
		time.Sleep(probeInterval)
	}
}

func integrateBundle(config Config, paths []string, homeDir string, configFilePath string) {
	options := config.Options
	entries := config.Tracker
	changed := false

	refreshBundle := func(bundle string, b3sum string, entry *BundleEntry, options Options) bool {
		if entry == nil || entry.B3SUM != b3sum {
			if isExecutable(bundle) {
				integrateBundleMetadata(bundle, b3sum, entries, options.IconDir, options.AppDir, config)
				return true
			}
			// Bundle is not executable, remove entry
			delete(entries, bundle)
			return false
		}
		return false
	}

	for _, filePath := range paths {
		// Expand the tilde (~) to the user's home directory
		filePath = expand(filePath, homeDir)

		// Check if the path is a file or directory
		info, err := os.Stat(filePath)
		if err != nil {
			logMessage("WRN", fmt.Sprintf("Directory does not exist: %s", filePath))
			continue // Skip this file or handle it as needed
		}

		if info.IsDir() {
			// If it's a directory, process all files within it
			files, err := os.ReadDir(filePath)
			if err != nil {
				logMessage("ERR", fmt.Sprintf("Failed to read directory %s: %v", filePath, err))
				continue // Handle directory read errors
			}

			for _, entry := range files {
				if !entry.Type().IsRegular() {
					logMessage("INF", fmt.Sprintf("Skipping non-regular file in directory: %s", entry.Name()))
					continue // Skip non-regular files (like directories, symlinks, etc.)
				}
				// Process each file within the directory
				filePathToIntegrate := filepath.Join(filePath, entry.Name())
				if !isSupportedFile(filePathToIntegrate, options.IntegrateFormats) {
					continue // Skip files that are not supported
				}
				b3sum := computeB3SUM(filePathToIntegrate)
				if entry, exists := entries[filePathToIntegrate]; exists {
					changed = refreshBundle(filePathToIntegrate, b3sum, entry, options) || changed
					checkAndRecreateFiles(entry, filePathToIntegrate, options, &changed)
				} else {
					logMessage("INF", fmt.Sprintf("New bundle detected: %s", filepath.Base(filePathToIntegrate)))
					changed = refreshBundle(filePathToIntegrate, b3sum, nil, options) || changed
				}
			}
			continue // After processing all files, continue with the next path
		}

		// If it's a regular file, proceed as before
		bundle := filePath
		if !isSupportedFile(bundle, options.IntegrateFormats) {
			continue // Skip files that are not supported
		}
		b3sum := computeB3SUM(bundle)

		// Check if the bundle already exists in entries
		if entry, exists := entries[bundle]; exists {
			changed = refreshBundle(bundle, b3sum, entry, options) || changed
			checkAndRecreateFiles(entry, bundle, options, &changed)
		} else {
			logMessage("INF", fmt.Sprintf("New bundle detected: %s", filepath.Base(bundle)))
			changed = refreshBundle(bundle, b3sum, nil, options) || changed
		}
	}

	// Check for deintegration of non-existing bundles
	for bundlePath := range entries {
		if !fileExists(bundlePath) {
			logMessage("WRN", fmt.Sprintf("Bundle %s does not exist. Deintegrating...", bundlePath))
			deintegrateBundle(config, bundlePath, configFilePath)
			changed = true
		}
	}

	if changed {
		saveConfig(config, configFilePath)
	}
}

func checkAndRecreateFiles(entry *BundleEntry, bundle string, options Options, changed *bool) {
	if entry == nil {
		return
	}

	checkAndRecreateFile := func(filePath *string, param, outputDir, extension string) {
		if *filePath != "" && !fileExists(*filePath) {
			logMessage("WRN", fmt.Sprintf("The file for %s doesn't exist anymore. Re-creating...", filepath.Base(bundle)))
			newFilePath := filepath.Join(outputDir, filepath.Base(remExtension(bundle))+extension)
			*filePath = executeAppBundle(bundle, param, newFilePath)
			if *filePath != "" {
				*changed = true
			}
		}
	}

	// Check and recreate thumbnail if missing
	if entry.Thumbnail != "" && !fileExists(entry.Thumbnail) {
		logMessage("WRN", fmt.Sprintf("The thumbnail file for %s doesn't exist anymore. Generating new thumbnail...", filepath.Base(bundle)))
		thumbnailPath, err := generateThumbnail(bundle, entry.Png)
		if err != nil {
			logMessage("ERR", fmt.Sprintf("Failed to create thumbnail file: %v", err))
		} else {
			entry.Thumbnail = thumbnailPath
			logMessage("INF", fmt.Sprintf("A new thumbnail for %s was created", filepath.Base(bundle)))
			*changed = true
		}
	}

	// Check and recreate PNG icon if missing
	checkAndRecreateFile(&entry.Png, "--pbundle_pngIcon", options.IconDir, ".png")

	// Check and recreate SVG icon if missing
	checkAndRecreateFile(&entry.Svg, "--pbundle_svgIcon", options.IconDir, ".svg")

	// Check and recreate desktop file if missing
	checkAndRecreateFile(&entry.Desktop, "--pbundle_desktop", options.AppDir, ".desktop")
}

func deintegrateBundle(config Config, filePath string, configFilePath string) {
	entries := config.Tracker
	changed := false

	if entry, checked := entries[filePath]; checked && entry != nil {
		cleanupBundle(filePath, entries)
		changed = true
	} else {
		logMessage("WRN", fmt.Sprintf("Bundle %s is not integrated.", filePath))
	}

	// Save config if any changes were made
	if changed {
		logMessage("INF", fmt.Sprintf("Updating %s", configFilePath))
		saveConfig(config, configFilePath)
	}
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
			logMessage("ERR", fmt.Sprintf("Failed to remove file: %s %v", file, err))
		} else {
			logMessage("INF", fmt.Sprintf("Removed file: %s", file))
		}
	}
	delete(entries, path)
}

func extractMetadata(filePath, iconDir, appDir string) {
	baseName := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))

	// Extract .DirIcon
	iconPath := filepath.Join(iconDir, baseName+".png")
	if extractedIcon := extractAppImageMetadata("icon", filePath, iconPath); extractedIcon != "" {
		logMessage("INF", fmt.Sprintf("Icon extracted to: %s", extractedIcon))
	} else {
		logMessage("WRN", "Failed to extract icon")
	}

	// Extract .desktop
	desktopPath := filepath.Join(appDir, baseName+".desktop")
	if extractedDesktop := extractAppImageMetadata("desktop", filePath, desktopPath); extractedDesktop != "" {
		logMessage("INF", fmt.Sprintf("Desktop file extracted to: %s", extractedDesktop))
	} else {
		logMessage("WRN", "Failed to extract desktop file")
	}
}

func isSupportedFile(filePath string, integrateFormats []string) bool {
	if len(integrateFormats) == 0 {
		return strings.HasSuffix(filePath, ".AppBundle") || strings.HasSuffix(filePath, ".AppImage") || strings.HasSuffix(filePath, ".NixAppImage") || strings.HasSuffix(filePath, ".AppDir")
	}
	for _, format := range integrateFormats {
		if strings.HasSuffix(filePath, format) {
			return true
		}
	}
	return false
}

// Function map for handling different formats
var formatHandlers = map[string]func(string, string, *BundleEntry){
	".AppImage": integrateAppImage,
	".NixAppImage": integrateAppImage,
	".AppBundle": integrateAppBundle,
	".AppDir": integrateAppDir,
}

func integrateMetadata(path, b3sum string, entries map[string]*BundleEntry, iconPath, appPath string, cfg Config) {
	entry := &BundleEntry{B3SUM: b3sum, HasMetadata: false}
	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	ext := filepath.Ext(path)
	if handler, ok := formatHandlers[ext]; ok {
		handler(path, appPath, entry)
	} else {
		logMessage("WRN", fmt.Sprintf("Unsupported format: %s", ext))
		return
	}

	if entry.Png != "" || entry.Svg != "" || entry.Desktop != "" {
		entry.HasMetadata = true
		logMessage("INF", fmt.Sprintf("Adding bundle to entries: %s", path))
		entries[path] = entry
	} else {
		logMessage("WRN", fmt.Sprintf("Bundle does not contain any metadata files. Skipping: %s", path))
		entries[path] = entry
	}

	createThumbnailForBundle(entry, path)
	updateDesktopFileIfRequired(path, baseName, appPath, entry, cfg)
}
