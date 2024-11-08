package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const url_prefix = "https://github.com/xplshn/AppBundleHUB/releases/download/latest_metadata/"

type Item struct {
	Pkg             string   `json:"pkg"`
	PkgName         string   `json:"pkg_name,omitempty"`
	PkgId           string   `json:"pkg_id,omitempty"`
	Icon            string   `json:"icon,omitempty"`
	Description     string   `json:"description,omitempty"`
	RichDescription string   `json:"rich_description,omitempty"`
	Desktop         string   `json:"desktop,omitempty"`
	Screenshots     []string `json:"screenshots,omitempty"`
	Version         string   `json:"version,omitempty"`
	DownloadURL     string   `json:"download_url,omitempty"`
	Size            string   `json:"size,omitempty"`
	Bsum            string   `json:"bsum,omitempty"`
	Shasum          string   `json:"shasum,omitempty"`
	BuildDate       string   `json:"build_date,omitempty"`
	SrcURL          string   `json:"src_url,omitempty"`
	Homepage        string   `json:"homepage,omitempty"`
	BuildScript     string   `json:"build_script,omitempty"`
	BuildLog        string   `json:"build_log,omitempty"`
	Category        string   `json:"category,omitempty"`
	Keywords        string   `json:"keywords,omitempty"`
	Note            string   `json:"note,omitempty"`
	Appstream       string   `json:"appstream,omitempty"`
}

type PackageList struct {
	Pkg []Item `json:"pkg"`
}

type Provides struct {
	Id string `xml:"id"`
}

type Url struct {
	Url  string `xml:",innerxml"`
	Type string `xml:"type,attr"`
}

type ContentRatingContentAttribute struct {
	Id   string `xml:"id,attr"`
	Type string `xml:",innerxml"`
}

type ContentRating struct {
	Type             string                          `xml:"type,attr"`
	ContentAttribute []ContentRatingContentAttribute `xml:"content_attribute"`
}

type Release struct {
	Version string `xml:"version,attr"`
	Date    string `xml:"date,attr"`
}

type Releases struct {
	Release []Release `xml:"release"`
}

type Screenshot struct {
	Type    string          `xml:"type,attr"`
	Caption string          `xml:"caption"`
	Image   ScreenshotImage `xml:"image"`
}

type ScreenshotImage struct {
	Type   string `xml:"type,attr"`
	Width  string `xml:"width,attr"`
	Height string `xml:"height,attr"`
	Url    string `xml:",innerxml"`
}

type Tag struct {
	XMLName xml.Name
	Content string `xml:",innerxml"`
	Lang    string `xml:"lang,attr"`
}

type Description struct {
	Items []Tag `xml:",any"`
}

type Launchable struct {
	Type      string `xml:"type,attr"`
	DesktopId string `xml:",innerxml"`
}

type Component struct {
	Type            string `xml:"type,attr"`
	Id              string `xml:"id"`
	Name            []Tag  `xml:"name"`
	Summary         []Tag  `xml:"summary"` // -> Description
	DevName         string `xml:"developer_name"`
	MetadataLicense string `xml:"metadata_license"`
	ProjectLicense  string `xml:"project_license"`

	Provides struct {
		Id string `xml:"id"`
	} `xml:"provides"`

	Launchable struct {
		DesktopId string `xml:"desktop-id"`
	} `xml:"launchable"`

	Url []struct {
		Type string `xml:"type,attr"`
		Url  string `xml:",chardata"`
	} `xml:"url"`

	Description   []Tag         `xml:"description"` // -> RichDescription
	Screenshots   []Screenshot  `xml:"screenshots>screenshot"`
	Releases      Releases      `xml:"releases"`
	ContentRating ContentRating `xml:"content_rating"`
	Keywords      []Tag         `xml:"keywords"`
}

type Components struct {
	XMLName    xml.Name    `xml:"components"`
	Version    string      `xml:"version,attr"`
	Origin     string      `xml:"origin,attr"`
	Components []Component `xml:"component"`
}

