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

const (
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"
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
	AppBundleID    string `json:"AppBundleID"`
	FilesystemType string `json:"FilesystemType"`
	Hash           string `json:"Hash"`
	BuildDate      string `json:"build_date,omitempty"`
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
		return fmt.Errorf("%serror%s creating zstd reader: %v", errorColor, resetColor, err)
	}
	defer zstdReader.Close()

	decompressed, err := zstdReader.DecodeAll(body, nil)
	if err != nil {
		return fmt.Errorf("%serror%s decompressing data: %v", errorColor, resetColor, err)
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

func extractAppBundleInfo(filename string) (RuntimeInfo, error) {
	file, err := elf.Open(filename)
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%serror%s opening ELF file %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}
	defer file.Close()

	section := file.Section(".pbundle_runtime_info")
	if section == nil {
		return RuntimeInfo{}, fmt.Errorf("%serror%s section .pbundle_runtime_info not found in %s%s%s", errorColor, resetColor, blueColor, filename, resetColor)
	}
	data, err := section.Data()
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%serror%s reading section data from %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}

	var runtimeInfo map[string]interface{}
	if err := msgpack.Unmarshal(data, &runtimeInfo); err != nil {
		return RuntimeInfo{}, fmt.Errorf("%serror%s parsing .pbundle_runtime_info MessagePack in %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}

	cfg := RuntimeInfo{
		AppBundleID:    runtimeInfo["AppBundleID"].(string),
		FilesystemType: runtimeInfo["FilesystemType"].(string),
		Hash:           runtimeInfo["Hash"].(string),
	}

	if cfg.AppBundleID == "" {
		return RuntimeInfo{}, fmt.Errorf("%serror%s appBundleID not found in %s%s%s", errorColor, resetColor, blueColor, filename, resetColor)
	}

	appBundleID, err := utils.ParseAppBundleID(cfg.AppBundleID)
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("%serror%s invalid AppBundleID in %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}

	if appBundleID.IsDated() {
		cfg.BuildDate = appBundleID.Date.Format("2006-01-02")
	} else {
		cfg.BuildDate = "unknown"
	}

	return cfg, nil
}

func getFileSize(path string) string {
	fileInfo, err := os.Stat(path)
	if err != nil {
		log.Printf("%swarning%s unable to get file size for %s%s%s: %v", warningColor, resetColor, blueColor, path, resetColor, err)
		return "0 MB"
	}
	sizeMB := float64(fileInfo.Size()) / (1024 * 1024)
	return fmt.Sprintf("%.2f MB", sizeMB)
}

func computeHashes(path string) (string, string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", fmt.Errorf("%serror%s opening file %s%s%s for hashing: %v", errorColor, resetColor, blueColor, path, resetColor, err)
	}
	defer file.Close()

	shaHasher := sha256.New()
	if _, err := io.Copy(shaHasher, file); err != nil {
		return "", "", fmt.Errorf("%serror%s computing SHA256 for %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
	}
	shaSum := hex.EncodeToString(shaHasher.Sum(nil))

	_, err = file.Seek(0, 0)
	if err != nil {
		return "", "", fmt.Errorf("%serror%s seeking file %s%s%s for Blake3: %v", errorColor, resetColor, blueColor, path, resetColor, err)
	}
	b3Hasher := blake3.New()
	if _, err := io.Copy(b3Hasher, file); err != nil {
		return "", "", fmt.Errorf("%serror%s computing Blake3 for %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
	}
	b3Sum := hex.EncodeToString(b3Hasher.Sum(nil))

	return b3Sum, shaSum, nil
}

func isExecutable(path string) (bool, error) {
	fileInfo, err := os.Stat(path)
	if err != nil {
		return false, fmt.Errorf("%serror%s checking executable status for %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
	}
	mode := fileInfo.Mode()
	return mode&0111 != 0, nil
}

func extractAppStreamXML(filename string) (*AppStreamXML, error) {
	cmd := exec.Command(filename, "--pbundle_appstream")
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%serror%s extracting AppStream XML from %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}

	decodedOutput, err := base64.StdEncoding.DecodeString(string(output))
	if err != nil {
		return nil, fmt.Errorf("%serror%s decoding base64 output from %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
	}

	var appStreamXML AppStreamXML
	err = xml.Unmarshal(decodedOutput, &appStreamXML)
	if err != nil {
		return nil, fmt.Errorf("%serror%s unmarshalling XML from %s%s%s: %v", errorColor, resetColor, blueColor, filename, resetColor, err)
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
		log.Printf("%serror%s loading AppStream metadata: %v", errorColor, resetColor, err)
		return
	}

	dbinMetadata := make(DbinMetadata)

	err := filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Printf("%serror%s walking directory at %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".AppBundle") {
			appBundleInfo, err := extractAppBundleInfo(path)
			if err != nil {
				log.Printf("%serror%s extracting runtime info from %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
				return nil
			}

			b3sum, shasum, err := computeHashes(path)
			if err != nil {
				log.Printf("%serror%s computing hashes for %s%s%s: %v", errorColor, resetColor, blueColor, path, resetColor, err)
				return nil
			}

			var pkg, pkgId string
			baseFilename := filepath.Base(path)
			appBundleID, err := utils.ParseAppBundleID(appBundleInfo.AppBundleID)
			if err == nil && appBundleID.Compliant() == nil {
				pkg = appBundleID.Name + "." + appBundleInfo.FilesystemType + ".AppBundle"
				pkgId = ternary(appBundleID.Repo != "", appBundleID.Repo, "github.com.xplshn.appbundlehub."+appBundleID.ShortName())
			} else {
				pkgId = strings.TrimSuffix(baseFilename, filepath.Ext(baseFilename+"."+appBundleInfo.FilesystemType))
				pkg = baseFilename
				pkgId = "github.com.xplshn.appbundlehub." + pkgId
			}
			log.Printf("Adding [%s%s%s](%s) to repository index", blueColor, baseFilename, resetColor, appBundleID.String())

			item := binaryEntry{
				Pkg:         pkg,
				Name:        strings.Title(strings.ReplaceAll(appBundleID.Name, "-", " ")),
				PkgId:       pkgId,
				BuildDate:   appBundleID.Date.String(),
				Size:        getFileSize(path),
				Bsum:        b3sum,
				Shasum:      shasum,
				DownloadURL: *downloadPrefix + filepath.Base(path),
				RepoName:    *repoName,
			}

			isExec, err := isExecutable(path)
			if err != nil {
				log.Printf("%serror%s checking if %s%s%s is executable: %v", errorColor, resetColor, blueColor, path, resetColor, err)
				return nil
			}
			if !isExec {
				log.Printf("%swarning%s %s%s%s is not executable", warningColor, resetColor, blueColor, filepath.Base(path), resetColor)
			}

			appStreamXML, err := extractAppStreamXML(path)
			if err != nil {
				log.Printf("%swarning%s %s%s%s does not have an AppStream AppData.xml", warningColor, resetColor, blueColor, path, resetColor)
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
						// Convert Summary to plain text
						summaryText, err := html2text.FromString(appData.Summary, html2text.Options{PrettyTables: true})
						if err != nil {
							log.Printf("%swarning%s failed to convert Summary to plain text for %s%s%s: %v", warningColor, resetColor, blueColor, path, resetColor, err)
							item.Description = appData.Summary // Fallback to raw summary
						} else {
							item.Description = summaryText
						}
					}
					if appData.RichDescription != "" {
						// Convert RichDescription to plain text
						richDescText, err := html2text.FromString(appData.RichDescription, html2text.Options{PrettyTables: true})
						if err != nil {
							log.Printf("%swarning%s failed to convert RichDescription to plain text for %s%s%s: %v", warningColor, resetColor, blueColor, path, resetColor, err)
							item.LongDescription = appData.RichDescription // Fallback to raw HTML
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
			} else {
				if name := getText(appStreamXML.Names); name != "" {
					item.Name = name
				}
				if appStreamXML.Icon != "" {
					item.Icon = appStreamXML.Icon
				}
				if len(appStreamXML.Screenshots.Screenshot) > 0 {
					for _, screenshot := range appStreamXML.Screenshots.Screenshot {
						item.Screenshots = append(item.Screenshots, screenshot.Image)
					}
				}
				if summary := getText(appStreamXML.Summaries); summary != "" {
					// Convert Summary to plain text
					summaryText, err := html2text.FromString(summary, html2text.Options{PrettyTables: true})
					if err != nil {
						log.Printf("%swarning%s failed to convert Summary to plain text for %s%s%s: %v", warningColor, resetColor, blueColor, path, resetColor, err)
						item.Description = summary // Fallback to raw summary
					} else {
						item.Description = summaryText
					}
				}
				if appStreamXML.Description.InnerXML != "" {
					// Convert Description.InnerXML to plain text
					descText, err := html2text.FromString(appStreamXML.Description.InnerXML, html2text.Options{PrettyTables: true})
					if err != nil {
						log.Printf("%swarning%s failed to convert Description to plain text for %s%s%s: %v", warningColor, resetColor, blueColor, path, resetColor, err)
						item.LongDescription = appStreamXML.Description.InnerXML // Fallback to raw HTML
					} else {
						item.LongDescription = descText
					}
				}
				item.AppstreamId = appBundleID.Name
			}

			dbinMetadata[*repoName] = append(dbinMetadata[*repoName], item)
		}
		return nil
	})

	if err != nil {
		log.Printf("%serror%s processing files: %v", errorColor, resetColor, err)
		return
	}

	if *outputJSON != "" {
		var buffer strings.Builder
		encoder := json.NewEncoder(&buffer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")

		if err := encoder.Encode(dbinMetadata); err != nil {
			log.Printf("%serror%s creating JSON: %v", errorColor, resetColor, err)
			return
		}

		if err := os.WriteFile(*outputJSON, []byte(buffer.String()), 0644); err != nil {
			log.Printf("%serror%s writing JSON file: %v", errorColor, resetColor, err)
			return
		}

		log.Printf("Successfully wrote JSON output to %s", *outputJSON)
	}

	if *outputMarkdown != "" {
		markdownContent, err := generateMarkdown(dbinMetadata)
		if err != nil {
			log.Printf("%serror%s generating Markdown: %v", errorColor, resetColor, err)
			return
		}

		if err := os.WriteFile(*outputMarkdown, []byte(markdownContent), 0644); err != nil {
			log.Printf("%serror%s writing Markdown file: %v", errorColor, resetColor, err)
			return
		}

		log.Printf("Successfully wrote Markdown output to %s", *outputMarkdown)
	}
}

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

func ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
