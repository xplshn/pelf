package main

import (
	"crypto/sha256"
	"debug/elf"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
	"github.com/shamaton/msgpack/v2"
	"github.com/zeebo/blake3"
	"github.com/jaytaylor/html2text"
	"github.com/xplshn/pelf/pkg/utils"
)

// ANSI color codes for logging
const (
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"
)

// BinaryEntry represents metadata for an AppBundle
type BinaryEntry struct {
	Pkg             string     `json:"pkg,omitempty"`
	Name            string     `json:"pkg_name,omitempty"`
	PkgId           string     `json:"pkg_id,omitempty"`
	AppstreamId     string     `json:"app_id,omitempty"`
	Icon            string     `json:"icon,omitempty"`
	Description     string     `json:"description,omitempty"`
	LongDescription string     `json:"description_long,omitempty"`
	Screenshots     []string   `json:"screenshots,omitempty"`
	Version         string     `json:"version,omitempty"`
	DownloadURL     string     `json:"download_url,omitempty"`
	Size            string     `json:"size,omitempty"`
	Bsum            string     `json:"bsum,omitempty"`
	Shasum          string     `json:"shasum,omitempty"`
	BuildDate       string     `json:"build_date,omitempty"`
	SrcURLs         []string   `json:"src_urls,omitempty"`
	WebURLs         []string   `json:"web_urls,omitempty"`
	BuildScript     string     `json:"build_script,omitempty"`
	BuildLog        string     `json:"build_log,omitempty"`
	Categories      string     `json:"categories,omitempty"`
	Snapshots       []Snapshot `json:"snapshots,omitempty"`
	Provides        string     `json:"provides,omitempty"`
	License         []string   `json:"license,omitempty"`
	Notes           []string   `json:"notes,omitempty"`
	Appstream       string     `json:"appstream,omitempty"`
	Rank            uint       `json:"rank,omitempty"`
	RepoURL         string     `json:"-"`
	RepoGroup       string     `json:"-"`
	RepoName        string     `json:"-"`
}

// Snapshot holds version and commit information
type Snapshot struct {
	Commit  string `json:"commit,omitempty"`
	Version string `json:"version,omitempty"`
}

// DbinMetadata maps repository names to a slice of BinaryEntry
type DbinMetadata map[string][]BinaryEntry

// AppStreamMetadata holds parsed AppStream data
type AppStreamMetadata struct {
	AppId           string   `json:"app_id"`
	Name            string   `json:"name,omitempty"`
	Categories      string   `json:"categories"`
	Summary         string   `json:"summary,omitempty"`
	RichDescription string   `json:"rich_description"`
	Version         string   `json:"version"`
	Icons           []string `json:"icons"`
	Screenshots     []string `json:"screenshots"`
}

