package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/u-root/u-root/pkg/ldd"
)

// Constants for directory structure
const (
	defaultDstDir    = "output"
	defaultSharedDir = "shared"
	defaultLibDir    = "lib"
	defaultBinDir    = "bin"
)

var (
	strip       = flag.Bool("strip", false, "Strip debug symbols")
	oneDir      = flag.Bool("one-dir", true, "Use one directory for output")
	createLinks = flag.Bool("create-links", true, "Create symlinks in the bin directory")
	dstDirPath  = flag.String("dst-dir", defaultDstDir, "Destination directory for libraries and binaries")
)

// makeExecutable makes a file executable
func makeExecutable(filePath string) error {
	if err := os.Chmod(filePath, 0755); err != nil {
		return fmt.Errorf("failed to chmod +x %s: %v", filePath, err)
	}
	return nil
}

// tryStrip attempts to strip the binary if the flag is set
func tryStrip(filePath string) error {
	if *strip {
		stripPath, err := exec.LookPath("strip")
		if err != nil {
			return fmt.Errorf("strip command not found: %v", err)
		}

		// Execute the strip command
		cmd := exec.Command(stripPath, filePath)
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to strip %s: %v", filePath, err)
		}
	}
	return nil
}

// getLibs retrieves the list of libraries that a binary depends on
func getLibs(binaryPath string) ([]string, error) {
	dependencies, err := ldd.FList(binaryPath)
	if err != nil {
		return nil, err
	}
	return dependencies, nil
}

// copyFile copies a file from source to destination
func copyFile(src, dst string) error {
	input, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, input, 0644)
}

// createSymlink creates a symlink at dst pointing to src
func createSymlink(src, dst string) error {
	return os.Symlink(src, dst)
}

// Check if the binary is a dynamic executable
func isDynamic(binaryPath string) (bool, error) {
	cmd := exec.Command("ldd", binaryPath)
	output, _ := cmd.CombinedOutput()
	// If ldd returns an error, assume the binary is static
	//if err != nil {
	//	log.Printf("ldd error: %v, assuming static binary for: %s", err, binaryPath)
	//	return false, nil
	//}

	outputLower := strings.ToLower(string(output))
	if strings.Contains(outputLower, "not a dynamic executable") || strings.Contains(outputLower, "not a valid dynamic program") {
		return false, nil
	}
	return true, nil
}

