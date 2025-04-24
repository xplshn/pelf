package main

import (
	"bytes"
	"debug/elf"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"crypto/sha256"
	"net/http"
	"sort"

	"github.com/zeebo/blake3"
	"github.com/fxamacker/cbor/v2"
	"github.com/goccy/go-json"
	"github.com/klauspost/compress/zstd"
)

type binaryEntry struct {
	Pkg            string     `json:"pkg,omitempty"`
	Name           string     `json:"pkg_name,omitempty"`
	PkgId          string     `json:"pkg_id,omitempty"`
	AppstreamId    string     `json:"app_id,omitempty"`
	Icon           string     `json:"icon,omitempty"`
	Description    string     `json:"description,omitempty"`
	LongDescription string    `json:"description_long,omitempty"`
	Screenshots    []string   `json:"screenshots,omitempty"`
	Version        string     `json:"version,omitempty"`
	DownloadURL    string     `json:"download_url,omitempty"`
	Size           string     `json:"size,omitempty"`
	Bsum           string     `json:"bsum,omitempty"`
	Shasum         string     `json:"shasum,omitempty"`
	BuildDate      string     `json:"build_date,omitempty"`
	SrcURLs        []string   `json:"src_urls,omitempty"`
	WebURLs        []string   `json:"web_urls,omitempty"`
	BuildScript    string     `json:"build_script,omitempty"`
	BuildLog       string     `json:"build_log,omitempty"`
	Categories     string     `json:"categories,omitempty"`
	Snapshots      []snapshot `json:"snapshots,omitempty"`
	Provides       string     `json:"provides,omitempty"`
	License        []string   `json:"license,omitempty"`
	Notes          []string   `json:"notes,omitempty"`
	Appstream      string     `json:"appstream,omitempty"`
	Rank           uint       `json:"rank,omitempty"`
	RepoURL        string     `json:"-"`
	RepoGroup      string     `json:"-"`
	RepoName       string     `json:"-"`
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

var appStreamMetadata []AppStreamMetadata
var appStreamMetadataLoaded bool

type RuntimeInfo struct {
	AppBundleID  string   `json:"appBundleID"`
}

func loadAppStreamMetadata() error {
	if appStreamMetadataLoaded {
		return nil
	}

	resp, err := http.Get("https://github.com/xplshn/dbin-metadata/raw/refs/heads/master/misc/cmd/flatpakAppStreamScrapper/appstream_metadata.cbor.zst")
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	// Decompress zstd content
	zstdReader, err := zstd.NewReader(bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("error creating zstd reader: %v", err)
	}
	defer zstdReader.Close()

	decompressed, err := io.ReadAll(zstdReader)
	if err != nil {
		return fmt.Errorf("error decompressing data: %v", err)
	}

	err = cbor.Unmarshal(decompressed, &appStreamMetadata)
	if err != nil {
		return err
	}

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

func extractAppBundleInfo(filename string) (RuntimeInfo, string, error) {
	file, err := elf.Open(filename)
	if err != nil {
		return RuntimeInfo{}, "", err
	}
	defer file.Close()
	section := file.Section(".pbundle_runtime_info")
	if section == nil {
		return RuntimeInfo{}, "", fmt.Errorf("section .pbundle_runtime_info not found")
	}
	data, err := section.Data()
	if err != nil {
		return RuntimeInfo{}, "", err
	}
	var runtimeInfo RuntimeInfo
	if err := cbor.Unmarshal(data, &runtimeInfo); err != nil {
		return RuntimeInfo{}, "", err
	}
	if runtimeInfo.AppBundleID == "" {
		return RuntimeInfo{}, "", fmt.Errorf("appBundleID not found")
	}
	// Parse build date from AppBundleID (format: appstreamId-DD_MM_YYYY-maintainer)
	parts := strings.Split(runtimeInfo.AppBundleID, "-")
	if len(parts) < 3 {
		return RuntimeInfo{}, "", fmt.Errorf("invalid appBundleID format")
	}
	buildDate := parts[len(parts)-2]
	re := regexp.MustCompile(`^(\d{2})_(\d{2})_(\d{4})$`)
	matches := re.FindStringSubmatch(buildDate)
	if len(matches) != 4 {
		return RuntimeInfo{}, "", fmt.Errorf("invalid build date format")
	}
	formattedBuildDate := fmt.Sprintf("%s-%s-%s", matches[3], matches[2], matches[1])

	// Extract the actual appstreamId (everything before the date)
	dateIndex := strings.LastIndex(runtimeInfo.AppBundleID, buildDate) - 1
	actualAppStreamId := runtimeInfo.AppBundleID[:dateIndex]
	runtimeInfo.AppBundleID = actualAppStreamId

	return runtimeInfo, formattedBuildDate, nil
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

	// Compute SHA256
	shaHasher := sha256.New()
	if _, err := io.Copy(shaHasher, file); err != nil {
		return "", "", err
	}
	shaSum := hex.EncodeToString(shaHasher.Sum(nil))

	// Compute BLAKE3
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

func generateMarkdown(dbinMetadata DbinMetadata) (string, error) {
	var mdBuffer bytes.Buffer
	mdBuffer.WriteString("| appname | description | site | download | version |\n")
	mdBuffer.WriteString("|---------|-------------|------|----------|---------|\n")

	var allEntries []binaryEntry
	for _, entries := range dbinMetadata {
		allEntries = append(allEntries, entries...)
	}

	// Sort entries by pkg_name (lowercased)
	sort.Slice(allEntries, func(i, j int) bool {
		return strings.ToLower(allEntries[i].Name) < strings.ToLower(allEntries[j].Name)
	})

	// Generate markdown content
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

		mdBuffer.WriteString(fmt.Sprintf("| %s | %s | %s | %s | %s |\n",
			filepath.Base(entry.Pkg), // No extensions in AM's repo index allowed
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
		fmt.Println("Usage: --input-dir <input_directory> --output-file <output_file.json> --download-prefix <url> --repo-name <repo_name> [--output-markdown <output_file.md>]")
		return
	}

	if err := loadAppStreamMetadata(); err != nil {
		fmt.Printf("Error loading AppStream metadata: %v\n", err)
		return
	}

	dbinMetadata := make(DbinMetadata)

	err := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".AppBundle") {
			runtimeInfo, buildDate, err := extractAppBundleInfo(path)
			if err != nil {
				fmt.Printf("Error extracting runtime info from %s: %v\n", path, err)
				return nil
			}

			// Compute hashes and size
			b3sum, shasum, err := computeHashes(path)
			if err != nil {
				fmt.Printf("Error computing hashes for %s: %v\n", path, err)
				return nil
			}

			var pkgId string
			// Extract the base filename without date and author
			baseFilename := filepath.Base(path)
			re := regexp.MustCompile(`^(.+)-(\d{2}_\d{2}_\d{4})-(.+)$`)
			matches := re.FindStringSubmatch(baseFilename)
			if len(matches) == 4 {
				pkgId = matches[1] + "." + matches[3]
			}

			// Generate PkgId
			pkgId = "github.com.xplshn.appbundlehub" + "." + filepath.Base(pkgId)

			// Create base item with info from the AppBundle
			item := binaryEntry{
				Pkg:            baseFilename,
				Name:           strings.Title(strings.ReplaceAll(runtimeInfo.AppBundleID, "-", " ")),
				PkgId:          pkgId,
				BuildDate:      buildDate,
				Size:           getFileSize(path),
				Bsum:           b3sum,
				Shasum:         shasum,
				DownloadURL:    *downloadPrefix + filepath.Base(path),
				RepoName:       *repoName,
			}

			// Look for matching AppStream metadata and use it to enhance our entry
			appData := findAppStreamMetadataForAppId(runtimeInfo.AppBundleID)
			if appData != nil {
				// Use the name, icon, screenshots, description fields from appstream_metadata
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

				// TODO:
				if appData.Version != "" {
					item.Version = appData.Version
				}

				// Set app_id because AppStream data is available
				item.AppstreamId = runtimeInfo.AppBundleID

				fmt.Printf("Enhanced entry with AppStream data: %s\n", runtimeInfo.AppBundleID)
			}

			// Add to metadata
			dbinMetadata[*repoName] = append(dbinMetadata[*repoName], item)
		}
		return nil
	})

	if err != nil {
		fmt.Println("Error processing files:", err)
		return
	}

	// Generate the output JSON
	if *outputJSON != "" {
		buffer := &bytes.Buffer{}
		encoder := json.NewEncoder(buffer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(dbinMetadata); err != nil {
			fmt.Println("Error creating JSON:", err)
			return
		}

		if err := os.WriteFile(*outputJSON, buffer.Bytes(), 0644); err != nil {
			fmt.Println("Error writing JSON file:", err)
			return
		}

		fmt.Printf("Successfully wrote JSON output to %s\n", *outputJSON)
	}

	// Generate the output Markdown
	if *outputMarkdown != "" {
		markdownContent, err := generateMarkdown(dbinMetadata)
		if err != nil {
			fmt.Println("Error generating Markdown:", err)
			return
		}

		if err := os.WriteFile(*outputMarkdown, []byte(markdownContent), 0644); err != nil {
			fmt.Println("Error writing Markdown file:", err)
			return
		}

		fmt.Printf("Successfully wrote Markdown output to %s\n", *outputMarkdown)
	}
}

func ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
