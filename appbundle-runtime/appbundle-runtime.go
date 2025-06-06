package main

import (
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"runtime"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shamaton/msgpack/v2" //"github.com/fxamacker/cbor/v2"
	"github.com/pkg/xattr"
	"github.com/joho/godotenv"

	"github.com/emmansun/base64"
	"pgregory.net/rand"
)

const (
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"

	DWARFS_CACHESIZE       = "256M"
	DWARFS_BLOCKSIZE       = "512K"
	DWARFS_READAHEAD       = "32M"
	DWARFS_BLOCK_ALLOCATOR = "mmap"
	DWARFS_TIDY_STRATEGY   = "tidy_strategy=time,tidy_interval=4s,tidy_max_age=10s,seq_detector=1"
)

var globalEnv = os.Environ()
var globalPath = getEnv(globalEnv, "PATH")

type RuntimeConfig struct {
	poolDir              string
	workDir              string
	rExeName             string
	mountDir             string
	entrypoint           string
	selfPath             string
	staticToolsDir       string
	exeName              string
	pelfHost             string
	pelfVersion          string
	appBundleFS          string
	hash                 string
	elfFileSize          uint64
	archiveOffset        uint64
	mountOrExtract       uint8
	noCleanup            bool
	disableRandomWorkDir bool
}

type fileHandler struct {
	path string
	file *os.File
}

// CommandRunner interface unifies both os/exec.Cmd and embedexe/exec.Cmd
type CommandRunner interface {
    Run() error
    SetStdout(io.Writer)
    SetStderr(io.Writer)
    SetStdin(io.Reader)
    CombinedOutput() ([]byte, error)
}

// CommandCreator defines the function type for creating commands
type CommandCreator func(*RuntimeConfig) CommandRunner
type ExtractCommandCreator func(*RuntimeConfig, string) CommandRunner

type Filesystem struct {
    Type       string
    Commands   []string
    MountCmd   CommandCreator
    ExtractCmd ExtractCommandCreator
}

func mountImage(cfg *RuntimeConfig, fh *fileHandler, fs *Filesystem) error {
    pidFile := filepath.Join(cfg.workDir, ".pid")

    if _, err := os.Stat(pidFile); os.IsNotExist(err) {
        if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
            return fmt.Errorf("failed to create mount directory %s: %v", cfg.mountDir, err)
        }

        if err := os.WriteFile(pidFile, fmt.Appendf(nil, "%d", os.Getpid()), 0644); err != nil {
            logError("Failed to write PID file", err, cfg)
        }

        cmd := fs.MountCmd(cfg)
        cmd.SetStdout(os.Stdout)
        cmd.SetStderr(os.Stderr)

        if err := cmd.Run(); err != nil {
            logWarning(fmt.Sprintf("Failed to mount %s archive: %v", cfg.appBundleFS, err))
            return err
        }
    } else {
        if cfg.noCleanup {
            if _, err := os.Stat(filepath.Join(cfg.mountDir, "AppRun")); os.IsNotExist(err) {
                os.Remove(pidFile)
                logError(".pid file present in workdir, but AppRun is not", err, cfg)
            }
        }
    }
    return nil
}

func extractImage(cfg *RuntimeConfig, fh *fileHandler, fs *Filesystem, query string) error {
	if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory %s: %v", cfg.mountDir, err)
	}
	cmd := fs.ExtractCmd(cfg, query)
	output, err := cmd.CombinedOutput()
	if err != nil {
		logWarning(fmt.Sprintf("Failed to extract %s archive: %v", cfg.appBundleFS, err))
		logWarning(string(output))
		return err
	}

	return nil
}

func getFilesystem(fsType string) (*Filesystem, bool) {
	for _, fs := range Filesystems {
		if fs.Type == fsType {
			return fs, true
		}
	}
	return nil, false
}

func newFileHandler(path string) (*fileHandler, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	return &fileHandler{path: path, file: file}, nil
}

