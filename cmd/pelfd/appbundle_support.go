package main

import (
	"encoding/base64"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"path/filepath"
)

func integrateAppBundle(path, appPath string, entry *BundleEntry) {
	baseName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	entry.Png = executeAppBundle(path, "--pbundle_pngIcon", filepath.Join(appPath, baseName+".png"))
	entry.Svg = executeAppBundle(path, "--pbundle_svgIcon", filepath.Join(appPath, baseName+".svg"))
	entry.Desktop = executeAppBundle(path, "--pbundle_desktop", filepath.Join(appPath, baseName+".desktop"))
}

func executeAppBundle(bundle, param, outputFile string) string {
	logMessage("INF", fmt.Sprintf("Retrieving metadata from %s with parameter: %s", bundle, param))
	// Prepend `sh -c` to the bundle execution
	cmd := exec.Command("sh", "-c", bundle+" "+param)
	output, err := cmd.Output()
	if err != nil {
		logMessage("WRN", fmt.Sprintf("Bundle %s with parameter %s didn't return a metadata file", bundle, param))
		return ""
	}

	outputStr := string(output)

	// Remove the escape sequence "^[[1F^[[2K"
	// Remove the escape sequence from the output
	outputStr = strings.ReplaceAll(outputStr, "\x1b[1F\x1b[2K", "")

	data, err := base64.StdEncoding.DecodeString(outputStr)
	if err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to decode base64 output for %s %s: %v", bundle, param, err))
		return ""
	}

	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		logMessage("ERR", fmt.Sprintf("Failed to write file %s: %v", outputFile, err))
		return ""
	}

	logMessage("INF", fmt.Sprintf("Successfully wrote file: %s", outputFile))
	return outputFile
}
