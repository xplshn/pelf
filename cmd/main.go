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

const configFilePath = ".config/pelfd.json"

type Options struct {
	DirectoriesToWalk []string `json:"directories_to_walk"`
	ProbeInterval     int      `json:"probe_interval"`
	IconDir           string   `json:"icon_dir"`
	AppDir            string   `json:"app_dir"`
	ProbeExtensions   []string `json:"probe_extensions"`
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
	Desktop string `json:"desktop,omitempty"`
}

func main() {
	log.Println("Starting pelfd daemon")

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
		processBundle(config, usr.HomeDir)
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
			ProbeExtensions:   []string{".AppBundle", ".blob"},
		},
		Tracker: make(map[string]*BundleEntry),
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

func processBundle(config Config, homeDir string) {
	existing := make(map[string]struct{})
	options := config.Options
	entries := config.Tracker
	changed := false

	for _, dir := range options.DirectoriesToWalk {
		dir = strings.Replace(dir, "~", homeDir, 1)
		log.Printf("Scanning directory: %s", dir)

		for _, ext := range options.ProbeExtensions {
			bundles, err := filepath.Glob(filepath.Join(dir, "*"+strings.ToLower(ext)))
			if err != nil {
				log.Printf("Failed to scan directory %s for %s files: %v", dir, ext, err)
				continue
			}

			for _, bundle := range bundles {
				bundle = strings.ToLower(bundle)
				// VERBOSITY: log.Printf("Found bundle: %s", bundle)
				existing[bundle] = struct{}{}

				sha := computeSHA(bundle)
				if entry, checked := entries[bundle]; checked {
					if entry == nil {
						// VERBOSITY: log.Printf("Entry for bundle %s is nil, skipping", bundle)
						continue
					}

					if entry.SHA != sha {
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir)
							changed = true
						} else {
							// VERBOSITY: log.Printf("Bundle is not executable: %s", bundle)
							entries[bundle] = nil
						}
					} // VERBOSITY: else {log.Printf("Bundle already processed and unchanged: %s", bundle)}
				} else {
					if isExecutable(bundle) {
						processBundles(bundle, sha, entries, options.IconDir, options.AppDir)
						changed = true
					} else {
						// VERBOSITY: log.Printf("Bundle is not executable: %s", bundle)
						entries[bundle] = nil
					}
				}
			}
		}
	}

	for path := range entries {
		if _, found := existing[path]; !found {
			log.Printf("Bundle no longer exists: %s", path)
			cleanupBundle(path, entries, options.IconDir, options.AppDir)
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

func processBundles(path, sha string, entries map[string]*BundleEntry, iconPath, appPath string) {
	entry := &BundleEntry{Path: path, SHA: sha}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	entry.Png = executeBundle(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Xpm = executeBundle(path, "--pbundle_xpmIcon", filepath.Join(iconPath, baseName+".xpm"))
	entry.Desktop = executeBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Xpm != "" || entry.Desktop != "" {
		log.Printf("Adding bundle to entries: %s", path)
		entries[path] = entry
	} else {
		log.Printf("Bundle does not contain required files: %s", path)
		entries[path] = nil
	}
}

func executeBundle(bundle, param, outputFile string) string {
	log.Printf("Retrieving metadata from %s with parameter: %s", bundle, param)
	cmd := exec.Command(bundle, param)
	output, err := cmd.Output()
	if err != nil {
		log.Printf("Bundle %s with parameter %s returned error code 1", bundle, param)
		return ""
	}

	outputStr := string(output)
	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		log.Printf("Failed to decode base64 output for %s %s: %v", bundle, param, err)
		return ""
	}

	if err := ioutil.WriteFile(outputFile, data, 0644); err != nil {
		log.Printf("Failed to write file %s: %v", outputFile, err)
		return ""
	}

	log.Printf("Successfully wrote file: %s", outputFile)
	return outputFile
}

func cleanupBundle(path string, entries map[string]*BundleEntry, iconPath, appPath string) {
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
