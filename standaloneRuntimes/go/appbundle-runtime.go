package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"debug/elf"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"runtime"

	"github.com/emmansun/base64"         // Drop-in replacement of "encoding/base64". (SIMD optimized)
	"github.com/klauspost/compress/gzip" // Drop-in replacement of "compress/gzip" (SIMD optimized)
)

const (
	// ANSI color codes
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"
	// Filesystem option's defaults
    DWARFS_CACHESIZE = "128M"
    DWARFS_BLOCKSIZE = "8K"
)

// RuntimeConfig holds the configuration for the AppBundle runtime
type RuntimeConfig struct {
	poolDir        string
	workDir        string
	// Depends on exeName in order to be populated correctly:
	rExeName       string // holds a version of exeName that can be used as a filepath or name for an variable
	mountDir       string
	execFile       string
	selfPath       string
	staticToolsDir string
	// Embedded within the .AppBundle itself:
	exeName        string
	pelfHost       string
	pelfVersion    string
	appBundleFS    string
}

var (
	globalArgs  []string
	elfFileSize int64
)

/* Supported filesystems */

// mountSquashfs mounts the embedded SquashFS archive
func (cfg *RuntimeConfig) mountSquashfs() error {
    if err := cfg.checkFuse(); err != nil {
        return err
    }

	uid := syscall.Getuid()
    gid := syscall.Getgid()

	_, archiveOffset, err := findMarkers(cfg.selfPath)
    if err != nil {
        return fmt.Errorf("failed to find markers: %v", err)
    }

    logFile := filepath.Join(cfg.workDir, ".squashfuse.log")
    if _, err := os.Stat(logFile); os.IsNotExist(err) {
        if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
            return err
        }

        cmd := exec.Command("squashfuse",
            "-f",
            "-o", "ro,nodev,noatime",
            "-o", fmt.Sprintf("uid=%d,gid=%d", uid, gid),
            "-o", fmt.Sprintf("offset=%d", archiveOffset),
            cfg.selfPath,
            cfg.mountDir,
        )
        output, err := cmd.CombinedOutput()
        if err != nil {
            logWarning(fmt.Sprintf("Failed to mount Squashfs archive: %v", err))
            logWarning(string(output))
            return err
        }

        if err := os.WriteFile(logFile, output, 0644); err != nil {
            return err
        }
    }

    return nil
}

// mountDwarfs mounts the embedded DwarFS archive
func (cfg *RuntimeConfig) mountDwarfs() error {
    if err := cfg.checkFuse(); err != nil {
        return err
    }

    logFile := filepath.Join(cfg.workDir, ".dwarfs.log")
    if _, err := os.Stat(logFile); os.IsNotExist(err) {
        if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
            return err
        }

        cmd := exec.Command("dwarfs", "-o", "offset=auto,ro,auto_unmount", cfg.selfPath, cfg.mountDir)
        output, err := cmd.CombinedOutput()
        if err != nil {
            logWarning(fmt.Sprintf("Failed to mount DwarFS archive: %v", err))
            logWarning(string(output))
            return err
        }

        if err := os.WriteFile(logFile, output, 0644); err != nil {
            return err
        }
    }

    return nil
}

// Dwarfs helper functions

// getDwarfsCacheSize gets the cache size from env or default
func getDwarfsCacheSize() string {
    cacheSize := os.Getenv("DWARFS_CACHESIZE")
    if cacheSize == "" {
        return DWARFS_CACHESIZE
    }
    opts := strings.Split(cacheSize, ",")
    if len(opts) > 0 {
        return opts[0]
    }
    return DWARFS_CACHESIZE
}

// getDwarfsBlockSize gets the block size from env or default
func getDwarfsBlockSize() string {
    blockSize := os.Getenv("DWARFS_BLOCKSIZE")
    if blockSize == "" {
        return DWARFS_BLOCKSIZE
    }
    opts := strings.Split(blockSize, ",")
    if len(opts) > 0 {
        return opts[0]
    }
    return DWARFS_BLOCKSIZE
}

