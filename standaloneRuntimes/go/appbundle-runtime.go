package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"runtime"
	"math/rand"
	"encoding/hex"

	"github.com/emmansun/base64"         // Drop-in replacement of "encoding/base64". (SIMD optimized)
	"github.com/klauspost/compress/gzip" // Drop-in replacement of "compress/gzip" (SIMD optimized)
)

const (
	warningColor     = "\x1b[0;33m"
	errorColor       = "\x1b[0;31m"
	blueColor        = "\x1b[0;34m"
	resetColor       = "\x1b[0m"
	defaultCacheSize = "128M"
	defaultBlockSize = "8K"

	// Filesystem types
	fsTypeSquashfs = "squashfs"
	fsTypeDwarfs   = "dwarfs"

	// Buffer sizes
	defaultBufSize = 4096
	maxScanSize    = 1024 * 1024 // 1MB scan buffer

	// Dwarfs default options:
	DWARFS_CACHESIZE = "128M"
	DWARFS_BLOCKSIZE = "128K"
)

type RuntimeConfig struct {
	poolDir              string
	workDir              string
	rExeName             string
	mountDir             string
	entrypoint           string
	selfPath             string
	staticToolsDir       string
	exeName              string    // Will initially contain "__APPBUNDLE_ID__: "
	pelfHost             string    // Will initially contain "__PELF_HOST__: "
	pelfVersion          string    // Will initially contain "__PELF_VERSION__: "
	appBundleFS          string    // Will initially contain "__APPBUNDLE_FS__: "
	staticToolsOffset    uint32
	archiveOffset        uint32
	staticToolsEndOffset uint32
	elfFileSize          uint32
}

var (
	globalArgs  []string
	cfg         *RuntimeConfig
	binaryPaths = make(map[string]string) // Map to store full paths of binaries
)

// Filesystem-specific commands and extraction logic
var filesystemCommands = map[string][]string{
	fsTypeSquashfs: {"squashfuse", "fusermount3"},
	fsTypeDwarfs:   {"dwarfs", "fusermount3"},
}

var filesystemMountCmdBuilders = map[string]func() *exec.Cmd{
	fsTypeSquashfs: buildSquashFSCmd,
	fsTypeDwarfs:   buildDwarFSCmd,
}

// mountFS handles mounting both squashfs and dwarfs filesystems
func mountImage() error {
	if err := checkFuse(); err != nil {
		return err
	}

	logFile := filepath.Join(cfg.workDir, fmt.Sprintf(".%s.log", cfg.appBundleFS))
	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
			return err
		}

		cmdBuilder, ok := filesystemMountCmdBuilders[cfg.appBundleFS]
		if !ok {
			return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
		}

		cmd := cmdBuilder()
		output, err := cmd.CombinedOutput()
		if err != nil {
			logWarning(fmt.Sprintf("Failed to mount %s archive: %v", cfg.appBundleFS, err))
			logWarning(string(output))
			return err
		}

		return os.WriteFile(logFile, output, 0644)
	}
	return nil
}

func buildSquashFSCmd() *exec.Cmd {
	uid := uint32(syscall.Getuid())
	gid := uint32(syscall.Getgid())

	return exec.Command(binaryPaths["squashfuse"],
		"-o", "ro,nodev,noatime",
		"-o", fmt.Sprintf("uid=%d,gid=%d", uid, gid),
		"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
		cfg.selfPath,
		cfg.mountDir,
	)
}

func buildDwarFSCmd() *exec.Cmd {
	return exec.Command(binaryPaths["dwarfs"],
		"-o", "ro,nodev,noatime,auto_unmount",
		"-o", "cache_files,no_cache_image,clone_fd",
		"-o", fmt.Sprintf("cachesize=%s", getEnvWithDefault("DWARFS_CACHESIZE", defaultCacheSize)),
		"-o", fmt.Sprintf("blocksize=%s", getEnvWithDefault("DWARFS_BLOCKSIZE", defaultBlockSize)),
		"-o", fmt.Sprintf("workers=%s", getEnvWithDefault("DWARFS_WORKERS", fmt.Sprintf("%d", runtime.NumCPU()))),
		"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
		"-o", "debuglevel=error",
		cfg.selfPath,
		cfg.mountDir,
	)
}

