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

	"github.com/liamg/tml"
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
	log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Starting <green>pelfd</green> daemon"))

	usr, err := user.Current()
	if err != nil {
		log.Fatalf("<red><bold>ERR:</bold></red> Failed to get current user: <yellow>%v</yellow>", err)
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
			log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Config file does not exist: <green>%s</green>, creating a new one", configPath))
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
		log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Scanning directory: <green>%s</green>", dir))

		for _, ext := range options.ProbeExtensions {
			bundles, err := filepath.Glob(filepath.Join(dir, "*"+ext))
			if err != nil {
				log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to scan directory <yellow>%s</yellow> for <yellow>%s</yellow> files: %v", dir, ext, err))
				continue
			}

			for _, bundle := range bundles {
				// VERBOSITY: log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Found bundle: <green>%s</green>", bundle))
				existing[bundle] = struct{}{}

				sha := computeSHA(bundle)
				if entry, checked := entries[bundle]; checked {
					if entry == nil {
						// VERBOSITY: log.Printf(tml.Sprintf("<red><bold>ERR:</bold></red> Entry for bundle <yellow>%s</yellow> is nil, skipping", bundle))
						continue
					}

					if entry.SHA != sha {
						if isExecutable(bundle) {
							processBundles(bundle, sha, entries, options.IconDir, options.AppDir)
							changed = true
						} else {
							// VERBOSITY: log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Bundle is not executable: <yellow>%s</yellow>", bundle))
							entries[bundle] = nil
						}
					} // VERBOSITY: else {log.Printf(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle already processed and unchanged: <blue>%s</blue>", bundle))}
				} else {
					if isExecutable(bundle) {
						processBundles(bundle, sha, entries, options.IconDir, options.AppDir)
						changed = true
					} else {
						// VERBOSITY: log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Bundle is not executable: <yellow>%s</yellow>", bundle))
						entries[bundle] = nil
					}
				}
			}
		}
	}

	for path := range entries {
		if _, found := existing[path]; !found {
			log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Bundle no longer exists: <yellow>%s</yellow>", path))
			cleanupBundle(path, entries, options.IconDir, options.AppDir)
			changed = true
		}
	}

	if changed {
		log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Updating config: <green>%s</green>", filepath.Join(homeDir, configFilePath)))
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

func processBundles(path, sha string, entries map[string]*BundleEntry, iconPath, appPath string) {
	entry := &BundleEntry{Path: path, SHA: sha}

	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	entry.Png = executeBundle(path, "--pbundle_pngIcon", filepath.Join(iconPath, baseName+".png"))
	entry.Xpm = executeBundle(path, "--pbundle_xpmIcon", filepath.Join(iconPath, baseName+".xpm"))
	entry.Desktop = executeBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))

	if entry.Png != "" || entry.Xpm != "" || entry.Desktop != "" {
		log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Adding bundle to entries: <green>%s</green>", path))
		entries[path] = entry
	} else {
		log.Printf(tml.Sprintf("<yellow><bold>WRN:</bold></yellow>: Bundle does not contain required files: <blue>%s</blue>", path))
		entries[path] = nil
	}
}

func executeBundle(bundle, param, outputFile string) string {
	log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Retrieving metadata from <green>%s</green> with parameter: <cyan>%s</cyan>", bundle, param))
	cmd := exec.Command(bundle, param)
	output, err := cmd.Output()
	if err != nil {
		log.Printf(tml.Sprintf("<yellow><bold>WRN:</bold></yellow> Bundle <blue>%s</blue> with parameter <cyan>%s</cyan> returned error code 1", bundle, param))
		return ""
	}

	outputStr := string(output)
	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to decode base64 output for <yellow>%s</yellow> <yellow>%s</yellow>: <red>%v</red>", bundle, param, err))
		return ""
	}

	if err := ioutil.WriteFile(outputFile, data, 0644); err != nil {
		log.Fatalf(tml.Sprintf("<red><bold>ERR:</bold></red> Failed to write file <yellow>%s</yellow>: <red>%v</red>", outputFile, err))
		return ""
	}

	log.Printf(tml.Sprintf("<blue><bold>INF:</bold></blue> Successfully wrote file: <green>%s</green>", outputFile))
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