// getDwarfsWorkers gets the number of workers from env or CPU count
func getDwarfsWorkers() string {
    workers := os.Getenv("DWARFS_WORKERS")
    if workers == "" {
        return fmt.Sprintf("%d", runtime.NumCPU())
    }
    opts := strings.Split(workers, ",")
    if len(opts) > 0 {
        return opts[0]
    }
    return fmt.Sprintf("%d", runtime.NumCPU())
}

/* --------------------- */

// initConfig initializes the runtime configuration
func initConfig() *RuntimeConfig {
	cfg := &RuntimeConfig{
		exeName:  os.Getenv("EXE_NAME"),
		poolDir:  filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath: getSelfPath(),
	}

	// Calculate the ELF file size and store it globally
	var err error
	elfFileSize, err = getElfFileSize(cfg.selfPath)
	if err != nil {
		logError("Failed to calculate ELF file size", err)
	}

	// Read the current executable to find placeholders
	if err := readPlaceholders(cfg); err != nil {
		logError("Failed to read placeholders", err)
	}

	if cfg.exeName == "" {
		logError("Unable to proceed without an AppBundleID (was it not injected correctly?)", nil)
	}

	cfg.rExeName = sanitizeFilename(cfg.exeName)
	cfg.workDir = getWorkDir(cfg)
	cfg.mountDir = filepath.Join(cfg.workDir, "mounted")
	cfg.execFile = filepath.Join(cfg.mountDir, "AppRun")

	if err := os.MkdirAll(cfg.workDir, 0755); err != nil {
		logError("Failed to create work directory", err)
	}

	return cfg
}

func readPlaceholders(cfg *RuntimeConfig) error {
	file, err := os.Open(cfg.selfPath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Seek to the end of the ELF file using the globally stored size
	if _, err := file.Seek(elfFileSize, 0); err != nil {
		return fmt.Errorf("failed to seek to the end of the ELF file: %w", err)
	}

	// Read the remaining data from the file
	remainingData, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read remaining data: %w", err)
	}

	// Split the remaining data into lines
	lines := strings.Split(string(remainingData), "\n")

	for _, line := range lines {
		if cfg.exeName == "" && strings.HasPrefix(line, "__APPBUNDLE_ID__: ") {
			cfg.exeName = strings.TrimSpace(strings.TrimPrefix(line, "__APPBUNDLE_ID__: "))
		} else if strings.HasPrefix(line, "__PELF_VERSION__: ") {
			cfg.pelfVersion = strings.TrimSpace(strings.TrimPrefix(line, "__PELF_VERSION__: "))
		} else if strings.HasPrefix(line, "__PELF_HOST__: ") {
			cfg.pelfHost = strings.TrimSpace(strings.TrimPrefix(line, "__PELF_HOST__: "))
		} else if strings.HasPrefix(line, "__APPBUNDLE_FS__: ") {
			cfg.appBundleFS = strings.TrimSpace(strings.TrimPrefix(line, "__APPBUNDLE_FS__: "))
		}
		if cfg.exeName != "" && cfg.rExeName != "" && cfg.mountDir != "" && cfg.appBundleFS != "" {
			break
		}
	}

	if cfg.exeName == "" || cfg.pelfHost == "" || cfg.pelfVersion == "" || cfg.appBundleFS == "" {
		return fmt.Errorf("missing placeholders in file: exeName=%q, pelfHost=%q, pelfVersion=%q, appBundleFS=%q",
			cfg.exeName, cfg.pelfHost, cfg.pelfVersion, cfg.appBundleFS)
	}

	return nil
}

// getSelfPath returns the absolute path of the current executable
func getSelfPath() string {
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		logError("Failed to resolve executable path", err)
	}
	return path
}

