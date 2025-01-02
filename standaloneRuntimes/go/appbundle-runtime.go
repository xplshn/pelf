package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

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
	exeName              string // Will be populated with the value of "__APPBUNDLE_ID__: " as found within the file
	pelfHost             string // Will be populated with the value of "__PELF_HOST__: " as found within the file
	pelfVersion          string // Will be populated with the value of "__PELF_VERSION__: " as found within the file
	appBundleFS          string // Will be populated with the value of "__APPBUNDLE_FS__: " as found within the file
	staticToolsOffset    uint32
	archiveOffset        uint32
	staticToolsEndOffset uint32
	elfFileSize          uint32
}

type fileHandler struct {
	path string
	data []byte
	file *os.File
}

var binaryPaths = make(map[string]string) // Map to store full paths of binaries

// Filesystem-specific commands and extraction logic
var filesystemCommands = map[string][]string{
	fsTypeSquashfs: {"squashfuse", "fusermount3"},
	fsTypeDwarfs:   {"dwarfs", "fusermount3"},
}

var filesystemMountCmdBuilders = map[string]func(*RuntimeConfig) *exec.Cmd{
	fsTypeSquashfs: buildSquashFSCmd,
	fsTypeDwarfs:   buildDwarFSCmd,
}

// mountFS handles mounting both squashfs and dwarfs filesystems
func mountImage(cfg *RuntimeConfig, fh *fileHandler) error {
	if err := checkFuse(cfg, fh); err != nil {
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

		cmd := cmdBuilder(cfg)
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

func buildSquashFSCmd(cfg *RuntimeConfig) *exec.Cmd {
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

func buildDwarFSCmd(cfg *RuntimeConfig) *exec.Cmd {
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

func newFileHandler(path string) (*fileHandler, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	// Read the file in chunks
	const chunkSize = 4096
	data := make([]byte, 0, chunkSize)
	buffer := make([]byte, chunkSize)
	for {
		n, err := file.Read(buffer)
		if n > 0 {
			data = append(data, buffer[:n]...)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			file.Close()
			return nil, fmt.Errorf("read file: %w", err)
		}
	}

	return &fileHandler{path: path, data: data, file: file}, nil
}

func (f *fileHandler) readPlaceholdersAndMarkers(cfg *RuntimeConfig) error {
	elfFile, err := elf.NewFile(bytes.NewReader(f.data))
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	// Calculate ELF offset once
	var elfEndOffset uint32
	for _, prog := range elfFile.Progs {
		end := uint32(prog.Off + prog.Filesz)
		if end > elfEndOffset {
			elfEndOffset = end
		}
	}
	cfg.elfFileSize = elfEndOffset

	// Reuse the slice to avoid allocation
	f.data = f.data[elfEndOffset:]

	lines := strings.Split(string(f.data), "\n")

	var staticToolsFound, staticToolsEndFound, archiveMarkerFound bool
	currentOffset := elfEndOffset

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

func getEnvWithDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		opts := strings.Split(value, ",")
		if len(opts) > 0 {
			return opts[0]
		}
	}
	return defaultValue
}

func initConfig() (*RuntimeConfig, *fileHandler, error) {
	cfg := &RuntimeConfig{
		exeName:  os.Getenv("EXE_NAME"),
		poolDir:  filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath: getSelfPath(),
	}

	fh, err := newFileHandler(cfg.selfPath)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create file handler: %w", err)
	}

	if err := fh.readPlaceholdersAndMarkers(cfg); err != nil {
		logError("Failed to read placeholders and markers", err, cfg)
	}

	if cfg.exeName == "" {
		logError("Unable to proceed without an AppBundleID (was it not injected correctly?)", nil, cfg)
	}

	cfg.rExeName = sanitizeFilename(cfg.exeName)
	cfg.workDir = getWorkDir(cfg)
	cfg.mountDir = filepath.Join(cfg.workDir, "mounted")
	cfg.entrypoint = filepath.Join(cfg.mountDir, "AppRun")

	if err := os.MkdirAll(cfg.workDir, 0755); err != nil {
		logError("Failed to create work directory", err, cfg)
	}

	return cfg, fh, nil
}

func getSelfPath() string {
	path, err := filepath.EvalSymlinks(os.Args[0])
	if err != nil {
		logError("Failed to resolve executable path", err, nil)
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
			logError("Failed to generate random string for workDir", err, cfg)
		}
		workDir = filepath.Join(cfg.poolDir, fmt.Sprintf("pbundle_%s%d%s", cfg.rExeName, os.Getpid(), randomString))
	}

	return workDir
}

func logWarning(msg string) {
	fmt.Fprintf(os.Stderr, "AppBundle Runtime %sWarning%s: %s\n", warningColor, resetColor, msg)
}

func logError(msg string, err error, cfg *RuntimeConfig) {
	if msg != "" {
		if err != nil {
			fmt.Fprintf(os.Stderr, "AppBundle Runtime %sError%s: %s: %v\n", errorColor, resetColor, msg, err)
		} else {
			fmt.Fprintf(os.Stderr, "AppBundle Runtime %sError%s: %s\n", errorColor, resetColor, msg)
		}
	}
	if cfg != nil {
		cleanup(cfg)
	}
	os.Exit(1)
}

func checkFuse(cfg *RuntimeConfig, fh *fileHandler) error {
	requiredCmds, ok := filesystemCommands[cfg.appBundleFS]
	if !ok {
		return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
	}

	for _, cmd := range requiredCmds {
		if path, err := cmdExists(cmd); err == nil {
			binaryPaths[cmd] = path
		} else {
			cfg.staticToolsDir = filepath.Join(cfg.workDir, "static", getSystemArchString())
			if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
				return fmt.Errorf("failed to create static tools directory: %v", err)
			}

			if err := fh.extractStaticTools(cfg); err != nil {
				return fmt.Errorf("failed to extract static tools: %v", err)
			}

			if path, err := cmdExists(cmd); err == nil {
				binaryPaths[cmd] = path
			} else {
				return fmt.Errorf("unable to find [%v] in the user's $PATH or extracted tools", cmd)
			}
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

func (f *fileHandler) extractStaticTools(cfg *RuntimeConfig) error {
	// Seek to the start of the static tools section
	if _, err := f.file.Seek(int64(cfg.staticToolsOffset), io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek to static tools: %w", err)
	}

	// Calculate the length of the static tools section
	staticToolsLength := int64(cfg.staticToolsEndOffset - cfg.staticToolsOffset)

	// Read the static tools section into a buffer
	staticToolsData := make([]byte, staticToolsLength)
	if _, err := io.ReadFull(f.file, staticToolsData); err != nil {
		return fmt.Errorf("failed to read static tools section: %w", err)
	}

	// Decode base64 in-place to minimize allocations
	decodedSize := base64.StdEncoding.DecodedLen(len(staticToolsData))
	decodedData := make([]byte, decodedSize)
	n, err := base64.StdEncoding.Decode(decodedData, staticToolsData)
	if err != nil {
		return fmt.Errorf("base64 decode: %w", err)
	}
	decodedData = decodedData[:n]

	// Single gzip reader from memory buffer
	gz, err := gzip.NewReader(bytes.NewReader(decodedData))
	if err != nil {
		return fmt.Errorf("gzip init: %w", err)
	}
	defer gz.Close()

	// Process tar entries sequentially
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		fpath := filepath.Join(cfg.staticToolsDir, hdr.Name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", fpath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			f, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0755)
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}
			defer f.Close()
			if _, err := io.Copy(f, tr); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			binaryPaths[filepath.Base(fpath)] = fpath
		}
	}

	// Discard any remaining data after the static tools section
	//if _, err := io.CopyN(io.Discard, f.file, int64(uint32(len(f.data))-cfg.staticToolsEndOffset)); err != nil {
	//	return fmt.Errorf("failed to discard remaining data: %w", err)
	//}

	return nil
}

func determineHome(cfg *RuntimeConfig) string {
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

func executeFile(args []string, cfg *RuntimeConfig) error {
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

func handleRuntimeFlags(args *[]string, cfg *RuntimeConfig) error {
	switch (*args)[0] {
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
		determineHome(cfg)
		return fmt.Errorf("!no_return")

	case "--pbundle_portableConfig":
		configDir := filepath.Join(cfg.selfPath + ".config")
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return err
		}
		determineHome(cfg)
		return fmt.Errorf("!no_return")

	case "--pbundle_link":
		if len(*args) < 2 {
			return fmt.Errorf("missing binary argument for --pbundle_link")
		}
		os.Setenv("LD_LIBRARY_PATH", strings.Join([]string{os.Getenv("LD_LIBRARY_PATH"), filepath.Join(cfg.mountDir, "lib")}, ":"))
		cfg.exeName = (*args)[1]
		*args = (*args)[1:]

	case "--pbundle_pngIcon":
		iconPath := filepath.Join(cfg.mountDir, ".DirIcon")
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("PNG icon not found", nil, cfg)

	case "--pbundle_svgIcon":
		iconPath := filepath.Join(cfg.mountDir, ".DirIcon.svg")
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("SVG icon not found", nil, cfg)

	case "--pbundle_desktop":
		return findAndEncodeFiles(cfg.mountDir, "*.desktop", cfg)

	case "--pbundle_appstream":
		return findAndEncodeFiles(cfg.mountDir, "*.xml", cfg)

	default:
		return nil
	}
	return nil
}

func cleanup(cfg *RuntimeConfig) {
	cmd := exec.Command(os.Args[0], "--pbundle_internal_Cleanup", cfg.mountDir, cfg.poolDir, cfg.workDir)
	cmd.Env = os.Environ()
	// Discard/disable std{out,err,in}
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil

	// Start the command as a non-blocking background process
	if err := cmd.Start(); err != nil {
		logError("Failed to start cleanup process", err, cfg)
		return
	}

	// Detach the process to avoid waiting for it to finish
	if err := cmd.Process.Release(); err != nil {
		logError("Failed to detach cleanup process", err, cfg)
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

func findAndEncodeFiles(dir, pattern string, cfg *RuntimeConfig) error {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	if len(matches) == 0 {
		logError(fmt.Sprintf("no files found matching pattern: %s", pattern), nil, cfg)
	}

	for _, file := range matches {
		if err := encodeFileToBase64(file); err != nil {
			return err
		}
	}
	return nil
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--pbundle_internal_Cleanup" {
		if len(os.Args) < 5 {
			logError("Invalid number of arguments for --pbundle_internal_Cleanup", nil, nil)
		}
		mountDir := os.Args[2]
		poolDir := os.Args[3]
		workDir := os.Args[4]

		// Perform cleanup tasks
		for i := 0; i < 32; i++ {
			if !isMounted(mountDir) {
				//logWarning("No longer mounted!")
				break
			}
			cmd := exec.Command("fusermount3", "-u", mountDir)
			cmd.Run()
			sleep(1)
		}

		if isMounted(mountDir) {
			exec.Command("fusermount3", "-uz", mountDir).Run()
		}

		os.RemoveAll(workDir)
		if isDirEmpty(poolDir) {
			os.RemoveAll(poolDir)
		}

		// Write test message to /tmp/pbundle_log
		logFile, _ := os.OpenFile("/tmp/pbundle_log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		defer logFile.Close()
		logFile.WriteString("Test message: Cleanup performed\n")

		os.Exit(0)
	}

	cfg, fh, err := initConfig()
	if err != nil {
		logError("Failed to initialize config", err, cfg)
	}
	defer func() {
		if r := recover(); r != nil {
			logError("Panic recovered", fmt.Errorf("%v", r), cfg)
		}
		if fh != nil {
			fh.file.Close()
		}
		cleanup(cfg)
		os.Exit(0)
	}()

	if err := mountImage(cfg, fh); err != nil {
		logError("Failed to mount image", err, cfg)
	}

	args := os.Args[1:]
	if len(args) >= 1 {
		if err := handleRuntimeFlags(&args, cfg); err != nil {
			if err.Error() != "!no_return" {
				logError("Runtime flag handling failed", err, cfg)
			}
		}
	}

	if err := executeFile(args, cfg); err != nil {
		logError("Failed to execute file", err, cfg)
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
