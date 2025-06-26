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
	"github.com/shamaton/msgpack/v2" //"github.com/fxamacker/cbor/v2"
	"github.com/zeebo/blake3"

	"github.com/xplshn/pelf/pkg/utils"
)

func init() {
	log.SetFlags(0)
}

type binaryEntry struct {
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
	Snapshots       []snapshot `json:"snapshots,omitempty"`
	Provides        string     `json:"provides,omitempty"`
	License         []string   `json:"license,omitempty"`
	Notes           []string   `json:"notes,omitempty"`
	Appstream       string     `json:"appstream,omitempty"`
	Rank            uint       `json:"rank,omitempty"`
	RepoURL         string     `json:"-"`
	RepoGroup       string     `json:"-"`
	RepoName        string     `json:"-"`
}

type snapshot struct {
	Commit  string `json:"commit,omitempty"`
	Version string `json:"version,omitempty"`
}

type DbinMetadata map[string][]binaryEntry

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

var appStreamMetadata []AppStreamMetadata
var appStreamMetadataLoaded bool

type RuntimeInfo struct {
	AppBundleID string `json:"AppBundleID"`
}

func loadAppStreamMetadata() error {
	if appStreamMetadataLoaded {
		return nil
	}

	log.Println("Loading AppStream metadata from remote source")
	resp, err := http.Get("https://github.com/xplshn/dbin-metadata/raw/refs/heads/master/misc/cmd/flatpakAppStreamScrapper/appstream_metadata.msgp.zst")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	zstdReader, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1))
	if err != nil {
		return fmt.Errorf("error creating zstd reader: %v", err)
	}
	defer zstdReader.Close()

	decompressed, err := zstdReader.DecodeAll(body, nil)
	if err != nil {
		return fmt.Errorf("error decompressing data: %v", err)
	}

	err = msgpack.Unmarshal(decompressed, &appStreamMetadata)
	if err != nil {
		return err
	}

	log.Printf("Successfully loaded %d AppStream metadata entries", len(appStreamMetadata))
	appStreamMetadataLoaded = true
	return nil
}

func findAppStreamMetadataForAppId(appId string) *AppStreamMetadata {
	for i := range appStreamMetadata {
		if appStreamMetadata[i].AppId == appId {
			return &appStreamMetadata[i]
		}
	}
	return nil
}

func extractAppBundleInfo(filename string) (utils.AppBundleID, string, error) {
	file, err := elf.Open(filename)
	if err != nil {
		return utils.AppBundleID{}, "", err
	}
	defer file.Close()

	section := file.Section(".pbundle_runtime_info")
	if section == nil {
		return utils.AppBundleID{}, "", fmt.Errorf("section .pbundle_runtime_info not found")
	}
	data, err := section.Data()
	if err != nil {
		return utils.AppBundleID{}, "", err
	}

	var runtimeInfo RuntimeInfo
	if err := msgpack.Unmarshal(data, &runtimeInfo); err != nil {
		return utils.AppBundleID{}, "", err
	}
	if runtimeInfo.AppBundleID == "" {
		return utils.AppBundleID{}, "", fmt.Errorf("appBundleID not found")
	}

	// Parse the AppBundleID using utils.ParseAppBundleID
	appBundleID, err := utils.ParseAppBundleID(runtimeInfo.AppBundleID)
	if err != nil {
		return utils.AppBundleID{}, "", fmt.Errorf("invalid AppBundleID: %v", err)
	}

	// Extract build date if present
	var buildDate string
	if appBundleID.IsDated() {
		buildDate = appBundleID.Date.Format("2006-01-02") // Format as YYYY-MM-DD
	} else {
		buildDate = "unknown"
	}

	return *appBundleID, buildDate, nil
}

func getFileSize(path string) string {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return "0 MB"
	}
	sizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	return fmt.Sprintf("%.2f MB", sizeMB)
}