// findMarkers finds both __STATIC_TOOLS__ and __ARCHIVE_MARKER__ line numbers
func findMarkers(path string) (int64, int64, error) {
    file, err := os.Open(path)
    if err != nil {
        return 0, 0, err
    }
    defer file.Close()

    reader := bufio.NewReader(file)
    var staticToolsOffset, archiveOffset int64
    currentOffset := int64(0)

    for {
        line, err := reader.ReadBytes('\n')
        if err != nil && err != io.EOF {
            return 0, 0, fmt.Errorf("read error: %w", err)
        }

        // For __STATIC_TOOLS__, we want the offset of the NEXT line
        if strings.TrimSpace(string(line)) == "__STATIC_TOOLS__" {
            staticToolsOffset = currentOffset + int64(len(line))
        } else if strings.TrimSpace(string(line)) == "__ARCHIVE_MARKER__" {
            archiveOffset = currentOffset + int64(len(line))
            break
        }

        currentOffset += int64(len(line))
        
        if err == io.EOF {
            break
        }
    }

    if staticToolsOffset == 0 || archiveOffset == 0 {
        return 0, 0, errors.New("markers not found")
    }

    //fmt.Printf("staticToolsOffset: %d\n", staticToolsOffset)
    //fmt.Printf("archiveOffset: %d\n", archiveOffset)

    return staticToolsOffset, archiveOffset, nil
}

// sanitizeFilename removes non-alphanumeric characters
func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return -1
	}, name)
}

// getWorkDir determines the work directory for the AppBundle
func getWorkDir(cfg *RuntimeConfig) string {
	// Check for existing work directory from environment
	envKey := fmt.Sprintf("%s_workDir", cfg.rExeName)
	workDir := os.Getenv(envKey)

	if workDir == "" {
		workDir = filepath.Join(cfg.poolDir, fmt.Sprintf("pbundle_%s%d", cfg.rExeName, os.Getpid()))
	}

	return workDir
}

// logWarning prints a warning message to stderr
func logWarning(msg string) {
	fmt.Fprintf(os.Stderr, "AppBundle Runtime %sWarning%s: %s\n", warningColor, resetColor, msg)
}

// logError prints an error message to stderr, calls cleanup(), and exits
func logError(msg string, err error) {
	if msg != "" {
		if err != nil {
			fmt.Fprintf(os.Stderr, "AppBundle Runtime %sError%s: %s: %v\n", errorColor, resetColor, msg, err)
		} else {
			fmt.Fprintf(os.Stderr, "AppBundle Runtime %sError%s: %s\n", errorColor, resetColor, msg)
		}
	}
	cleanup()
	os.Exit(1)
}

// checkFuse verifies availability of dwarfs and fusermount3
func (cfg *RuntimeConfig) checkFuse() error {
	if cmdExists("dwarfs") && cmdExists("fusermount3") {
		return nil
	}

	// Extract static tools if not found in PATH
	staticToolsMarker, err := findStaticToolsMarker(cfg.selfPath)
	if err != nil {
		return fmt.Errorf("failed to locate static tools marker: %v", err)
	}

	cfg.staticToolsDir = filepath.Join(cfg.workDir, "static", getSystemArchString())
	if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
		return fmt.Errorf("failed to create static tools directory: %v", err)
	}

	if err := extractStaticTools(cfg.selfPath, staticToolsMarker, cfg.staticToolsDir); err != nil {
		return fmt.Errorf("failed to extract static tools: %v", err)
	}

	// Update PATH with extracted tools
	updatePath([]string{cfg.staticToolsDir})

	// Recheck for tools
	if !cmdExists("dwarfs") && !cmdExists("fusermount3") {
		return errors.New("neither dwarfs nor fusermount3 are available")
	}

	return nil
}

// cmdExists checks if a command is available in PATH
func cmdExists(cmd string) bool {
	_, err := exec.LookPath(cmd)
	return err == nil
}

// getSystemArchString returns a system-specific architecture string
func getSystemArchString() string {
	cmd := exec.Command("uname", "-om")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.ReplaceAll(strings.TrimSpace(string(output)), " ", "_")
}