// extractAppstreamId extracts the potential appstream ID from the AppBundle filename
func extractAppstreamId(filename string) string {
	// Remove .dwfs.AppBundle suffix
	name := strings.TrimSuffix(filename, ".dwfs.AppBundle")

	// Remove date pattern (assuming format like -DD_MM_YYYY)
	re := regexp.MustCompile(`-\d{2}_\d{2}_\d{4}$`)
	name = re.ReplaceAllString(name, "")

	return name
}

// findComponentById searches for a component with the given ID in the components list
func findComponentById(components *Components, id string) *Component {
	for i := range components.Components {
		if components.Components[i].Id == id {
			return &components.Components[i]
		}
	}
	return nil
}

// handlePBundleCommand executes a pbundle command and handles its output
// Returns true if successful, false if command returned exit status 1 (not an error)
func handlePBundleCommand(appBundlePath, flag, outputPath string) bool {
	cmd := exec.Command(appBundlePath, flag)
	output, err := cmd.Output()

	if err != nil {
		if exitError, ok := err.(*exec.ExitError); ok && exitError.ExitCode() == 1 {
			// Exit status 1 means file not found in bundle - not an error
			return false
		}
		fmt.Printf("Error executing %s: %v\n", flag, err)
		return false
	}

	decodedData, err := base64.StdEncoding.DecodeString(string(output))
	if err != nil {
		fmt.Printf("Error decoding base64 data: %v\n", err)
		return false
	}

	err = os.WriteFile(outputPath, decodedData, 0644)
	if err != nil {
		fmt.Printf("Error writing to %s: %v\n", outputPath, err)
		return false
	}

	return true
}

func main() {
	// Define flags
	inputDir := flag.String("input-dir", "", "Path to the input directory containing .AppBundle files")
	outputDir := flag.String("output-dir", "", "Directory to save the output files")
	outputJSON := flag.String("output-file", "", "Path to the output JSON file")
	componentsXML := flag.String("components-xml", "", "Path to components XML file")
	flag.Parse()

	if *inputDir == "" || *outputDir == "" || *outputJSON == "" || *componentsXML == "" {
		fmt.Println("Usage: --input-dir <input_directory> --output-dir <output_directory> --output-file <output_file.json> --components-xml <components_file.xml>")
		return
	}

	// Read and parse components XML
	xmlData, err := os.ReadFile(*componentsXML)
	if err != nil {
		fmt.Println("Error reading components XML:", err)
		return
	}

	var components Components
	err = xml.Unmarshal(xmlData, &components)
	if err != nil {
		fmt.Println("Error parsing components XML:", err)
		return
	}

	// Create output directory
	err = os.MkdirAll(*outputDir, 0755)
	if err != nil {
		fmt.Println("Error creating output directory:", err)
		return
	}

	var packageList PackageList

	// Process each AppBundle file
	err = filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if !info.IsDir() && strings.HasSuffix(path, ".dwfs.AppBundle") {
			appBundleBasename := filepath.Base(path)
			potentialId := extractAppstreamId(appBundleBasename)

			// Look for matching component
			matchingComponent := findComponentById(&components, potentialId)
			if matchingComponent == nil {
				fmt.Printf("No matching component found for %s\n", potentialId)
				return nil
			}

			// Handle pbundle commands independently of component matching
			pbundleFlags := []string{"--pbundle_appstream", "--pbundle_pngIcon", "--pbundle_desktop"}
			extensions := []string{".appstream.xml", ".png", ".desktop"}

			for i, flag := range pbundleFlags {
				outputPath := filepath.Join(*outputDir, potentialId+extensions[i])
				if handlePBundleCommand(path, flag, outputPath) {
					fmt.Printf("Successfully extracted %s\n", extensions[i])
				} else {
					fmt.Printf("Skipping %s (not found in bundle)\n", extensions[i])
				}
			}

			// Get the size of the AppBundle file
			fileInfo, err := os.Stat(path)
			if err != nil {
				fmt.Printf("Error getting file info for %s: %v\n", path, err)
				return nil
			}
			sizeInMegabytes := float64(fileInfo.Size()) / (1024 * 1024)

			// Convert matching component to JSON
			item := ConvertComponentToItem(*matchingComponent)
			item.Pkg = appBundleBasename
			item.Icon = url_prefix + potentialId + ".png"
			item.Desktop = url_prefix + potentialId + ".desktop"
			item.Appstream = url_prefix + potentialId + ".appstream.xml"
			item.Size = fmt.Sprintf("%.2f MB", sizeInMegabytes)
			packageList.Pkg = append(packageList.Pkg, item)

			// Write individual component XML
			outputFile := filepath.Join(*outputDir, potentialId+".xml")
			if err := writeComponentToFile(*matchingComponent, outputFile); err != nil {
				fmt.Printf("Error writing component file: %v\n", err)
			}
		}
		return nil
	})

	if err != nil {
		fmt.Println("Error processing files:", err)
		return
	}

	// Write final JSON output
	jsonData, err := json.MarshalIndent(packageList, "", "  ")
	if err != nil {
		fmt.Println("Error creating JSON:", err)
		return
	}

	err = os.WriteFile(*outputJSON, jsonData, 0644)
	if err != nil {
		fmt.Println("Error writing JSON file:", err)
		return
	}

	fmt.Printf("Successfully processed AppBundles and wrote output to %s\n", *outputJSON)
}

