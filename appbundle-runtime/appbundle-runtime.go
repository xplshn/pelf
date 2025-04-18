package main

import (
	"archive/tar"
	"bytes"
	"debug/elf"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/emmansun/base64"
	"github.com/fxamacker/cbor/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/pkg/xattr"
	"pgregory.net/rand"
)

const (
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"

	DWARFS_CACHESIZE     = "256M"
	DWARFS_BLOCKSIZE     = "512K"
	DWARFS_READAHEAD     = "32M"
	DWARFS_TIDY_STRATEGY = "tidy_strategy=time,tidy_interval=2s,tidy_max_age=10s,seq_detector=1"
)

var globalPath = os.Getenv("PATH")

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

type Filesystem struct {
	Type       string
	Commands   []string
	MountCmd   func(*RuntimeConfig) *exec.Cmd
	ExtractCmd func(*RuntimeConfig, string) *exec.Cmd
}

var Filesystems = []*Filesystem{
	{
		Type:     "squashfs",
		Commands: []string{"fusermount", "squashfuse", "unsquashfs"},
		MountCmd: func(cfg *RuntimeConfig) *exec.Cmd {
			executable, err := lookPath("squashfuse", globalPath)
			if err != nil {
				println(globalPath)
				logError("squashfuse not available", err, cfg)
			}
			args := []string{
				"-o", "ro,nodev,noatime",
				"-o", "uid=0,gid=0",
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				cfg.selfPath,
				cfg.mountDir,
			}
			if os.Getenv("ENABLE_FUSE_DEBUG") != "" {
				logWarning("squashfuse's debug mode implies foreground. The AppRun won't be called.")
				args = append(args, "-o", "debug")
			}
			return exec.Command(executable, args...)
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) *exec.Cmd {
			executable, err := lookPath("unsquashfs", globalPath)
			if err != nil {
				logError("unsquashfs not available", err, cfg)
			}
			args := []string{"-d", cfg.mountDir, "-o", fmt.Sprintf("%d", cfg.archiveOffset), cfg.selfPath}
			if query != "" {
				for file := range strings.SplitSeq(query, " ") {
					args = append(args, "-e", file)
				}
			}
			return exec.Command(executable, args...)
		},
	},
	{
		Type:     "dwarfs",
		Commands: []string{"fusermount3", "dwarfs", "dwarfsextract"},
		MountCmd: func(cfg *RuntimeConfig) *exec.Cmd {
			executable, err := lookPath("dwarfs", globalPath)
			if err != nil {
				logError("dwarfs not available", err, cfg)
			}
			return exec.Command(executable,
				"-o", "ro,nodev,noatime",
				"-o", "cache_files,no_cache_image,clone_fd",
				"-o", "block_allocator=mmap",
				"-o", getEnvWithDefault("DWARFS_TIDY_STRATEGY", DWARFS_TIDY_STRATEGY),
				"-o", "debuglevel="+T(os.Getenv("ENABLE_FUSE_DEBUG") != "", "debug", "error"),
				"-o", "cachesize="+getEnvWithDefault("DWARFS_CACHESIZE", DWARFS_CACHESIZE),
				"-o", "readahead="+getEnvWithDefault("DWARFS_READAHEAD", DWARFS_READAHEAD),
				"-o", "blocksize="+getEnvWithDefault("DWARFS_BLOCKSIZE", DWARFS_BLOCKSIZE),
				"-o", fmt.Sprintf("workers=%d", getEnvWithDefault("DWARFS_WORKERS", runtime.NumCPU())),
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				cfg.selfPath,
				cfg.mountDir,
			)
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) *exec.Cmd {
			executable, err := lookPath("dwarfsextract", globalPath)
			if err != nil {
				logError("dwarfsextract not available", err, cfg)
			}
			args := []string{
				"--input", cfg.selfPath,
				"--image-offset", fmt.Sprintf("%d", cfg.archiveOffset),
				"--output", cfg.mountDir,
			}
			if query != "" {
				for pattern := range strings.SplitSeq(query, " ") {
					args = append(args, "--pattern", pattern)
				}
			}
			return exec.Command(executable, args...)
		},
	},
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

		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

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
		if len(lines) >= 6 {
			cfg.appBundleFS = lines[0]
			cfg.archiveOffset = uint64(parseUint(lines[1]))
			cfg.exeName = lines[2]
			cfg.pelfVersion = lines[3]
			cfg.pelfHost = lines[4]
			cfg.hash = lines[5]
			cfg.disableRandomWorkDir = T(lines[6] == "1", true, false)
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
	if err := cbor.Unmarshal(runtimeInfoData, &runtimeInfo); err != nil {
		return fmt.Errorf("failed to parse .pbundle_runtime_info CBOR: %w", err)
	}

	cfg.appBundleFS = runtimeInfo["filesystemType"].(string)
	cfg.exeName = runtimeInfo["appBundleID"].(string)
	cfg.pelfVersion = runtimeInfo["pelfVersion"].(string)
	cfg.pelfHost = runtimeInfo["hostInfo"].(string)
	cfg.hash = runtimeInfo["hash"].(string)
	cfg.disableRandomWorkDir = runtimeInfo["disableRandomWorkDir"].(bool)
	cfg.archiveOffset = cfg.elfFileSize

	// Set mountOrExtract with default value 2 if not present in CBOR data
	cfg.mountOrExtract = uint8(2)
	if mountOrExtract, ok := runtimeInfo["mountOrExtract"].(uint8); ok {
		cfg.mountOrExtract = mountOrExtract
	}

	xattrData := fmt.Sprintf("%s\n%d\n%s\n%s\n%s\n%s\n%s\n",
		cfg.appBundleFS, cfg.archiveOffset, cfg.exeName, cfg.pelfVersion, cfg.pelfHost, cfg.hash, T(cfg.disableRandomWorkDir, "1", ""))
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
func getEnvWithDefault[T any](key string, defaultValue T) T {
	if value := os.Getenv(key); value != "" {
		opts := strings.Split(value, ",")
		if len(opts) > 0 {
			return any(opts[0]).(T)
		}
	}
	return defaultValue
}

func initConfig() (*RuntimeConfig, *fileHandler, error) {
	cfg := &RuntimeConfig{
		exeName:              "",
		poolDir:              filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath:             getSelfPath(),
		disableRandomWorkDir: T(os.Getenv("PBUNDLE_DISABLE_RANDOM_WORKDIR") == "1", true, false),
		noCleanup:            false,
		mountOrExtract:       2, // Default value for mountOrExtract
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
	workDir := os.Getenv(envKey)

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

func (f *fileHandler) extractStaticTools(cfg *RuntimeConfig) error {
	elfFile, err := elf.NewFile(f.file)
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	staticToolsSection := elfFile.Section(".pbundle_static_tools")
	if staticToolsSection == nil {
		return fmt.Errorf("static_tools section not found")
	}

	staticToolsData, err := staticToolsSection.Data()
	if err != nil {
		return fmt.Errorf("failed to read static_tools section: %w", err)
	}

	decoder, err := zstd.NewReader(bytes.NewReader(staticToolsData))
	if err != nil {
		return fmt.Errorf("zstd init: %w", err)
	}
	defer decoder.Close()

	sizeCache := make(map[string]int64)
	err = filepath.Walk(cfg.staticToolsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, err := filepath.Rel(cfg.staticToolsDir, path)
			if err != nil {
				return err
			}
			sizeCache[relPath] = info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to cache file sizes: %w", err)
	}

	tr := tar.NewReader(decoder)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		fpath := filepath.Join(cfg.staticToolsDir, hdr.Name)
		relPath, err := filepath.Rel(cfg.staticToolsDir, fpath)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		if _, exists := sizeCache[relPath]; exists {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", fpath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			f, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			if err := os.Symlink(hdr.Linkname, fpath); err != nil {
				return fmt.Errorf("symlink: %w", err)
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			if err := os.Link(hdr.Linkname, fpath); err != nil {
				return fmt.Errorf("hardlink: %w", err)
			}
		}
	}

	return nil
}

func checkDeps(cfg *RuntimeConfig, fh *fileHandler) (*Filesystem, error) {
	fs, ok := getFilesystem(cfg.appBundleFS)
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
	}

	updatePath("PATH", cfg.staticToolsDir)
	var missingCmd bool
	for _, cmd := range fs.Commands {
		if _, err := lookPath(cmd, globalPath); err != nil {
			missingCmd = true
			break
		}
	}

	if missingCmd {
		if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create static tools directory: %v", err)
		}

		if err := fh.extractStaticTools(cfg); err != nil {
			return nil, fmt.Errorf("failed to extract static tools: %v", err)
		}
	}

	return fs, nil
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

	os.Setenv(cfg.rExeName+"_libDir", libDirs)
	os.Setenv(cfg.rExeName+"_binDir", binDirs)
	os.Setenv(cfg.rExeName+"_mountDir", cfg.mountDir)

	updatePath("PATH", binDirs)

	os.Setenv("APPDIR", cfg.mountDir)
	os.Setenv("SELF", cfg.selfPath)
	os.Setenv("ARGV0", filepath.Base(os.Args[0]))

	// COMPAT
	os.Setenv("APPIMAGE", cfg.selfPath)

	executableFile, err := lookPath(cfg.entrypoint, globalPath)
	if err != nil {
		return fmt.Errorf("Unable to find the location of %s: %v", cfg.entrypoint, err)
	}

	cmd := exec.Command(executableFile, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func updatePath(envVar, dirs string) string {
	var newPath string
	if os.Getenv(envVar) == "" {
		newPath = dirs
	} else if os.Getenv(fmt.Sprintf("PBUNDLE_OVERTAKE_%s", envVar)) == "1" {
		newPath = dirs + ":" + os.Getenv(envVar)
	} else {
		newPath = os.Getenv(envVar) + ":" + dirs
	}

	os.Setenv(envVar, newPath)
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

func T[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
