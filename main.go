package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

const configFilePath = ".config/blapp.json"

type Options struct {
	DirectoriesToWalk []string `json:"directories_to_walk"`
	ProbeInterval     int      `json:"probe_interval"`
	IconDir           string   `json:"icon_dir"`
	AppDir            string   `json:"app_dir"`
}

type Config struct {
	Options Options               `json:"options"`
	Tracker map[string]*BlobEntry `json:"tracker"`
}

type BlobEntry struct {
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Png     string `json:"png,omitempty"`
	Xpm     string `json:"xpm,omitempty"`
	Desktop string `json:"desktop,omitempty"`
}

func main() {
	log.Println("Starting Blapp daemon")

	usr, err := user.Current()
	if err != nil {
		log.Fatalf("Failed to get current user: %v", err)
	}

	configPath := filepath.Join(usr.HomeDir, configFilePath)
	config := loadConfig(configPath, usr.HomeDir)

	if err := os.MkdirAll(config.Options.IconDir, 0755); err != nil {
		log.Fatalf("Failed to create icons directory: %v", err)
	}

	if err := os.MkdirAll(config.Options.AppDir, 0755); err != nil {
		log.Fatalf("Failed to create applications directory: %v", err)
	}

	probeInterval := time.Duration(config.Options.ProbeInterval) * time.Second

	for {
		processBlobs(config, usr.HomeDir)
		time.Sleep(probeInterval)
	}
}

func loadConfig(configPath, homeDir string) Config {
	config := Config{
		Options: Options{
			DirectoriesToWalk: []string{"~/Programs"},
			ProbeInterval:     90,
			IconDir:           filepath.Join(homeDir, ".local/share/icons"),
			AppDir:            filepath.Join(homeDir, ".local/share/applications"),
		},
		Tracker: make(map[string]*BlobEntry),
	}

	file, err := os.Open(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("Config file does not exist: %s, creating a new one", configPath)
			saveConfig(config, configPath)
			return config
		}
		log.Fatalf("Failed to open config file %s: %v", configPath, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&config); err != nil {
		log.Fatalf("Failed to decode config file %s: %v", configPath, err)
	}

	return config
}

func saveConfig(config Config, path string) {
	file, err := os.Create(path)
	if err != nil {
		log.Fatalf("Failed to save config file: %v", err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(config); err != nil {
		log.Fatalf("Failed to encode config file: %v", err)
	}
}

func processBlobs(config Config, homeDir string) {
	existing := make(map[string]struct{})
	options := config.Options
	entries := config.Tracker
	changed := false

	for _, dir := range options.DirectoriesToWalk {
		dir = strings.Replace(dir, "~", homeDir, 1)
		log.Printf("Scanning directory: %s", dir)
		blobs, err := filepath.Glob(filepath.Join(dir, "*.blob"))
		if err != nil {
			log.Printf("Failed to scan directory %s for .blob files: %v", dir, err)
			continue
		}

		for _, blob := range blobs {
			// VERBOSITY: log.Printf("Found blob: %s", blob)
			existing[blob] = struct{}{}

			sha := computeSHA(blob)
			if entry, checked := entries[blob]; checked {
				if entry == nil {
					// VERBOSITY: log.Printf("Entry for blob %s is nil, skipping", blob)
					continue
				}

				if entry.SHA != sha {
					if isExecutable(blob) {
						processBlob(blob, sha, entries, options.IconDir, options.AppDir)
						changed = true
					} else {
						// VERBOSITY: log.Printf("Blob is not executable: %s", blob)
						entries[blob] = nil
					}
				} // VERBOSITY: else {log.Printf("Blob already processed and unchanged: %s", blob)}
			} else {
				if isExecutable(blob) {
					processBlob(blob, sha, entries, options.IconDir, options.AppDir)
					changed = true
				} else {
					// VERBOSITY: log.Printf("Blob is not executable: %s", blob)
					entries[blob] = nil
				}
			}
		}
	}

	for path := range entries {
		if _, found := existing[path]; !found {
			log.Printf("Blob no longer exists: %s", path)
			cleanupBlob(path, entries, options.IconDir, options.AppDir)
			changed = true
		}
	}

	if changed {
		log.Printf("Updating config: %s", filepath.Join(homeDir, configFilePath))
		saveConfig(config, filepath.Join(homeDir, configFilePath))
	}
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		log.Printf("Failed to stat file %s: %v", path, err)
		return false
	}
	mode := info.Mode()
	return mode&0111 != 0
}

func computeSHA(path string) string {
	file, err := os.Open(path)
	if err != nil {
		log.Printf("Failed to open file %s: %v", path, err)
		return ""
	}
	defer file.Close()

	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		log.Printf("Failed to compute SHA256 for file %s: %v", path, err)
		return ""
	}

	return hex.EncodeToString(hasher.Sum(nil))
}

func processBlob(path, sha string, entries map[string]*BlobEntry, iconPath, appPath string) {
	entry := &BlobEntry{Path: path, SHA: sha}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	entry.Png = executeBlob(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Xpm = executeBlob(path, "--pbundle_xpmIcon", filepath.Join(iconPath, baseName+".xpm"))
	entry.Desktop = executeBlob(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Xpm != "" || entry.Desktop != "" {
		log.Printf("Adding blob to entries: %s", path)
		entries[path] = entry
	} else {
		log.Printf("Blob does not contain required files: %s", path)
		entries[path] = nil
	}
}

func executeBlob(blob, param, outputFile string) string {
	log.Printf("Executing blob: %s with parameter: %s", blob, param)
	cmd := exec.Command(blob, param)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Blob %s with parameter %s returned error code 1", blob, param)
		return ""
	}

	outputStr := string(output)
	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		log.Printf("Failed to decode base64 output for %s %s: %v", blob, param, err)
		return ""
	}

	if err := ioutil.WriteFile(outputFile, data, 0644); err != nil {
		log.Printf("Failed to write file %s: %v", outputFile, err)
		return ""
	}

	log.Printf("Successfully wrote file: %s", outputFile)
	return outputFile
}

func cleanupBlob(path string, entries map[string]*BlobEntry, iconPath, appPath string) {
	if entry, found := entries[path]; found && entry != nil {
		if entry.Png != "" {
			os.Remove(entry.Png)
		}
		if entry.Xpm != "" {
			os.Remove(entry.Xpm)
		}
		if entry.Desktop != "" {
			os.Remove(entry.Desktop)
		}
		delete(entries, path)
	}
}