// processBinary processes a binary file and decides whether to place it in the bin directory or shared/bin
func processBinary(binaryPath, dynExecPath string) error {
	fileInfo, err := os.Stat(binaryPath)
	if err != nil {
		return err
	}

	if !fileInfo.Mode().IsRegular() {
		return fmt.Errorf("skipped: %s is not a regular file", binaryPath)
	}

	// Create the main destination directory
	if err := os.MkdirAll(*dstDirPath, 0755); err != nil {
		return err
	}

	// Check if the binary is dynamic
	dynamic, err := isDynamic(binaryPath)
	if err != nil {
		return err
	}

	// Handle dynamic binaries
	if dynamic {
		// Create the shared directory structure for dynamic executables
		sharedDir := filepath.Join(*dstDirPath, defaultSharedDir)
		sharedBinDir := filepath.Join(sharedDir, defaultBinDir)
		sharedLibDir := filepath.Join(sharedDir, defaultLibDir)

		if err := os.MkdirAll(sharedBinDir, 0755); err != nil {
			return err
		}

		if err := os.MkdirAll(sharedLibDir, 0755); err != nil {
			return err
		}

		sharedBinaryPath := filepath.Join(sharedBinDir, fileInfo.Name())

		// Copy the binary to the shared bin directory
		if err := copyFile(binaryPath, sharedBinaryPath); err != nil {
			return err
		}

		// Chmod +x
		if err := makeExecutable(sharedBinaryPath); err != nil {
			return err
		}

		// Strip the binary if the strip flag is set
		if err := tryStrip(sharedBinaryPath); err != nil {
			return err
		}

		// Get the list of libraries the binary depends on
		libPaths, err := getLibs(binaryPath)
		if err != nil {
			return err
		}

		// Copy libraries to the shared lib directory
		for _, libPath := range libPaths {
			dstLibPath := filepath.Join(sharedLibDir, filepath.Base(libPath))
			if err := copyFile(libPath, dstLibPath); err != nil {
				return err
			}

			// Strip libraries if the strip flag is set
			if err := tryStrip(dstLibPath); err != nil {
				return err
			}
		}

		// Create the bin directory and symlink to the shared binary
		binDir := filepath.Join(*dstDirPath, defaultBinDir)
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return err
		}

		symlinkPath := filepath.Join(binDir, fileInfo.Name())
		if *createLinks { // Ugly as fuck. TODO: Find a better way or prettify
			oPWD, err := os.Getwd()
			if err != nil {
				return err
			}
			os.Chdir(filepath.Dir(symlinkPath))
			if err := createSymlink("../sharun", filepath.Join("../", defaultBinDir, filepath.Base(symlinkPath))); err != nil { // TODO, don't hardcode sharun
				os.Chdir(oPWD)
				return err
			}
			os.Chdir(oPWD)
		}

		//if *createLinks {
		//	if err := createSymlink(filepath.Join("..", dynExecPath), symlinkPath); err != nil {
		//		return err
		//	}
		//}

		//symlinkPath := filepath.Join(binDir, fileInfo.Name())
		//if *createLinks {
		//	if err := createSymlink(filepath.Join("..", defaultSharedDir, defaultBinDir, fileInfo.Name()), symlinkPath); err != nil {
		//		return err
		//	}
		//}
	} else {
		// Handle static binaries: Copy directly to the bin directory
		binDir := filepath.Join(*dstDirPath, defaultBinDir)
		if err := os.MkdirAll(binDir, 0755); err != nil {
			return err
		}

		staticBinaryPath := filepath.Join(binDir, fileInfo.Name())
		if err := copyFile(binaryPath, staticBinaryPath); err != nil {
			return err
		}

		// Chmod +x
		if err := makeExecutable(staticBinaryPath); err != nil {
			return err
		}

		// Strip the binary if the strip flag is set
		if err := tryStrip(staticBinaryPath); err != nil {
			return err
		}
	}

	fmt.Printf("Processed: %s\n", fileInfo.Name())
	return nil
}

// findDynExec finds the dynexec executable in the user's $PATH
func findDynExec() (string, error) {
	path, err := exec.LookPath("sharun")
	if err != nil {
		return "", fmt.Errorf("sharun not found in PATH: %v", err)
	}
	return path, nil
}

// copyDynExec copies the sharun executable to the destination directory and makes it executable
func copyDynExec(dynExecPath, dstDynExecPath string) error {
	if err := copyFile(dynExecPath, dstDynExecPath); err != nil {
		log.Fatalf("Unable to copy dynexec: %v", err)
	}

	if err := makeExecutable(dstDynExecPath); err != nil {
		return err
	}

	fmt.Printf("Copied and made executable: %s\n", dstDynExecPath)
	return nil
}

func main() {
	flag.Parse()

	dynExecPath, err := findDynExec()
	if err != nil {
		log.Fatalf("%v", err)
	}
	dstDynExecPath := filepath.Join(*dstDirPath, "sharun")

	if err := copyDynExec(dynExecPath, dstDynExecPath); err != nil {
		log.Printf("sharun not found in PATH or failed to copy: %v\n", err)
	}

	// Process any additional binaries passed as arguments
	binaryList := flag.Args()
	if len(binaryList) == 0 {
		fmt.Println("Error: Specify the ELF binary executable!")
		os.Exit(1)
	}

	if *oneDir && *dstDirPath == "" {
		*dstDirPath = defaultDstDir
	}

	for _, binary := range binaryList {
		if err := processBinary(binary, dstDynExecPath); err != nil {
			log.Printf("Error processing %s: %v\n", binary, err)
		}
	}
}