func (f *fileHandler) readPlaceholdersAndMarkers(cfg *RuntimeConfig) error {
	data, err := xattr.FGet(f.file, "user.RuntimeConfig")
	if err == nil {
		lines := strings.Split(string(data), "\n")
		if len(lines) >= 8 {
			cfg.appBundleFS = lines[0]
			cfg.archiveOffset = uint64(parseUint(lines[1]))
			cfg.exeName = lines[2]
			cfg.pelfVersion = lines[3]
			cfg.pelfHost = lines[4]
			cfg.hash = lines[5]
			cfg.disableRandomWorkDir = T(lines[6] == "1", true, false)
			if n, err := strconv.ParseUint(lines[7], 10, 8); err == nil {
				cfg.mountOrExtract = uint8(n)
			}
			return nil
		}
	}

	elfFile, err := elf.NewFile(f.file)
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	cfg.elfFileSize, err = calculateElfSize(elfFile, f.file)
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	runtimeInfoSection := elfFile.Section(".pbundle_runtime_info")
	if runtimeInfoSection == nil {
		return fmt.Errorf(".pbundle_runtime_info section not found")
	}

	runtimeInfoData, err := runtimeInfoSection.Data()
	if err != nil {
		return fmt.Errorf("failed to read .pbundle_runtime_info section: %w", err)
	}

	var runtimeInfo map[string]any
	if err := msgpack.Unmarshal(runtimeInfoData, &runtimeInfo); err != nil {
		return fmt.Errorf("failed to parse .pbundle_runtime_info MessagePack: %w", err)
	}

	cfg.appBundleFS = runtimeInfo["FilesystemType"].(string)
	cfg.exeName = runtimeInfo["AppBundleID"].(string)
	cfg.pelfVersion = runtimeInfo["PelfVersion"].(string)
	cfg.pelfHost = runtimeInfo["HostInfo"].(string)
	cfg.hash = runtimeInfo["Hash"].(string)
	cfg.mountOrExtract = runtimeInfo["MountOrExtract"].(uint8) // cfg.mountOrExtract = uint8(runtimeInfo["MountOrExtract"].(uint64))
	cfg.disableRandomWorkDir = runtimeInfo["DisableRandomWorkDir"].(bool)
	cfg.archiveOffset = cfg.elfFileSize

	xattrData := fmt.Sprintf("%s\n%d\n%s\n%s\n%s\n%s\n%s\n%d\n",
		cfg.appBundleFS, cfg.archiveOffset, cfg.exeName, cfg.pelfVersion, cfg.pelfHost, cfg.hash, T(cfg.disableRandomWorkDir, "1", ""), cfg.mountOrExtract)
	if err := xattr.FSet(f.file, "user.RuntimeConfig", []byte(xattrData)); err != nil {
		return fmt.Errorf("failed to set xattr: %w", err)
	}

	return nil
}

func parseUint(s string) uint64 {
	val, _ := strconv.ParseUint(s, 10, 64)
	return val
}

func calculateElfSize(elfFile *elf.File, file *os.File) (len uint64, err error) {
	sr := io.NewSectionReader(file, 0, 1<<63-1)
	var shoff, shentsize, shnum uint64
	switch elfFile.Class.String() {
	case "ELFCLASS64":
		hdr := new(elf.Header64)
		_, err = sr.Seek(0, 0)
		if err != nil {
			return
		}
		err = binary.Read(sr, elfFile.ByteOrder, hdr)
		if err != nil {
			return
		}
		shoff = uint64(hdr.Shoff)
		shnum = uint64(hdr.Shnum)
		shentsize = uint64(hdr.Shentsize)
	case "ELFCLASS32":
		hdr := new(elf.Header32)
		_, err = sr.Seek(0, 0)
		if err != nil {
			return
		}
		err = binary.Read(sr, elfFile.ByteOrder, hdr)
		if err != nil {
			return
		}
		shoff = uint64(hdr.Shoff)
		shnum = uint64(hdr.Shnum)
		shentsize = uint64(hdr.Shentsize)
	default:
		err = fmt.Errorf("unsupported elf architecture\n")
		return
	}
	len = shoff + (shentsize * shnum)
	return
}