func ConvertComponentToItem(c Component) Item {
	var downloadURL, srcURL, webURL string

	// Extract URLs
	for _, u := range c.Url {
		switch u.Type {
		case "source":
			srcURL = sanitizeURL(u.Url)
		case "homepage":
			webURL = sanitizeURL(u.Url)
		}
	}

	// Extract screenshots
	var screenshots []string
	for _, s := range c.Screenshots {
		screenshots = append(screenshots, sanitizeURL(s.Image.Url))
	}

	// Get the latest release version
	var version string
	if len(c.Releases.Release) > 0 {
		version = c.Releases.Release[0].Version
	}

	// Extract content for name, summary, description, and keywords based on missing xml:lang attribute
	var name, summary, richDescription string
	var keywords []string

	for _, item := range c.Name {
		if item.Lang == "" { // Default to English if xml:lang attribute is missing
			name = sanitizeString(item.Content)
			break
		}
	}

	for _, item := range c.Summary {
		if item.Lang == "" {
			summary = sanitizeString(item.Content)
			break
		}
	}

	for _, item := range c.Description {
		if item.Lang == "" { // FIXME: For some reason, I get russian sometimes?
			richDescription = sanitizeString(item.Content)
			break
		}
	}

	for _, item := range c.Keywords {
		if item.Lang == "" {
			keywords = append(keywords, sanitizeString(item.Content))
		}
	}

	// Join keywords for single-field output
	joinedKeywords := strings.Join(keywords, ", ")

	return Item{
		PkgName:         name,
		PkgId:           c.Id,
		Description:     summary,
		RichDescription: richDescription,
		Screenshots:     screenshots,
		Version:         version,
		DownloadURL:     downloadURL,
		SrcURL:          srcURL,
		Homepage:        webURL,
		Category:        c.Type,
		Keywords:        joinedKeywords,
		Note:            "Courtesy of AppBundleHUB",
	}
}

func writeComponentToFile(component Component, outputFile string) error {
	singleComponent := Components{
		XMLName:    xml.Name{Local: "components"},
		Version:    "0.8",
		Origin:     "flatpak",
		Components: []Component{component},
	}

	xmlData, err := xml.MarshalIndent(singleComponent, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshalling component to XML: %v", err)
	}

	return os.WriteFile(outputFile, xmlData, 0644)
}

// sanitizeString filters out non-alphanumeric and non-emoji characters
func sanitizeString(input string) string {
	// Define a regex pattern to match alphanumeric characters and common emoji ranges
	re := regexp.MustCompile(`[^[[:print:]]+`)
	return re.ReplaceAllString(input, "")
}

// sanitizeURL ensures that the URL is properly formatted and escaped
func sanitizeURL(input string) string {
	u, err := url.Parse(input)
	if err != nil {
		return ""
	}
	return u.String()
}
