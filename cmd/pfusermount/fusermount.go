package main

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

//go:embed fusermount
var fusermount embed.FS

func main() {
	// Extract the fusermount binary to a temporary location
	tempDir := os.TempDir()
	fusermountPath := filepath.Join(tempDir, "fusermount")
	fusermountData, err := fusermount.ReadFile("fusermount")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading embedded fusermount: %v\n", err)
		os.Exit(1)
	}
	err = os.WriteFile(fusermountPath, fusermountData, 0755)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error writing fusermount to temp directory: %v\n", err)
		os.Exit(1)
	}

	// Check if unshare is available
	unshareCmd := exec.Command("unshare")
	var out bytes.Buffer
	unshareCmd.Stdout = &out
	err = unshareCmd.Run()

	var cmd *exec.Cmd
	if err == nil {
		// unshare is available, use unshare
		args := []string{"--mount", "--user", "-r", fusermountPath}
		args = append(args, os.Args[1:]...)
		cmd = exec.Command("unshare", args...)
	} else {
		// unshare is not available, run fusermount directly
		cmd = exec.Command(fusermountPath, os.Args[1:]...)
	}

	// Set stdout and stderr for the command
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	// Run the command
	_ = cmd.Run()
}