func computeHashes(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()

	shaHasher := sha256.New()
	if _, err := io.Copy(shaHasher, file); err != nil {
		return "", "", err
	}
	shaSum := hex.EncodeToString(shaHasher.Sum(nil))

	_, err = file.Seek(0, 0)
	if err != nil {
		return "", "", err
	}
	b3Hasher := blake3.New()
	if _, err := io.Copy(b3Hasher, file); err != nil {
		return "", "", err
	}
	b3Sum := hex.EncodeToString(b3Hasher.Sum(nil))

	return b3Sum, shaSum, nil
}

func isExecutable(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	// Check if the file has executable permissions (e.g., -r-x------, -rwxr-xr-x)
	mode := fileInfo.Mode()
	return mode&0111 != 0, nil
}

func extractAppStreamXML(filename string) (*AppStreamXML, error) {
	cmd := exec.Command(filename, "--pbundle_appstream")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("error extracting AppStream XML: %v", err)
	}

	decodedOutput, err := base64.StdEncoding.DecodeString(string(output))
	if err != nil {
		return nil, fmt.Errorf("error decoding base64 output: %v", err)
	}

	var appStreamXML AppStreamXML
	err = xml.Unmarshal(decodedOutput, &appStreamXML)
	if err != nil {
		return nil, fmt.Errorf("error unmarshalling XML: %v", err)
	}

	return &appStreamXML, nil
}

func generateMarkdown(dbinMetadata DbinMetadata) (string, error) {
	var mdBuffer strings.Builder
	mdBuffer.WriteString("| appname | description | site | download | version |\n")
	mdBuffer.WriteString("|---------|-------------|------|----------|---------|\n")

	var allEntries []binaryEntry
	for _, entries := range dbinMetadata {
		allEntries = append(allEntries, entries...)
	}

	sort.Slice(allEntries, func(i, j int) bool {
		return strings.ToLower(allEntries[i].Name) < strings.ToLower(allEntries[j].Name)
	})

	for _, entry := range allEntries {
		siteURL := ""

		pkg := strings.TrimSuffix(entry.Pkg, filepath.Ext(entry.Pkg))
		pkg = strings.TrimSuffix(pkg, ".dwfs")
		pkg = strings.TrimSuffix(pkg, ".sqfs")

		if pkg != "" {
			entry.Pkg = pkg
		}

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
			pkg,
			ternary(entry.Description != "", entry.Description, "not_available"),
			ternary(siteURL != "", siteURL, "not_available"),
			entry.DownloadURL,
			ternary(version != "", version, "not_available"),
		))
	}
	return mdBuffer.String(), nil
}