// getEnvWithDefault returns environment variable value or default if not set
func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		opts := strings.Split(value, ",")
		if len(opts) > 0 {
			return opts[0]
		}
	}
	return defaultValue
}

func readPlaceholdersAndMarkers(cfg *RuntimeConfig) error {
	// Read the entire file into memory once
	data, err := os.ReadFile(cfg.selfPath)
	if err != nil {
		return fmt.Errorf("failed to read file: %w", err)
	}

	// Calculate ELF offset
	elfFile, err := elf.NewFile(bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to parse ELF file: %w", err)
	}

	var elfEndOffset uint32
	for _, prog := range elfFile.Progs {
		endOffset := uint32(prog.Off + prog.Filesz)
		if endOffset > elfEndOffset {
			elfEndOffset = endOffset
		}
	}
	cfg.elfFileSize = elfEndOffset

	// Process the file starting from the ELF offset
	data = data[elfEndOffset:]
	lines := strings.Split(string(data), "\n")

	var staticToolsFound, staticToolsEndFound, archiveMarkerFound bool
	currentOffset := uint32(elfEndOffset)

	for _, line := range lines {
		lineLen := uint32(len(line) + 1) // Include newline
		trimmedLine := strings.TrimSpace(line)

		// Each of these markers is followed by a newline, we want the offset of the line AFTER these markers. (except for the end marker of static tools)
		switch trimmedLine {
		case "__STATIC_TOOLS__":
			cfg.staticToolsOffset = currentOffset + lineLen
			staticToolsFound = true
		case "__STATIC_TOOLS_EOF__":
			cfg.staticToolsEndOffset = currentOffset
			staticToolsEndFound = true
		case "__ARCHIVE_MARKER__":
			cfg.archiveOffset = currentOffset + lineLen
			archiveMarkerFound = true
		}

		if staticToolsFound && archiveMarkerFound && staticToolsEndFound {
			// logWarning(fmt.Sprintf("cfg.archiveOffset: %d ; cfg.staticToolsOffset: %d ; cfg.staticToolsEndOffset: %d", cfg.archiveOffset, cfg.staticToolsOffset, cfg.staticToolsEndOffset))
			break
		}
		currentOffset += lineLen
	}

	if !staticToolsFound || !archiveMarkerFound {
		return fmt.Errorf("markers not found: staticToolsOffset=%d, staticToolsEndOffset=%d", cfg.staticToolsOffset, cfg.staticToolsEndOffset)
	}

	// Parse remaining data for placeholders
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

		// Exit early if all placeholders are found
		if cfg.exeName != "" && cfg.pelfVersion != "" && cfg.pelfHost != "" && cfg.appBundleFS != "" {
			break
		}
	}

	if cfg.exeName == "" || cfg.pelfVersion == "" || cfg.pelfHost == "" || cfg.appBundleFS == "" {
		return fmt.Errorf("missing placeholders: exeName=%q, pelfVersion=%q, pelfHost=%q, appBundleFS=%q",
			cfg.exeName, cfg.pelfVersion, cfg.pelfHost, cfg.appBundleFS)
	}

	return nil
}

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

func initConfig() *RuntimeConfig {
	cfg := &RuntimeConfig{
		exeName:  os.Getenv("EXE_NAME"),
		poolDir:  filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath: getSelfPath(),
	}

	if err := readPlaceholdersAndMarkers(cfg); err != nil {
		logError("Failed to read placeholders and markers", err)
	}

	if cfg.exeName == "" {
		logError("Unable to proceed without an AppBundleID (was it not injected correctly?)", nil)
	}

	cfg.rExeName = sanitizeFilename(cfg.exeName)
	cfg.workDir = getWorkDir(cfg)
	cfg.mountDir = filepath.Join(cfg.workDir, "mounted")
	cfg.entrypoint = filepath.Join(cfg.mountDir, "AppRun")

	if err := os.MkdirAll(cfg.workDir, 0755); err != nil {
		logError("Failed to create work directory", err)
	}

	return cfg
}

func getSelfPath() string {
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		logError("Failed to resolve executable path", err)
	}
	return path
}