// findStaticToolsMarker finds the line number of __STATIC_TOOLS__ marker
func findStaticToolsMarker(path string) (int, error) {
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	// Seek to the end of the ELF file using the globally stored size
	if _, err := file.Seek(elfFileSize, 0); err != nil {
		return 0, fmt.Errorf("failed to seek to the end of the ELF file: %w", err)
	}

	scanner := bufio.NewScanner(file)
	for lineNum := 1; scanner.Scan(); lineNum++ {
		if strings.Contains(scanner.Text(), "__STATIC_TOOLS__") {
			return lineNum + 1, nil
		}
	}

	return 0, errors.New("static tools marker not found")
}

// extractStaticTools extracts the bundled tar archive
func extractStaticTools(sourcePath string, marker int, destDir string) error {
	file, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer file.Close()

	// Skip to the marker line
	scanner := bufio.NewScanner(file)
	for i := 1; i < marker; i++ {
		if !scanner.Scan() {
			return errors.New("unexpected end of file")
		}
	}

	// Decode base64 and extract tar.gz
	decoder := base64.NewDecoder(base64.StdEncoding, bytes.NewReader(scanner.Bytes()))
	gzReader, err := gzip.NewReader(decoder)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}

		path := filepath.Join(destDir, header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0755); err != nil {
				return err
			}
		case tar.TypeReg:
			file, err := os.Create(path)
			if err != nil {
				return err
			}
			defer file.Close()

			if _, err := io.Copy(file, tarReader); err != nil {
				return err
			}

			// Set executable permissions for tools
			if err := os.Chmod(path, 0755); err != nil {
				return err
			}
		}
	}

	return nil
}

// determineHome sets up portable home and config directories
func (cfg *RuntimeConfig) determineHome() string {
	selfHomeDir := cfg.selfPath + ".home"
	selfConfigDir := cfg.selfPath + ".config"

	setEnvIfExists := func(suffix, envVar, oldEnvVar string) string {
		dir := cfg.selfPath + suffix
		if _, err := os.Stat(dir); err == nil {
			// If the old environment variable is empty, set it
			oldValue := os.Getenv(oldEnvVar)
			if oldValue == "" {
				oldValue = os.Getenv(envVar)
				os.Setenv(oldEnvVar, oldValue)
			}
			os.Setenv(envVar, dir)
			return dir
		}
		return ""
	}

	// Use the return value to capture the directory
	setEnvIfExists(selfHomeDir, "HOME", "OLD_HOME")
	config := setEnvIfExists(selfConfigDir, "XDG_CONFIG_HOME", "OLD_XDG_CONFIG_HOME")

	return config // Return the config directory
}

