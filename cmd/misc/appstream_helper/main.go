package main

import (
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Item struct {
	Pkg         string   `json:"pkg"`
	PkgName     string   `json:"pkg_name"`
	PkgId       string   `json:"pkg_id"`
	Icon        string   `json:"icon"`
	Description string   `json:"description"`
	Screenshots []string `json:"screenshots,omitempty"`
	Version     string   `json:"version"`
	DownloadURL string   `json:"download_url,omitempty"`
	Size        string   `json:"size,omitempty"`
	Bsum        string   `json:"bsum,omitempty"`
	Shasum      string   `json:"shasum,omitempty"`
	BuildDate   string   `json:"build_date,omitempty"`
	SrcURL      string   `json:"src_url,omitempty"`
	Homepage    string   `json:"homepage,omitempty"`
	BuildScript string   `json:"build_script,omitempty"`
	BuildLog    string   `json:"build_log,omitempty"`
	Category    string   `json:"category"`
	Note        string   `json:"note,omitempty"`
	Appstream   string   `json:"appstream,omitempty"`
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
	Name            string `xml:"name"`
	Summary         string `xml:"summary"`
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

	Description   Description   `xml:"description"`
	Screenshots   []Screenshot  `xml:"screenshots>screenshot"`
	Releases      Releases      `xml:"releases"`
	ContentRating ContentRating `xml:"content_rating"`
}

type Components struct {
	XMLName    xml.Name    `xml:"components"`
	Version    string      `xml:"version,attr"`
	Origin     string      `xml:"origin,attr"`
	Components []Component `xml:"component"`
}

func decodeBase64(data string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(data)
}

func executeProgram(programPath, flag string) (string, error) {
	cmd := exec.Command(programPath, flag)
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("error executing program: %v", err)
	}
	return string(output), nil
}

func ConvertComponentToItem(c Component, appbundleBasename string) Item {
	var downloadURL, srcURL, webURL, buildScript, buildLog string

	// Extract URLs
	for _, u := range c.Url {
		switch u.Type {
		case "source":
			srcURL = u.Url
		case "homepage":
			webURL = u.Url
		}
	}

	// Extract screenshots
	var screenshots []string
	for _, s := range c.Screenshots {
		screenshots = append(screenshots, s.Image.Url)
	}

	// Get the latest release version
	var version string
	if len(c.Releases.Release) > 0 {
		version = c.Releases.Release[0].Version
	}

	// Build the Item struct
	item := Item{
		Pkg:         appbundleBasename,
		PkgName:     c.Name,
		PkgId:       c.Id,
		Icon:        fmt.Sprintf("%s.png", appbundleBasename),
		Description: c.Summary,
		Screenshots: screenshots,
		Version:     version,
		DownloadURL: downloadURL,
		SrcURL:      srcURL,
		Homepage:    webURL,
		BuildScript: buildScript,
		BuildLog:    buildLog,
		Category:    c.Type,
		Note:        "Courtesy of AppBundleHUB",
		Appstream:   fmt.Sprintf("%s.appstream.xml", appbundleBasename),
	}

	return item
}

func writeComponentToFile(component Component, outputFile string) error {
	// Create a new Components struct with a single component
	singleComponent := Components{
		XMLName:    xml.Name{Local: "components"},
		Version:    "BEST",
		Origin:     "AppBundleHUB",
		Components: []Component{component},
	}

	// Marshal the single component to XML
	xmlData, err := xml.MarshalIndent(singleComponent, "", "  ")
	if err != nil {
		return fmt.Errorf("error marshalling component to XML: %v", err)
	}

	// Write the XML data to the output file
	err = os.WriteFile(outputFile, xmlData, 0644)
	if err != nil {
		return fmt.Errorf("error writing XML to file: %v", err)
	}

	return nil
}

func main() {
	// Define flags
	inputDir := flag.String("input-dir", "", "Path to the input directory containing .AppBundle files")
	outputDir := flag.String("outdir", "", "Directory to save the output XML files")
	outputJSON := flag.String("outfile", "", "Path to the output JSON file")
	flag.Parse()

	if *inputDir == "" || *outputDir == "" || *outputJSON == "" {
		fmt.Println("Usage: --input-dir <input_directory> --outdir <output_directory> --outfile <output_file.json>")
		return
	}

	err := os.MkdirAll(*outputDir, 0755)
	if err != nil {
		fmt.Println("Error creating output directory:", err)
		return
	}

	var packageList PackageList

	filepath.Walk(*inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && filepath.Ext(path) == ".AppBundle" {
			programFile := filepath.Base(path)
			programName := strings.TrimSuffix(programFile, ".dwfs.AppBundle")

			flags := []string{"--pbundle_appstream", "--pbundle_pngIcon", "--pbundle_desktop"}
			extensions := []string{".appstream.xml", ".png", ".desktop"}

			for i, flag := range flags {
				data, err := executeProgram(path, flag)
				if err != nil {
					fmt.Printf("Error executing program %s with %s: %v\n", programFile, flag, err)
					continue
				}

				decodedData, err := decodeBase64(data)
				if err != nil {
					fmt.Printf("Error decoding base64 data for %s: %v\n", programFile, err)
					continue
				}

				filePath := filepath.Join(*outputDir, fmt.Sprintf("%s%s", programName, extensions[i]))
				err = os.WriteFile(filePath, decodedData, 0644)
				if err != nil {
					fmt.Printf("Error writing data to file %s: %v\n", filePath, err)
					continue
				}
			}

			fmt.Printf("Files for %s saved to %s\n", programFile, *outputDir)

			// Read the input XML file
			xmlData, err := os.ReadFile(filepath.Join(*outputDir, fmt.Sprintf("%s.appstream.xml", programName)))
			if err != nil {
				fmt.Println("Error reading input file:", err)
				return err
			}

			// Unmarshal the XML data
			var components Components
			err = xml.Unmarshal(xmlData, &components)
			if err != nil {
				fmt.Println("Error unmarshalling XML:", err)
				return err
			}

			// Extract and write the components
			for _, component := range components.Components {
				if *outputDir != "" {
					outputFile := fmt.Sprintf("%s/%s.xml", *outputDir, component.Id)
					err = writeComponentToFile(component, outputFile)
					if err != nil {
						fmt.Printf("Error writing component %s to file: %v\n", component.Id, err)
					} else {
						fmt.Printf("Component %s written to %s\n", component.Id, outputFile)
					}
				}

				if *outputJSON != "" {
					// Convert to Item
					item := ConvertComponentToItem(component, programName)
					packageList.Pkg = append(packageList.Pkg, item)
				}
			}
		}
		return nil
	})

	if *outputJSON != "" {
		// Marshal the PackageList to JSON
		jsonData, err := json.MarshalIndent(packageList, "", "  ")
		if err != nil {
			fmt.Println("Error marshalling JSON:", err)
			return
		}

		// Write the JSON data to the output file
		err = os.WriteFile(*outputJSON, jsonData, 0644)
		if err != nil {
			fmt.Println("Error writing output file:", err)
			return
		}

		fmt.Println("Conversion successful! JSON data written to", *outputJSON)
	}
}