func sanitizeFilename(name string) string {
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			return r
		}
		return -1
	}, name)
}

func getWorkDir(cfg *RuntimeConfig) string {
	envKey := fmt.Sprintf("%s_workDir", cfg.rExeName)
	workDir := os.Getenv(envKey)

	if workDir == "" {
		randomString, err := generateRandomString(8)
		if err != nil {
			logError("Failed to generate random string for workDir", err)
		}
		workDir = filepath.Join(cfg.poolDir, fmt.Sprintf("pbundle_%s%d%s", cfg.rExeName, os.Getpid(), randomString))
	}

	return workDir
}

func logWarning(msg string) {
	fmt.Fprintf(os.Stderr, "AppBundle Runtime %sWarning%s: %s\n", warningColor, resetColor, msg)
}

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

func checkFuse() error {
	requiredCmds, ok := filesystemCommands[cfg.appBundleFS]
	if !ok {
		return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
	}

	for _, cmd := range requiredCmds {
		if path, err := cmdExists(cmd); err == nil {
			cfg.staticToolsDir = filepath.Join(cfg.workDir, "static", getSystemArchString())
			if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
				return fmt.Errorf("failed to create static tools directory: %v", err)
			}

			if err := extractStaticTools(cfg); err != nil {
				return fmt.Errorf("failed to extract static tools: %v", err)
			}

			binaryPaths[cmd] = path
		}
	}

	return nil
}

// Check if a given command exists in the users' $PATH or was extracted and made available by extractStaticTools()
func cmdExists(cmd string) (string, error) {
	// First, check if the command exists in the system's PATH
	if path, err := exec.LookPath(cmd); err == nil {
		return path, nil
	}

	// Fallback: check if the command exists in our custom map
	if path, exists := binaryPaths[cmd]; exists {
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	return "", fmt.Errorf("unable to find [%v] in the user's $PATH", cmd)
}

func getSystemArchString() string {
	cmd := exec.Command("uname", "-om")
	output, err := cmd.Output()
	if err != nil {
		return "unknown"
	}
	return strings.ReplaceAll(strings.TrimSpace(string(output)), " ", "_")
}

func extractStaticTools(cfg *RuntimeConfig) error {
    file, err := os.Open(cfg.selfPath)
    if err != nil {
        return fmt.Errorf("failed to open AppBundle: %w", err)
    }
    defer file.Close()

    // Seek to static tools offset
    if _, err := file.Seek(int64(cfg.staticToolsOffset), io.SeekStart); err != nil {
        return fmt.Errorf("failed to seek to static tools: %w", err)
    }

    // Read the base64-encoded data
    base64Data := make([]byte, cfg.staticToolsEndOffset-cfg.staticToolsOffset)
    if _, err := io.ReadFull(file, base64Data); err != nil {
        return fmt.Errorf("failed to read static tools data: %w", err)
    }

    // Decode base64 data
    decodedData := make([]byte, base64.StdEncoding.DecodedLen(len(base64Data)))
    n, err := base64.StdEncoding.Decode(decodedData, base64Data)
    if err != nil {
        return fmt.Errorf("failed to decode base64 data: %w", err)
    }
    decodedData = decodedData[:n] // Trim to actual size

    // Decompress using gzip
    gzipReader, err := gzip.NewReader(bytes.NewReader(decodedData))
    if err != nil {
        return fmt.Errorf("failed to create gzip reader: %w", err)
    }
    defer gzipReader.Close()

    tarReader := tar.NewReader(gzipReader)
    for {
        hdr, err := tarReader.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return fmt.Errorf("failed to read tar entry: %w", err)
        }

        // Extract the file
        filePath := filepath.Join(cfg.staticToolsDir, hdr.Name)
        if hdr.Typeflag == tar.TypeDir {
            // If it's a directory, create it
            if err := os.MkdirAll(filePath, 0755); err != nil {
                return fmt.Errorf("failed to create directory: %w", err)
            }
        } else {
            // If it's a file, create the file
            if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
                return fmt.Errorf("failed to create directory: %w", err)
            }
            outFile, err := os.Create(filePath)
            if err != nil {
                return fmt.Errorf("failed to create file: %w", err)
            }
            if _, err := io.Copy(outFile, tarReader); err != nil {
                outFile.Close()
                return fmt.Errorf("failed to write file: %w", err)
            }
            outFile.Close()

            // Set file permissions
            if err := os.Chmod(filePath, 0755); err != nil {
                return fmt.Errorf("failed to set file permissions: %w", err)
            }

            // Update the binaryPaths map
            binaryPaths[filepath.Base(filePath)] = filePath
        }
    }

    return nil
}