// executeFile executes the specified file from the mounted directory
func (cfg *RuntimeConfig) executeFile(args []string) error {
	binDirs := []string{
		filepath.Join(cfg.mountDir, "bin"),
		filepath.Join(cfg.mountDir, "usr", "bin"),
		filepath.Join(cfg.mountDir, "shared", "bin"),
	}

	libDirs := []string{
		filepath.Join(cfg.mountDir, "lib"),
		filepath.Join(cfg.mountDir, "usr", "lib"),
		filepath.Join(cfg.mountDir, "shared", "lib"),
		filepath.Join(cfg.mountDir, "lib64"),
		filepath.Join(cfg.mountDir, "usr", "lib64"),
		filepath.Join(cfg.mountDir, "lib32"),
		filepath.Join(cfg.mountDir, "usr", "lib32"),
		filepath.Join(cfg.mountDir, "libx32"),
		filepath.Join(cfg.mountDir, "usr", "libx32"),
	}

	// Set library and binary environment variables
	os.Setenv(fmt.Sprintf("%s_libDir", cfg.rExeName), strings.Join(libDirs, ":"))
	os.Setenv(fmt.Sprintf("%s_binDir", cfg.rExeName), strings.Join(binDirs, ":"))
	os.Setenv(fmt.Sprintf("%s_mountDir", cfg.rExeName), cfg.mountDir)

	// Modify PATH
	updatePath(binDirs)

	// Set additional environment variables
	os.Setenv("SELF_TEMPDIR", cfg.mountDir)
	os.Setenv("SELF", cfg.selfPath)
	os.Setenv("ARGV0", filepath.Base(os.Args[0]))

	// Check if the executable exists
	if _, err := exec.LookPath(cfg.execFile); err != nil {
		return fmt.Errorf("executable %s does not exist", cfg.execFile)
	}

	// Execute the binary
	cmd := exec.Command(cfg.execFile, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	return cmd.Run()
}

// updatePath modifies the system PATH
func updatePath(binDirs []string) {
	overtakePath := os.Getenv("PBUNDLE_OVERTAKE_PATH") == "1"
	currentPath := os.Getenv("PATH")

	var newPath string
	if overtakePath {
		newPath = strings.Join(append(binDirs, currentPath), ":")
	} else {
		newPath = strings.Join(append([]string{currentPath}, binDirs...), ":")
	}

	os.Setenv("PATH", newPath)
}

// handleRuntimeFlags handles various special runtime commands
func (cfg *RuntimeConfig) handleRuntimeFlags(args []string) error {
	switch args[0] {
	case "--pbundle_help":
		fmt.Printf("This bundle was generated automatically by PELF %s, the machine on which it was created has the following \"uname -mrsp(v)\":\n %s \n", cfg.pelfVersion, cfg.pelfHost)
		fmt.Printf("Internal variables:\n")
		fmt.Printf("  cfg.exeName: %s%s%s\n", blueColor, cfg.exeName, resetColor)
		fmt.Printf("  cfg.rExeName: %s%s%s\n", blueColor, cfg.rExeName, resetColor)
		fmt.Printf("  cfg.mountDir: %s%s%s\n", blueColor, cfg.mountDir, resetColor)
		fmt.Printf("  cfg.workDir: %s%s%s\n", blueColor, cfg.workDir, resetColor)
		fmt.Println("Usage: <|--pbundle_help|--pbundle_list|--pbundle_link <binary>|--pbundle_pngIcon|--pbundle_svgIcon|--pbundle_desktop|--pbundle_appstream|--pbundle_portableHome|--pbundle_portableConfig|>")
		fmt.Println(`
        NOTE: EXE_NAME is the AppBundleID -> rEXE_NAME is the same, but sanitized to be used as a variable name
        NOTE: The -v option in uname may have not been saved, to allow for reproducibility (since uname -v will output the current date)
        NOTE: This runtime was made in Go. It is not the default runtime used by pelf-dwfs`)
		return fmt.Errorf("!no_return")

	case "--pbundle_list":
		return filepath.Walk(cfg.mountDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			fmt.Println(path)
			return fmt.Errorf("!no_return")
		})

	case "--pbundle_portableHome":
		homeDir := filepath.Join(cfg.selfPath + ".home")
		if err := os.MkdirAll(homeDir, 0755); err != nil {
			return err
		}
		cfg.determineHome()
		return fmt.Errorf("!no_return")

	case "--pbundle_portableConfig":
		configDir := filepath.Join(cfg.selfPath + ".config")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return err
		}
		cfg.determineHome()
		return fmt.Errorf("!no_return")

	case "--pbundle_link":
		if len(args) < 2 {
			return fmt.Errorf("missing binary argument for --pbundle_link")
		}
		os.Setenv("LD_LIBRARY_PATH", strings.Join([]string{os.Getenv("LD_LIBRARY_PATH"), filepath.Join(cfg.mountDir, "lib")}, ":"))
		cfg.exeName = args[1]
		globalArgs = args[2:]

	case "--pbundle_pngIcon":
		iconPath := filepath.Join(cfg.mountDir, ".DirIcon")
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("PNG icon not found", nil)

	case "--pbundle_svgIcon":
		iconPath := filepath.Join(cfg.mountDir, ".DirIcon.svg")
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("SVG icon not found", nil)

	case "--pbundle_desktop":
		return findAndEncodeFiles(cfg.mountDir, "*.desktop")

	case "--pbundle_appstream":
		return findAndEncodeFiles(cfg.mountDir, "*.xml")

	default:
		return nil
	}
	return nil
}