// AppStreamXML represents the structure of AppStream XML data
type AppStreamXML struct {
	XMLName xml.Name `xml:"component"`
	ID      string   `xml:"id"`
	Names   []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"name"`
	Summaries []struct {
		Lang string `xml:"lang,attr"`
		Text string `xml:",chardata"`
	} `xml:"summary"`
	Description struct {
		InnerXML string `xml:",innerxml"`
	} `xml:"description"`
	Icon        string `xml:"icon"`
	Screenshots struct {
		Screenshot []struct {
			Image string `xml:"image"`
		} `xml:"screenshot"`
	} `xml:"screenshots"`
}

// RuntimeInfo holds runtime metadata from ELF sections
type RuntimeInfo struct {
	AppBundleID    string `json:"AppBundleID"`
	FilesystemType string `json:"FilesystemType"`
	Hash           string `json:"Hash"`
	BuildDate      string `json:"build_date,omitempty"`
}

var (
	appStreamMetadata      []AppStreamMetadata
	appStreamMetadataLoaded bool
)

// init sets up logging
func init() {
	log.SetFlags(0)
}

// loadAppStreamMetadata fetches and decodes AppStream metadata from a remote source
func loadAppStreamMetadata() error {
	if appStreamMetadataLoaded {
		return nil
	}

	log.Println("Loading AppStream metadata from remote source")
	resp, err := http.Get("https://github.com/xplshn/dbin-metadata/raw/refs/heads/master/misc/cmd/flatpakAppStreamScrapper/appstream_metadata.msgp.zst")
	if err != nil {
		return fmt.Errorf("%sfailed to fetch AppStream metadata%s: %v", errorColor, resetColor, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("%sfailed to read response body%s: %v", errorColor, resetColor, err)
	}

	zstdReader, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("%sfailed to create zstd reader%s: %v", errorColor, resetColor, err)
	}
	defer zstdReader.Close()

	decompressed, err := zstdReader.DecodeAll(body, nil)
	if err != nil {
		return fmt.Errorf("%sfailed to decompress data%s: %v", errorColor, resetColor, err)
	}

	err = msgpack.Unmarshal(decompressed, &appStreamMetadata)
	if err != nil {
		return fmt.Errorf("%sfailed to unmarshal AppStream metadata%s: %v", errorColor, resetColor, err)
	}

	log.Printf("Successfully loaded %d AppStream metadata entries", len(appStreamMetadata))
	appStreamMetadataLoaded = true
	return nil
}

// findAppStreamMetadataForAppId searches for AppStream metadata by app ID
func findAppStreamMetadataForAppId(appId string) *AppStreamMetadata {
	for i := range appStreamMetadata {
		if appStreamMetadata[i].AppId == appId {
			return &appStreamMetadata[i]
		}
	}
	return nil
}

// extractAppBundleInfo extracts runtime info from an ELF file
func extractAppBundleInfo(filename string) (RuntimeInfo, error) {
	file, err := elf.Open(filename)
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%sfailed to open ELF file %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}
	defer file.Close()

	section := file.Section(".pbundle_runtime_info")
	if section == nil {
		return RuntimeInfo{}, fmt.Errorf("%ssection .pbundle_runtime_info not found in %s%s%s", errorColor, blueColor, filename, resetColor)
	}
	data, err := section.Data()
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%sfailed to read section data from %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	var runtimeInfo map[string]interface{}
	if err := msgpack.Unmarshal(data, &runtimeInfo); err != nil {
		return RuntimeInfo{}, fmt.Errorf("%sfailed to parse .pbundle_runtime_info MessagePack in %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	cfg := RuntimeInfo{
		AppBundleID:    runtimeInfo["AppBundleID"].(string),
		FilesystemType: runtimeInfo["FilesystemType"].(string),
		Hash:           runtimeInfo["Hash"].(string),
	}

	switch cfg.FilesystemType {
	case "dwarfs":
		cfg.FilesystemType = "dwfs"
	case "squashfs":
		cfg.FilesystemType = "sqfs"
	}

	if cfg.AppBundleID == "" {
		return RuntimeInfo{}, fmt.Errorf("%sappBundleID not found in %s%s%s", errorColor, blueColor, filename, resetColor)
	}

	appBundleID, err := utils.ParseAppBundleID(cfg.AppBundleID)
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%sinvalid AppBundleID in %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	if appBundleID.IsDated() {
		cfg.BuildDate = appBundleID.Date.Format("2006-01-02")
	} else {
		cfg.BuildDate = "unknown"
	}

	return cfg, nil
}

// getFileSize returns the file size in MB
func getFileSize(path string) string {
	fileInfo, err := os.Stat(path)
	if err != nil {
		log.Printf("%sfailed to get file size for %s%s%s: %v", warningColor, blueColor, path, resetColor, err)
		return "0 MB"
	}
	sizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	return fmt.Sprintf("%.2f MB", sizeMB)
}

// computeHashes calculates Blake3 and SHA256 hashes for a file
func computeHashes(path string) (b3sum, shasum string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("%sfailed to open file %s%s%s for hashing: %v", errorColor, blueColor, path, resetColor, err)
	}
	defer file.Close()

	shaHasher := sha256.New()
	if _, err := io.Copy(shaHasher, file); err != nil {
		return "", "", fmt.Errorf("%sfailed to compute SHA256 for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
	}
	shaSum := hex.EncodeToString(shaHasher.Sum(nil))

	if _, err = file.Seek(0, 0); err != nil {
		return "", "", fmt.Errorf("%sfailed to seek file %s%s%s for Blake3: %v", errorColor, blueColor, path, resetColor, err)
	}
	b3Hasher := blake3.New()
	if _, err := io.Copy(b3Hasher, file); err != nil {
		return "", "", fmt.Errorf("%sfailed to compute Blake3 for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
	}
	b3Sum := hex.EncodeToString(b3Hasher.Sum(nil))

	return b3Sum, shaSum, nil
}

// isExecutable checks if a file is executable
func isExecutable(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("%sfailed to check executable status for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
	}
	return fileInfo.Mode()&0111 != 0, nil
}

// extractAppStreamXML extracts and parses AppStream XML from an AppBundle
func extractAppStreamXML(filename string) (*AppStreamXML, error) {
	cmd := exec.Command(filename, "--pbundle_appstream")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%sfailed to extract AppStream XML from %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	decodedOutput, err := base64.StdEncoding.DecodeString(string(output))
	if err != nil {
		return nil, fmt.Errorf("%sfailed to decode base64 output from %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	var appStreamXML AppStreamXML
	err = xml.Unmarshal(decodedOutput, &appStreamXML)
	if err != nil {
		return nil, fmt.Errorf("%sfailed to unmarshal XML from %s%s%s: %v", errorColor, blueColor, filename, resetColor, err)
	}

	return &appStreamXML, nil
}

// generateMarkdown creates a Markdown table from DbinMetadata
func generateMarkdown(dbinMetadata DbinMetadata) (string, error) {
	var mdBuffer strings.Builder
	mdBuffer.WriteString("| appname | description | site | download | version |\n")
	mdBuffer.WriteString("|---------|-------------|------|----------|---------|\n")

	var allEntries []BinaryEntry
	for _, entries := range dbinMetadata {
		allEntries = append(allEntries, entries...)
	}

	sort.Slice(allEntries, func(i, j int) bool {
		return strings.ToLower(allEntries[i].Name) < strings.ToLower(allEntries[j].Name)
	})

	for _, entry := range allEntries {
		siteURL := ""
		if len(entry.SrcURLs) > 0 {
			siteURL = entry.SrcURLs[0]
		} else if len(entry.WebURLs) > 0 {
			siteURL = entry.WebURLs[0]
		} else {
			siteURL = "https://github.com/xplshn/AppBundleHUB"
		}

		version := entry.Version
		if version == "" && entry.BuildDate != "" {
			version = entry.BuildDate
		}
		if version == "" {
			version = "not_available"
		}

		mdBuffer.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			entry.Pkg,
			ternary(entry.Description != "", entry.Description, "not_available"),
			ternary(siteURL != "", siteURL, "not_available"),
			entry.DownloadURL,
			ternary(version != "", version, "not_available"),
		))
	}
	return mdBuffer.String(), nil
}

// main processes AppBundle files and generates JSON and Markdown outputs
func main() {
	inputDir := flag.String("input-dir", "", "Path to the input directory containing .AppBundle files")
	outputJSON := flag.String("output-file", "", "Path to the output JSON file")
	outputMarkdown := flag.String("output-markdown", "", "Path to the output Markdown file")
	downloadPrefix := flag.String("download-prefix", "https://example.com/downloads/", "Prefix for download URLs")
	repoName := flag.String("repo-name", "", "Name of the repository")
	flag.Parse()

	if *inputDir == "" || *repoName == "" {
		log.Println("Usage: --input-dir <input_directory> --output-file <output_file.json> --download-prefix <url> --repo-name <repo_name> [--output-markdown <output_file.md>]")
		os.Exit(1)
	}

	if err := loadAppStreamMetadata(); err != nil {
		log.Printf("%sfailed to load AppStream metadata%s: %v", errorColor, resetColor, err)
		os.Exit(1)
	}

	dbinMetadata := make(DbinMetadata)

	err := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("%sfailed to walk directory at %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".AppBundle") {
			return nil
		}

		appBundleInfo, err := extractAppBundleInfo(path)
		if err != nil {
			log.Printf("%sfailed to extract runtime info from %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
			return nil
		}

		b3sum, shasum, err := computeHashes(path)
		if err != nil {
			log.Printf("%sfailed to compute hashes for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
			return nil
		}

		appBundleID, err := utils.ParseAppBundleID(appBundleInfo.AppBundleID)
		if err != nil {
			log.Printf("%sfailed to parse AppBundleID for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
			return nil
		}

		// Initialize package name and ID
		var pkg, pkgId string
		baseFilename := filepath.Base(path)
		appStreamXML, err := extractAppStreamXML(path)
		name := ""

		// Try to get name from AppStream XML
		if err == nil && appStreamXML != nil {
			name = getText(appStreamXML.Names)
		}

		// Fallback to AppStream metadata if no XML name
		if name == "" {
			appData := findAppStreamMetadataForAppId(appBundleID.Name)
			if appData != nil && appData.Name != "" {
				name = appData.Name
			}
		}

		// Sanitize name if found, otherwise fallback to nameToPkg
		if name != "" {
			name = sanitizeName(name)
		} else {
			name = nameToPkg(appBundleID.Name)
		}

		// Construct package name and ID
		pkg = name + "." + appBundleInfo.FilesystemType + ".AppBundle"
		pkgId = ternary(appBundleID.Repo != "", appBundleID.Repo, "github.com.xplshn.appbundlehub."+appBundleID.ShortName())

		log.Printf("Adding [%s%s%s](%s) to repository index", blueColor, baseFilename, resetColor, appBundleID.String())

		item := BinaryEntry{
			Pkg:         pkg,
			Name:        strings.Title(strings.ReplaceAll(name, "-", " ")),
			PkgId:       pkgId,
			BuildDate:   appBundleInfo.BuildDate,
			Size:        getFileSize(path),
			Bsum:        b3sum,
			Shasum:      shasum,
			DownloadURL: *downloadPrefix + filepath.Base(path),
			RepoName:    *repoName,
		}

		isExec, err := isExecutable(path)
		if err != nil {
			log.Printf("%sfailed to check executable status for %s%s%s: %v", errorColor, blueColor, path, resetColor, err)
			return nil
		}
		if !isExec {
			log.Printf("%s%s%s is not executable%s", warningColor, blueColor, filepath.Base(path), resetColor)
		}

		if appStreamXML != nil {
			if appStreamXML.Icon != "" {
				item.Icon = appStreamXML.Icon
			}
			for _, screenshot := range appStreamXML.Screenshots.Screenshot {
				item.Screenshots = append(item.Screenshots, screenshot.Image)
			}
			if summary := getText(appStreamXML.Summaries); summary != "" {
				summaryText, err := html2text.FromString(summary, html2text.Options{PrettyTables: true})
				if err != nil {
					log.Printf("%sfailed to convert summary to plain text for %s%s%s: %v", warningColor, blueColor, path, resetColor, err)
					item.Description = summary
				} else {
					item.Description = summaryText
				}
			}
			if appStreamXML.Description.InnerXML != "" {
				descText, err := html2text.FromString(appStreamXML.Description.InnerXML, html2text.Options{PrettyTables: true})
				if err != nil {
					log.Printf("%sfailed to convert description to plain text for %s%s%s: %v", warningColor, blueColor, path, resetColor, err)
					item.LongDescription = appStreamXML.Description.InnerXML
				} else {
					item.LongDescription = descText
				}
			}
			item.AppstreamId = appBundleID.Name
		} else {
			appData := findAppStreamMetadataForAppId(appBundleID.Name)
			if appData != nil {
				log.Printf("Using flatpakAppStreamScrapper data for %s%s%s", blueColor, baseFilename, resetColor)
				if appData.Name != "" {
					item.Name = appData.Name
				}
				if len(appData.Icons) > 0 {
					item.Icon = appData.Icons[0]
				}
				if len(appData.Screenshots) > 0 {
					item.Screenshots = appData.Screenshots
				}
				if appData.Summary != "" {
					summaryText, err := html2text.FromString(appData.Summary, html2text.Options{PrettyTables: true})
					if err != nil {
						log.Printf("%sfailed to convert summary to plain text for %s%s%s: %v", warningColor, blueColor, path, resetColor, err)
						item.Description = appData.Summary
					} else {
						item.Description = summaryText
					}
				}
				if appData.RichDescription != "" {
					richDescText, err := html2text.FromString(appData.RichDescription, html2text.Options{PrettyTables: true})
					if err != nil {
						log.Printf("%sfailed to convert rich description to plain text for %s%s%s: %v", warningColor, blueColor, path, resetColor, err)
						item.LongDescription = appData.RichDescription
					} else {
						item.LongDescription = richDescText
					}
				}
				if appData.Categories != "" {
					item.Categories = appData.Categories
				}
				if appData.Version != "" {
					item.Version = appData.Version
				}
				item.AppstreamId = appBundleID.Name
			}
		}

		dbinMetadata[*repoName] = append(dbinMetadata[*repoName], item)
		return nil
	})

	if err != nil {
		log.Printf("%sfailed to process files%s: %v", errorColor, resetColor, err)
		os.Exit(1)
	}

	if *outputJSON != "" {
		var buffer strings.Builder
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(dbinMetadata); err != nil {
			log.Printf("%sfailed to encode JSON%s: %v", errorColor, resetColor, err)
			os.Exit(1)
		}

		if err := os.WriteFile(*outputJSON, []byte(buffer.String()), 0644); err != nil {
			log.Printf("%sfailed to write JSON file%s: %v", errorColor, resetColor, err)
			os.Exit(1)
		}

		log.Printf("Successfully wrote JSON output to %s", *outputJSON)
	}

	if *outputMarkdown != "" {
		markdownContent, err := generateMarkdown(dbinMetadata)
		if err != nil {
			log.Printf("%sfailed to generate Markdown%s: %v", errorColor, resetColor, err)
			os.Exit(1)
		}

		if err := os.WriteFile(*outputMarkdown, []byte(markdownContent), 0644); err != nil {
			log.Printf("%sfailed to write Markdown file%s: %v", errorColor, resetColor, err)
			os.Exit(1)
		}

		log.Printf("Successfully wrote Markdown output to %s", *outputMarkdown)
	}
}

// sanitizeName cleans a name string for safe use
func sanitizeName(name string) string {
	name = strings.ToLower(name)
	name = strings.TrimSpace(name)
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, ":", "_")
	return name
}

// nameToPkg extracts the package name from an AppBundle ID
func nameToPkg(appBundleIDName string) string {
	idParts := strings.Split(appBundleIDName, ".")
	if len(idParts) > 0 {
		return sanitizeName(idParts[len(idParts)-1])
	}
	return appBundleIDName
}

// getText retrieves the most appropriate text from a slice of XML elements
func getText(elements []struct {
	Lang string `xml:"lang,attr"`
	Text string `xml:",chardata"`
}) string {
	for _, elem := range elements {
		if elem.Lang == "en" || elem.Lang == "en_US" || elem.Lang == "en_GB" {
			return strings.TrimSpace(elem.Text)
		}
	}

	for _, elem := range elements {
		if elem.Lang == "" {
			return strings.TrimSpace(elem.Text)
		}
	}

	if len(elements) > 0 {
		return strings.TrimSpace(elements[0].Text)
	}

	return ""
}

// ternary provides a ternary-like operation for any type
func ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