func determineHome() string {
	selfHomeDir := cfg.selfPath + ".home"
	selfConfigDir := cfg.selfPath + ".config"

	setEnvIfExists := func(suffix, envVar, oldEnvVar string) string {
		dir := cfg.selfPath + suffix
		if _, err := os.Stat(dir); err == nil {
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

	setEnvIfExists(selfHomeDir, "HOME", "OLD_HOME")
	config := setEnvIfExists(selfConfigDir, "XDG_CONFIG_HOME", "OLD_XDG_CONFIG_HOME")

	return config
}

func executeFile(args []string) error {
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

	os.Setenv(fmt.Sprintf("%s_libDir", cfg.rExeName), strings.Join(libDirs, ":"))
	os.Setenv(fmt.Sprintf("%s_binDir", cfg.rExeName), strings.Join(binDirs, ":"))
	os.Setenv(fmt.Sprintf("%s_mountDir", cfg.rExeName), cfg.mountDir)

	updatePath(binDirs)

	os.Setenv("SELF_TEMPDIR", cfg.mountDir)
	os.Setenv("SELF", cfg.selfPath)
	os.Setenv("ARGV0", filepath.Base(os.Args[0]))

	if _, err := os.Stat(cfg.entrypoint); err != nil {
		return fmt.Errorf("executable %s does not exist", cfg.entrypoint)
	}

	cmd := exec.Command(cfg.entrypoint, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Run()

	return nil
}

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

func handleRuntimeFlags(args []string) error {
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
		determineHome()
		return fmt.Errorf("!no_return")

	case "--pbundle_portableConfig":
		configDir := filepath.Join(cfg.selfPath + ".config")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return err
		}
		determineHome()
		return fmt.Errorf("!no_return")

	case "--pbundle_link":
		if len(args) < 1 {
			return fmt.Errorf("missing binary argument for --pbundle_link")
		}
		os.Setenv("LD_LIBRARY_PATH", strings.Join([]string{os.Getenv("LD_LIBRARY_PATH"), filepath.Join(cfg.mountDir, "lib")}, ":"))
		cfg.exeName = args[1]
		globalArgs = args[1:]

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

func cleanup() {
	cmd := exec.Command("fusermount3", "-u", cfg.mountDir)
	cmd.Run()

	for i := 1; i <= 5; i++ {
		if isMounted(cfg.mountDir) {
			sleep(i)
		} else {
			break
		}
	}

	if isMounted(cfg.mountDir) {
		cmd = exec.Command("fusermount3", "-uz", cfg.mountDir)
		cmd.Run()
	}

	os.RemoveAll(cfg.workDir)
	if isDirEmpty(cfg.poolDir) {
		os.RemoveAll(cfg.poolDir)
	}
}

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

func isDirEmpty(dir string) bool {
	files, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	return len(files) == 0
}

func sleep(seconds int) {
	<-time.After(time.Duration(seconds) * time.Second)
}

func encodeFileToBase64(filePath string) error {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return err
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	fmt.Println(encoded)
	return nil
}

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

func main() {
	cfg = initConfig()
	defer func() {
		if r := recover(); r != nil {
			logError("Panic recovered", fmt.Errorf("%v", r))
		}
		cleanup()
	}()

	if err := mountImage(); err != nil {
		logError("Failed to mount image", err)
	}

	globalArgs = os.Args
	if len(globalArgs) > 1 {
		if err := handleRuntimeFlags(globalArgs[1:]); err != nil {
			if err.Error() != "!no_return" {
				logError("Runtime flag handling failed", err)
			} else {
				cleanup()
				os.Exit(0)
			}
		}
	}

	if err := executeFile(globalArgs[1:]); err != nil {
		logError("Failed to execute file", err)
	}
}

// Helpers:

func generateRandomString(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