// getEnvWithDefault is a generic function that retrieves an environment variable
// and returns the first value if it exists, otherwise it returns the default value.
func getEnvWithDefault[T any](env []string, key string, defaultValue T) T {
	for _, e := range env {
		pair := strings.SplitN(e, "=", 2)
		if pair[0] == key {
			opts := strings.Split(pair[1], ",")
			if len(opts) > 0 {
				return any(opts[0]).(T)
			}
		}
	}
	return defaultValue
}

func getEnv(env []string, key string) string {
	for _, e := range env {
		pair := strings.SplitN(e, "=", 2)
		if pair[0] == key {
			return pair[1]
		}
	}
	return ""
}

func setEnv(env *[]string, key, value string) {
	for i, e := range *env {
		pair := strings.SplitN(e, "=", 2)
		if pair[0] == key {
			(*env)[i] = key + "=" + value
			return
		}
	}
	*env = append(*env, key+"="+value)
}

func initConfig() (*RuntimeConfig, *fileHandler, error) {
	cfg := &RuntimeConfig{
		exeName:              "",
		poolDir:              filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath:             getSelfPath(),
		disableRandomWorkDir: T(getEnv(globalEnv, "PBUNDLE_DISABLE_RANDOM_WORKDIR") == "1", true, false),
		noCleanup:            false,
		mountOrExtract:       2,
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

	cfg.workDir = getWorkDir(cfg, fh)
	cfg.mountDir = filepath.Join(cfg.workDir, "mounted")
	cfg.entrypoint = filepath.Join(cfg.mountDir, "AppRun")
	cfg.staticToolsDir = filepath.Join(cfg.poolDir, ".static")

	if err := os.MkdirAll(cfg.workDir, 0755); err != nil {
		logError("Failed to create work directory", err, cfg)
	}

	return cfg, fh, nil
}

func getSelfPath() string {
	path, _ := os.Executable()
	path, _ = filepath.EvalSymlinks(path)
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

func getWorkDir(cfg *RuntimeConfig, fh *fileHandler) string {
	envKey := cfg.rExeName + "_workDir"
	workDir := getEnv(globalEnv, envKey)

	if workDir == "" {
		if cfg.disableRandomWorkDir {
			workDir = filepath.Join(cfg.poolDir, "pbundle_"+cfg.rExeName+"_"+cfg.hash[:8])
			cfg.noCleanup = true
		} else {
			workDir = filepath.Join(cfg.poolDir, "pbundle_"+cfg.rExeName+"_"+generateRandomString(8))
		}
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

func setSelfEnvs(cfg *RuntimeConfig) error {

	setEnvIfExists := func(dir, envVar, oldEnvVar string) error {
		if _, err := os.Stat(dir); err == nil {
			oldValue := getEnv(globalEnv, oldEnvVar)
			if oldValue == "" {
				oldValue = getEnv(globalEnv, envVar)
				setEnv(&globalEnv, oldEnvVar, oldValue)
			}
			setEnv(&globalEnv, envVar, dir)
		}
		return nil
	}

	setEnvIfExists(hiddenPath(cfg.selfPath, ".home"), "HOME", "OLD_HOME")
	setEnvIfExists(hiddenPath(cfg.selfPath, ".share"), "XDG_DATA_HOME", "OLD_XDG_DATA_HOME")
	setEnvIfExists(hiddenPath(cfg.selfPath, ".config"), "XDG_CONFIG_HOME", "OLD_XDG_CONFIG_HOME")

	envFile := hiddenPath(cfg.selfPath, ".env")
	if _, err := os.Stat(envFile); err == nil {
		if envs, err := godotenv.Read(envFile); err == nil {
			for key, value := range envs {
				globalEnv = append(globalEnv, fmt.Sprintf("%s=%s", key, value))
			}
		} else {
			return fmt.Errorf("failed to load .env file: %w", err)
		}
	}

	return nil
}

func executeFile(args []string, cfg *RuntimeConfig) error {
	binDirs := cfg.mountDir + "/bin:" +
		cfg.mountDir + "/usr/bin:" +
		cfg.mountDir + "/shared/bin"

	libDirs := cfg.mountDir + "/lib:" +
		cfg.mountDir + "/usr/lib:" +
		cfg.mountDir + "/shared/lib:" +
		cfg.mountDir + "/lib64:" +
		cfg.mountDir + "/usr/lib64:" +
		cfg.mountDir + "/lib32:" +
		cfg.mountDir + "/usr/lib32:" +
		cfg.mountDir + "/libx32:" +
		cfg.mountDir + "/usr/libx32"

	setEnv(&globalEnv, cfg.rExeName+"_libDir", libDirs)
	setEnv(&globalEnv, cfg.rExeName+"_binDir", binDirs)
	setEnv(&globalEnv, cfg.rExeName+"_mountDir", cfg.mountDir)

	updatePath("PATH", binDirs)

	setEnv(&globalEnv, "APPDIR", cfg.mountDir)
	setEnv(&globalEnv, "SELF", cfg.selfPath)
	setEnv(&globalEnv, "ARGV0", filepath.Base(os.Args[0]))

	// COMPAT
	setEnv(&globalEnv, "APPIMAGE", cfg.selfPath)

	executableFile, err := lookPath(cfg.entrypoint, globalPath)
	if err != nil {
		return fmt.Errorf("Unable to find the location of %s: %v", cfg.entrypoint, err)
	}

	setSelfEnvs(cfg)

	cmd := exec.Command(executableFile, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = globalEnv
	return cmd.Run()
}

func updatePath(envVar, dirs string) string {
	var newPath string
	if getEnv(globalEnv, envVar) == "" {
		newPath = dirs
	} else if getEnv(globalEnv, fmt.Sprintf("PBUNDLE_OVERTAKE_%s", envVar)) == "1" {
		newPath = dirs + ":" + getEnv(globalEnv, envVar)
	} else {
		newPath = getEnv(globalEnv, envVar) + ":" + dirs
	}

	setEnv(&globalEnv, envVar, newPath)
	globalPath = newPath
	return newPath
}

func cleanup(cfg *RuntimeConfig) {
	if cfg.noCleanup {
		return
	}
	cmd := exec.Command(os.Args[0], "--pbundle_internal_Cleanup", cfg.mountDir, cfg.poolDir, cfg.workDir, T(cfg.mountOrExtract == 1, "true", ""))
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.Env = globalEnv
	if err := cmd.Start(); err != nil {
		panic(err)
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
	matches, err := filepath.Glob(dir + "/" + pattern)
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
		doNotMount := os.Args[5]

		if doNotMount != "true" {
			for range 5 {
				if !isMounted(mountDir) {
					break
				}
				cmd := exec.Command("fusermount3", "-u", mountDir)
				cmd.Env = globalEnv
				cmd.Run()
				sleep(1)
			}
			if isMounted(mountDir) {
				exec.Command("fusermount3", "-uz", mountDir).Run()
			}
		}

		os.RemoveAll(workDir)
		if isDirEmpty(poolDir) {
			os.RemoveAll(poolDir)
		}

		os.Exit(0)
	}

	cfg, fh, err := initConfig()
	if err != nil {
		logError("Failed to initialize config", err, cfg)
	}

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	die := func() {
		if fh != nil {
			fh.file.Close()
		}
		cleanup(cfg)
		os.Exit(0)
	}

	defer func() {
		if r := recover(); r != nil {
			logError("Panic recovered", fmt.Errorf("%v", r), cfg)
		}
		die()
	}()

	go func() {
		<-sigChan
		die()
	}()

	args := os.Args[1:]
	if len(args) > 0 {
		if err := handleRuntimeFlags(fh, &args, cfg); err != nil {
			if err.Error() != "!no_return" {
				logError("Runtime flag handling failed", err, cfg)
			} else {
				cleanup(cfg)
			}
		}
	} else {
		mountOrExtract(cfg, fh)
		_ = executeFile(args, cfg)
		cleanup(cfg)
	}
}

func generateRandomString(length int) string {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		panic(err)
	}
	return hex.EncodeToString(bytes)
}

func lookPath(file string, pathenv string) (string, error) {
	errNotFound := fmt.Errorf("executable file not found in $PATH")
	if strings.Contains(file, "/") {
		err := isExecutableFile(file)
		if err == nil {
			return file, nil
		}
		return "", err
	}
	if pathenv == "" {
		return "", errNotFound
	}
	for dir := range strings.SplitSeq(pathenv, ":") {
		if dir == "" {
			dir = "."
		}
		path := dir + "/" + file
		if err := isExecutableFile(path); err == nil {
			return path, nil
		}
	}
	return "", errNotFound
}

func isExecutableFile(file string) error {
	d, err := os.Stat(file)
	if err != nil {
		return err
	}
	if m := d.Mode(); !m.IsDir() && m&0111 != 0 {
		return nil
	}
	return os.ErrPermission
}

func mountOrExtract(cfg *RuntimeConfig, fh *fileHandler) {
	fs, err := checkDeps(cfg, fh)
	if err != nil {
		logError("Unexpected failure when checking the availability of the AppBundle's dependencies", err, cfg)
	}

	switch cfg.mountOrExtract {
	case 0:
		// FUSE mounting only
		if err := mountImage(cfg, fh, fs); err != nil {
			logError("Failed to mount image", err, cfg)
		}
	case 1:
		// Do not use FUSE mounting, but extract and run
		if err := extractImage(cfg, fh, fs, ""); err != nil {
			logError("Failed to extract image", err, cfg)
		}
	case 2:
		// Try to use FUSE mounting and if it is unavailable extract and run
		if err := mountImage(cfg, fh, fs); err != nil {
			logWarning("FUSE mounting failed, falling back to extraction")
			if err := extractImage(cfg, fh, fs, ""); err != nil {
				logError("Failed to extract image", err, cfg)
			}
		}
	case 3:
		// As above, but if the image size is less than 350 MB (default)
		const defaultSizeLimit = 350 * 1024 * 1024
		if cfg.elfFileSize < defaultSizeLimit {
			if err := mountImage(cfg, fh, fs); err != nil {
				logWarning("FUSE mounting failed, falling back to extraction")
				if err := extractImage(cfg, fh, fs, ""); err != nil {
					logError("Failed to extract image", err, cfg)
				}
			}
		} else {
			if err := extractImage(cfg, fh, fs, ""); err != nil {
				logError("Failed to extract image", err, cfg)
			}
		}
	default:
		logError("Invalid value for mountOrExtract", nil, cfg)
	}
}

// --- General purpose utility functions ---
func T[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
//func hiddenPath(base string, suffix string) string { return filepath.Join(filepath.Dir(base), "."+filepath.Base(base)+suffix) }
func hiddenPath(base string, suffix string) string { return base+suffix }

// --- DWARFS ---
func getDwarfsCacheSize() string {
    if cacheSize := getEnv(globalEnv, "DWARFS_CACHESIZE"); cacheSize != "" { return cacheSize }

    memStats, err := mem.VirtualMemory()
    if err != nil { return DWARFS_CACHESIZE }

	availableMemory := memStats.Available
    if availableMemory == 0 { availableMemory = memStats.Free }

    availableMemoryMB := float64(availableMemory) / 1024.0 / 1024.0 / 1.3
    cacheSizesMB := []uint32{64, 128, 256, 384, 512, 640, 768, 896, 1024, 1536}

    for i := len(cacheSizesMB) - 1; i >= 0; i-- {
        if availableMemoryMB >= float64(cacheSizesMB[i]) { return fmt.Sprintf("%dM", cacheSizesMB[i]) }
    }
    return "32M"
}

func getDwarfsWorkers(cachesize *string) string {
    workers := getEnv(globalEnv, "DWARFS_WORKERS")
    if workers != "" {
        return workers
    }
    switch *cachesize {
    case "1536M", "1024M":
        return strconv.Itoa(runtime.NumCPU())
    case "896M":
        return "2"
    default:
        return "1"
    }
}