// cleanup unmounts and removes temporary directories
func cleanup() {
	cfg := initConfig()
	// Attempt to unmount
	cmd := exec.Command("fusermount3", "-u", cfg.mountDir)
	cmd.Run()

	// Wait and check if the mount point is unmounted
	for i := 1; i <= 5; i++ {
		if isMounted(cfg.mountDir) {
			sleep(i)
		} else {
			break
		}
	}

	// Force unmount if still mounted
	if isMounted(cfg.mountDir) {
		cmd = exec.Command("fusermount3", "-uz", cfg.mountDir)
		cmd.Run()
	}

	// Remove temporary directories
	os.RemoveAll(cfg.workDir)
	if isDirEmpty(cfg.poolDir) {
		os.RemoveAll(cfg.poolDir)
	}
}

// isMounted checks if a directory is mounted
func isMounted(dir string) bool {
    var stat syscall.Statfs_t
    if err := syscall.Statfs(dir, &stat); err != nil {
        return false
    }

    parentDir := filepath.Dir(dir)
    var parentStat syscall.Statfs_t
    if err := syscall.Statfs(parentDir, &parentStat); err != nil {
        return false
    }

    return stat.Fsid != parentStat.Fsid
}

// isDirEmpty checks if a directory is empty
func isDirEmpty(dir string) bool {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(files) == 0
}

// sleep for a given number of seconds
func sleep(seconds int) {
	<-time.After(time.Duration(seconds) * time.Second)
}

// encodeFileToBase64 encodes a file's content to base64 and prints it
func encodeFileToBase64(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	fmt.Println(encoded)
	return nil
}

// findAndEncodeFiles searches for files matching a pattern and encodes them to base64
func findAndEncodeFiles(dir, pattern string) error {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		logError(fmt.Sprintf("no files found matching pattern: %s", pattern), nil)
	}

	for _, file := range matches {
		if err := encodeFileToBase64(file); err != nil {
			return err
		}
	}
	return nil
}

func getElfFileSize(filePath string) (int64, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return 0, fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	elfFile, err := elf.NewFile(file)
	if err != nil {
		return 0, fmt.Errorf("not a valid ELF file: %w", err)
	}

	var maxOffset uint64
	for _, prog := range elfFile.Progs {
		endOffset := prog.Off + prog.Filesz
		if endOffset > maxOffset {
			maxOffset = endOffset
		}
	}

	return int64(maxOffset), nil
}

// mountImage mounts the image using the appropriate filesystem handler
func mountImage(cfg *RuntimeConfig) error {
    switch cfg.appBundleFS {
    case "squashfs":
        return cfg.mountSquashfs()
    case "dwarfs":
        return cfg.mountDwarfs()
    default:
        return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
    }
}

// main function to handle the logic
func main() {
	cfg := initConfig()
	defer func() {
		if r := recover(); r != nil {
			// Just don't make it worse
			logError("Entering cleanup(); Reason? Caught panic. ", fmt.Errorf("%v", r))
		}
		cleanup()
	}()

	if err := mountImage(cfg); err != nil {
		logError("Failed to mount image", err)
	}

	globalArgs = os.Args

	if len(globalArgs) > 1 {
		if err := cfg.handleRuntimeFlags(globalArgs[1:]); err != nil {
			logError("", nil)
		}
	}

	if err := cfg.executeFile(globalArgs[1:]); err != nil {
		logError("Failed to execute file", err)
	}
}