func main() {
	inputDir := flag.String("input-dir", "", "Path to the input directory containing .AppBundle files")
	outputJSON := flag.String("output-file", "", "Path to the output JSON file")
	outputMarkdown := flag.String("output-markdown", "", "Path to the output Markdown file")
	downloadPrefix := flag.String("download-prefix", "https://example.com/downloads/", "Prefix for download URLs")
	repoName := flag.String("repo-name", "", "Name of the repository")
	flag.Parse()

	if *inputDir == "" || *repoName == "" {
		log.Println("Usage: --input-dir <input_directory> --output-file <output_file.json> --download-prefix <url> --repo-name <repo_name> [--output-markdown <output_file.md>]")
		return
	}

	if err := loadAppStreamMetadata(); err != nil {
		log.Printf("Error loading AppStream metadata: %v\n", err)
		return
	}

	dbinMetadata := make(DbinMetadata)

	err := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".AppBundle") {
			appBundleID, buildDate, err := extractAppBundleInfo(path)
			if err != nil {
				log.Printf("Error extracting runtime info from %s: %v\n", path, err)
				return nil
			}

			b3sum, shasum, err := computeHashes(path)
			if err != nil {
				log.Printf("Error computing hashes for %s: %v\n", path, err)
				return nil
			}

			var pkg, pkgId string
			baseFilename := filepath.Base(path)
			if appBundleID.Compliant() == nil {
				// New format: name#repo:version[@dd_mm_yyyy]
				pkg = appBundleID.Name
				pkgId = "github.com.xplshn.appbundlehub." + appBundleID.Name
			} else {
				// Legacy format: name-dd_mm_yyyy-maintainer
				pkg = appBundleID.Name
				pkgId = "github.com.xplshn.appbundlehub." + appBundleID.Name
			}

			log.Printf("Adding %s to repository index\n", baseFilename)
			log.Println(".pkg: " + pkg)

			item := binaryEntry{
				Pkg:         pkg,
				Name:        strings.Title(strings.ReplaceAll(appBundleID.Name, "-", " ")),
				PkgId:       pkgId,
				BuildDate:   buildDate,
				Size:        getFileSize(path),
				Bsum:        b3sum,
				Shasum:      shasum,
				DownloadURL: *downloadPrefix + filepath.Base(path),
				RepoName:    *repoName,
			}

			// Check if the file is executable before attempting AppStream extraction
			isExec, err := isExecutable(path)
			if err != nil {
				log.Printf("Error checking if %s is executable: %v\n", path, err)
				return nil
			}
			if !isExec {
				log.Printf("warning: %s is not executable\n", filepath.Base(path))
			}

			appStreamXML, err := extractAppStreamXML(path)
			if err != nil {
				// If no AppStream data was found or the file is not executable, use flatpakAppStreamScrapper data
				appData := findAppStreamMetadataForAppId(appBundleID.ShortName())
				if appData != nil {
					log.Printf("Using flatpakAppStreamScrapper data for %s\n", baseFilename)
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
						item.Description = appData.Summary
					}
					if appData.RichDescription != "" {
						item.LongDescription = appData.RichDescription
					}
					if appData.Categories != "" {
						item.Categories = appData.Categories
					}
					if appData.Version != "" {
						item.Version = appData.Version
					}
					item.AppstreamId = appBundleID.ShortName()
				}
			} else {
				if getText(appStreamXML.Names) != "" {
					item.Name = getText(appStreamXML.Names)
				}
				if appStreamXML.Icon != "" {
					item.Icon = appStreamXML.Icon
				}
				if len(appStreamXML.Screenshots.Screenshot) > 0 {
					for _, screenshot := range appStreamXML.Screenshots.Screenshot {
						item.Screenshots = append(item.Screenshots, screenshot.Image)
					}
				}
				if getText(appStreamXML.Summaries) != "" {
					item.Description = getText(appStreamXML.Summaries)
				}
				if appStreamXML.Description.InnerXML != "" {
					item.LongDescription = appStreamXML.Description.InnerXML
				}
				item.AppstreamId = appBundleID.Name
			}

			dbinMetadata[*repoName] = append(dbinMetadata[*repoName], item)
		}
		return nil
	})

	if err != nil {
		log.Println("Error processing files:", err)
		return
	}

	if *outputJSON != "" {
		var buffer strings.Builder
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(dbinMetadata); err != nil {
			log.Println("Error creating JSON:", err)
			return
		}

		if err := os.WriteFile(*outputJSON, []byte(buffer.String()), 0644); err != nil {
			log.Println("Error writing JSON file:", err)
			return
		}

		log.Printf("Successfully wrote JSON output to %s\n", *outputJSON)
	}

	if *outputMarkdown != "" {
		markdownContent, err := generateMarkdown(dbinMetadata)
		if err != nil {
			log.Println("Error generating Markdown:", err)
			return
		}

		if err := os.WriteFile(*outputMarkdown, []byte(markdownContent), 0644); err != nil {
			log.Println("Error writing Markdown file:", err)
			return
		}

		log.Printf("Successfully wrote Markdown output to %s\n", *outputMarkdown)
	}
}

func getText(elements []struct {
	Lang string `xml:"lang,attr"`
	Text string `xml:",chardata"`
}) string {
	// First, try to find explicit English
	for _, elem := range elements {
		if elem.Lang == "en" || elem.Lang == "en_US" || elem.Lang == "en_GB" {
			return elem.Text
		}
	}

	// If no explicit English, look for elements without lang attribute (default)
	for _, elem := range elements {
		if elem.Lang == "" {
			return elem.Text
		}
	}

	// If still nothing, return the first element
	if len(elements) > 0 {
		return elements[0].Text
	}

	return ""
}

func ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
