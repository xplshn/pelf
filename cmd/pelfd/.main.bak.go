package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/liamg/tml"
)

const configFilePath = ".config/pelfd.json"

type Options struct {
	DirectoriesToWalk   []string `json:"directories_to_walk"`
	ProbeInterval       int      `json:"probe_interval"`
	IconDir             string   `json:"icon_dir"`
	AppDir              string   `json:"app_dir"`
	ProbeExtensions     []string `json:"probe_extensions"`
	CorrectDesktopFiles bool     `json:"correct_desktop_files"`
}

type Config struct {
	Options Options                 `json:"options"`
	Tracker map[string]*BundleEntry `json:"tracker"`
}

type BundleEntry struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Png     string `json:"png,omitempty"`
	Xpm     string `json:"xpm,omitempty"`
	Svg     string `json:"svg,omitempty"`
	Desktop string `json:"desktop,omitempty"`
}

func main() {
	log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Starting <green>pelfd</green> daemon"))

	usr, err := user.Current()
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to get current user: <yellow>%v</yellow>", err))
	}

	configPath := filepath.Join(usr.HomeDir, configFilePath)
	config := loadConfig(configPath, usr.HomeDir)

	if err := os.MkdirAll(config.Options.IconDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create icons directory: <yellow>%v</yellow>", err))
	}

	if err := os.MkdirAll(config.Options.AppDir, 0755); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to create applications directory: <yellow>%v</yellow>", err))
	}

	probeInterval := time.Duration(config.Options.ProbeInterval) * time.Second

	for {
		processBundle(config, usr.HomeDir)
		time.Sleep(probeInterval)
	}
}

func loadConfig(configPath, homeDir string) Config {
	config := Config{
		Options: Options{
			DirectoriesToWalk:   []string{"~/Programs"},
			ProbeInterval:       90,
			IconDir:             filepath.Join(homeDir, ".local/share/icons"),
			AppDir:              filepath.Join(homeDir, ".local/share/applications"),
			ProbeExtensions:     []string{".AppBundle", ".blob"},
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

func processBundle(config Config, homeDir string) {
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
				continue
			}

			for _, bundle := range bundles {
				existing[bundle] = struct{}{}

				sha := computeSHA(bundle)
				if entry, checked := entries[bundle]; checked {
					if entry == nil {
						continue
					}

					if entry.SHA != sha {
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir, config)
							changed = true
						} else {
							entries[bundle] = nil
						}
					}
				} else {
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
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Bundle no longer exists: <yellow>%s</yellow>", path))
			cleanupBundle(path, entries, options.IconDir, options.AppDir)
			changed = true
		}
	}

	if changed {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Updating config: <green>%s</green>", filepath.Join(homeDir, configFilePath)))
		saveConfig(config, filepath.Join(homeDir, configFilePath))
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
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to compute SHA256 for file <yellow>%s</yellow>: <red>%v</red>", path, err))
		return ""
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

func processBundles(path, sha string, entries map[string]*BundleEntry, iconPath, appPath string, cfg Config) {
	entry := &BundleEntry{Path: path, SHA: sha}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	desktopPath := filepath.Join(appPath, baseName+".desktop")

	if _, err := os.Stat(desktopPath); err == nil {
		content, err := os.ReadFile(desktopPath)
		if err != nil {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to read .desktop file: %v", err))
			return
		}
		if cfg.Options.CorrectDesktopFiles {
			updatedContent := updateExecLine(string(content), path)

			// Remove the existing .desktop file before writing the updated content
			if err := os.Remove(desktopPath); err != nil && !os.IsNotExist(err) {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove existing .desktop file: %v", err))
				return
			}

			if err := os.WriteFile(desktopPath, []byte(updatedContent), 0644); err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write updated .desktop file: %v", err))
				return
			}

			// Add a small delay to observe the file changes
			//time.Sleep(100 * time.Millisecond)

			contentAfterUpdate, err := os.ReadFile(desktopPath)
			if err != nil {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to read .desktop file after update: %v", err))
				return
			}
			if string(contentAfterUpdate) != updatedContent {
				log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> The .desktop file was not updated correctly."))
				return
			}
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> The .desktop file in the bundle was corrected."))
		}
	}

	entry.Png = executeBundle(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Xpm = executeBundle(path, "--pbundle_xpmIcon", filepath.Join(iconPath, baseName+".xpm"))
	entry.Svg = executeBundle(path, "--pbundle_svgIcon", filepath.Join(iconPath, baseName+".svg"))
	entry.Desktop = executeBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Xpm != "" || entry.Desktop != "" {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Adding bundle to entries: <green>%s</green>", path))
		entries[path] = entry
	} else {
		log.Println(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle does not contain any metadata files. Skipping: <blue>%s</blue>", path))
		entries[path] = nil
	}
}

/*
func updateExecLine(content, bundlePath string) string {
	execRegex := regexp.MustCompile(`(?m)^Exec=.*$`)
	newExecLine := fmt.Sprintf("Exec=%s", bundlePath)
	return execRegex.ReplaceAllString(content, newExecLine)
}
*/

func updateExecLine(content, bundlePath string) string {
	// Check if the bundle is on the system's path.
	lookPath, err := exec.LookPath(filepath.Base(bundlePath))
	updatedExec := "nil"
	if err != nil {
		// The bundle is not on the system's path, use the full path.
		updatedExec = fmt.Sprintf("Exec=%s", bundlePath)
	} else {
		// The bundle is on the system's path, use just the name of the bundle.
		updatedExec = fmt.Sprintf("Exec=%s", filepath.Base(lookPath))
	}
	// Define a regular expression to match the Exec line.
	re := regexp.MustCompile(`(?m)^Exec=\S+`)
	// Replace the first occurrence of the Exec line with the new one.
	return re.ReplaceAllString(content, updatedExec)
}

func cleanupBundle(path string, entries map[string]*BundleEntry, iconDir, appDir string) {
	entry := entries[path]
	if entry == nil {
		return
	}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	pngPath := filepath.Join(iconDir, baseName+".png")
	xpmPath := filepath.Join(iconDir, baseName+".xpm")
	desktopPath := filepath.Join(appDir, baseName+".desktop")

	filesToRemove := []string{pngPath, xpmPath, desktopPath}
	for _, file := range filesToRemove {
		if err := os.Remove(file); err != nil && !os.IsNotExist(err) {
			log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to remove file: <yellow>%s</yellow> <red>%v</red>", file, err))
		} else {
			log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Removed file: <green>%s</green>", file))
		}
	}

	delete(entries, path)
}

func executeBundle(path, command, outputPath string) string {
	cmd := exec.Command(path, command, outputPath)
	if err := cmd.Run(); err != nil {
		log.Println(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to execute bundle command <yellow>%s</yellow> on bundle <yellow>%s</yellow>: <red>%v</red>", command, path, err))
		return ""
	}

	if _, err := os.Stat(outputPath); err == nil {
		log.Println(tml.Sprintf("<blue><bold>INF:</bold></blue> Created file: <green>%s</green>", outputPath))
		return outputPath
	}

	return ""
}
