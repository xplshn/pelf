package main

import (
	"archive/tar"
	"bufio"
	"bytes"
	"debug/elf"
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

	"github.com/emmansun/base64"
	"github.com/klauspost/compress/zstd"
	"github.com/pkg/xattr"
	"lukechampine.com/blake3"
	"pgregory.net/rand"
)

const (
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"

	DWARFS_CACHESIZE = "256M"
)

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
	updateInfo           string
	signature            string
	staticToolsOffset    uint
	archiveOffset        uint
	staticToolsEndOffset uint
	elfFileSize          uint
	doNotMount           bool
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
		Commands: []string{"squashfuse", "fusermount"},
		MountCmd: func(cfg *RuntimeConfig) *exec.Cmd {
			return exec.Command("squashfuse",
				"-o", "ro,nodev,noatime",
				"-o", "uid=0,gid=0",
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				cfg.selfPath,
				cfg.mountDir,
			)
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) *exec.Cmd {
			args := []string{"-d", cfg.mountDir, "-o", fmt.Sprintf("%d", cfg.archiveOffset), cfg.selfPath}
			if query != "" {
				for _, file := range strings.Split(query, " ") {
					args = append(args, "-e", file)
				}
			}
			return exec.Command("unsquashfs", args...)
		},
	},
	{
		Type:     "dwarfs",
		Commands: []string{"dwarfs", "fusermount3"},
		MountCmd: func(cfg *RuntimeConfig) *exec.Cmd {
			return exec.Command("dwarfs",
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				"-o", "ro,nodev,noatime,auto_unmount",
				"-o", "cache_files,no_cache_image,clone_fd",
				"-o", "cachesize="+getEnvWithDefault("DWARFS_CACHESIZE", DWARFS_CACHESIZE),
				"-o", "debuglevel=error",
				cfg.selfPath,
				cfg.mountDir,
			)
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) *exec.Cmd {
			if query != "" {
				logWarning(fmt.Sprintf("dwarfsextract cannot do a partial extraction. The following arguments will be ignored: %s", query))
			}
			return exec.Command("dwarfsextract",
				"-o", cfg.mountDir,
				"-O", fmt.Sprintf("%d", cfg.archiveOffset),
				cfg.selfPath,
			)
		},
	},
}

func mountImage(cfg *RuntimeConfig, fh *fileHandler) error {
	if cfg.doNotMount {
		return nil
	}
	if err := checkFuse(cfg, fh); err != nil {
		return err
	}

	logFile := filepath.Join(cfg.workDir, "."+cfg.appBundleFS+".log")

	if err := os.MkdirAll(cfg.mountDir, 0755); err != nil {
		return fmt.Errorf("failed to create mount directory %s: %v", cfg.mountDir, err)
	}

	if _, err := os.Stat(logFile); os.IsNotExist(err) {
		if err := os.WriteFile(filepath.Join(cfg.workDir, ".pid"), []byte(fmt.Sprintf("%d", os.Getpid())), 0644); err != nil {
			logError("Failed to write PID file", err, cfg)
		}

		fs, ok := getFilesystem(cfg.appBundleFS)
		if !ok {
			return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
		}

		cmd := fs.MountCmd(cfg)
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

func extractImage(cfg *RuntimeConfig, fh *fileHandler, query string) error {
	fs, ok := getFilesystem(cfg.appBundleFS)
	if !ok {
		return fmt.Errorf("unsupported filesystem for extraction: %s", cfg.appBundleFS)
	}

	if cfg.appBundleFS == "squashfs" {
		xattr.Remove(cfg.selfPath, "user.RuntimeConfig")
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
	elfFile, err := elf.NewFile(f.file)
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	for _, prog := range elfFile.Progs {
		end := uint(prog.Off + prog.Filesz)
		if end > cfg.elfFileSize {
			cfg.elfFileSize = end
		}
	}

	data, err := xattr.FGet(f.file, "user.RuntimeConfig")
	if err == nil {
		lines := strings.Split(string(data), "\n")

		cfg.appBundleFS = lines[0]

		staticToolsOffset, err := strconv.ParseUint(lines[1], 10, 64)
		if err != nil {
			return fmt.Errorf("failed to parse staticToolsOffset: %w", err)
		}
		cfg.staticToolsOffset = uint(staticToolsOffset)

		staticToolsEndOffset, err := strconv.ParseUint(lines[2], 10, 64)
		if err != nil {
			return fmt.Errorf("failed to parse staticToolsEndOffset: %w", err)
		}
		cfg.staticToolsEndOffset = uint(staticToolsEndOffset)

		archiveOffset, err := strconv.ParseUint(lines[3], 10, 64)
		if err != nil {
			return fmt.Errorf("failed to parse archiveOffset: %w", err)
		}

		cfg.archiveOffset = uint(archiveOffset)
		cfg.exeName = lines[4]
		cfg.pelfVersion = lines[5]
		cfg.pelfHost = lines[6]
		cfg.disableRandomWorkDir = T(lines[7] == "__APPBUNDLE_OPTS__: disableRandomWorkDir", true, false)

		if cfg.exeName == "" || cfg.pelfVersion == "" || cfg.pelfHost == "" || cfg.appBundleFS == "" {
			return fmt.Errorf("xattr cache is corrupt: missing runtime info: exeName=%q, pelfVersion=%q, pelfHost=%q, appBundleFS=%q",
				cfg.exeName, cfg.pelfVersion, cfg.pelfHost, cfg.appBundleFS)
		}
		return nil
	}

	if _, err := f.file.Seek(int64(cfg.elfFileSize), io.SeekStart); err != nil {
		return fmt.Errorf("seek to ELF end: %w", err)
	}

	reader := bufio.NewReader(f.file)
	var (
		staticToolsFound, staticToolsEndFound, archiveMarkerFound          bool
		exeName, pelfVersion, pelfHost, appBundleFS, updateInfo, signature string
	)
	currentOffset := cfg.elfFileSize

	for {
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return fmt.Errorf("read line: %w", err)
		}
		if err == io.EOF {
			break
		}

		lineLen := uint(len(line))
		trimmedLine := strings.TrimSpace(line)

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

		if strings.HasPrefix(trimmedLine, "__APPBUNDLE_ID__: ") {
			exeName = strings.TrimSpace(trimmedLine[len("__APPBUNDLE_ID__: "):])
		} else if strings.HasPrefix(trimmedLine, "__PELF_VERSION__: ") {
			pelfVersion = strings.TrimSpace(trimmedLine[len("__PELF_VERSION__: "):])
		} else if strings.HasPrefix(trimmedLine, "__PELF_HOST__: ") {
			pelfHost = strings.TrimSpace(trimmedLine[len("__PELF_HOST__: "):])
		} else if strings.HasPrefix(trimmedLine, "__APPBUNDLE_FS__: ") {
			appBundleFS = strings.TrimSpace(trimmedLine[len("__APPBUNDLE_FS__: "):])
		} else if strings.HasPrefix(trimmedLine, "__UPD_INFO__: ") {
			updateInfo = strings.TrimSpace(trimmedLine[len("__UPD_INFO__: "):])
		} else if strings.HasPrefix(trimmedLine, "__SHA256_SIG__: ") {
			signature = strings.TrimSpace(trimmedLine[len("__SHA256_SIG__: "):])
		} else if strings.HasPrefix(trimmedLine, "__APPBUNDLE_OPTS__: disableRandomWorkDir") {
			cfg.disableRandomWorkDir = true
		}

		currentOffset += lineLen
	}

	cfg.exeName = exeName
	cfg.pelfVersion = pelfVersion
	cfg.pelfHost = pelfHost
	cfg.appBundleFS = appBundleFS
	cfg.updateInfo = updateInfo
	cfg.signature = signature

	if !archiveMarkerFound || !staticToolsFound || !staticToolsEndFound {
		return fmt.Errorf("markers not found: archiveOffset=%d, staticToolsOffset=%d, staticToolsEndOffset=%d", cfg.archiveOffset, cfg.staticToolsOffset, cfg.staticToolsEndOffset)
	}

	xattrData := fmt.Sprintf("%s\n%d\n%d\n%d\n%s\n%s\n%s\n",
		cfg.appBundleFS, cfg.staticToolsOffset, cfg.staticToolsEndOffset, cfg.archiveOffset, cfg.exeName, cfg.pelfVersion, cfg.pelfHost)
	if err := xattr.FSet(f.file, "user.RuntimeConfig", []byte(xattrData)); err != nil {
		return fmt.Errorf("failed to set xattr: %w", err)
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
		exeName:              os.Getenv("EXE_NAME"),
		poolDir:              filepath.Join(os.TempDir(), ".pelfbundles"),
		selfPath:             getSelfPath(),
		doNotMount:           T(os.Getenv("APPIMAGE_EXTRACT_AND_RUN") == "1" || os.Getenv("PBUNDLE_EXTRACT_AND_RUN") == "1", true, false),
		noCleanup:            T(os.Getenv("PBUNDLE_NO_CLEANUP") == "1", true, false),
		disableRandomWorkDir: T(os.Getenv("PBUNDLE_DISABLE_RANDOM_WORKDIR") == "__APPBUNDLE_OPTS__: disableRandomWorkDir", true, false),
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

	if cfg.workDir == "" {
		cfg.workDir = getWorkDir(cfg, fh)
	}

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

func getWorkDir(cfg *RuntimeConfig, fh *fileHandler) string {
	envKey := cfg.rExeName + "_workDir"
	workDir := os.Getenv(envKey)

	if cfg.disableRandomWorkDir {
		fileInfo, err := os.Stat(cfg.selfPath)
		if err != nil {
			logError("Failed to get file info", err, cfg)
		}
		offset := fileInfo.Size() - 256
		buffer := make([]byte, 256)
		if _, err := fh.file.ReadAt(buffer, offset); err != nil {
			logError("Failed to read last 256 bytes of file", err, cfg)
		}
		hash := blake3.New(8, nil)
		hash.Write(buffer)
		hashSum := hash.Sum(nil)
		hashStr := fmt.Sprintf("%x", hashSum)
		workDir = filepath.Join(cfg.poolDir, "pbundle_"+cfg.rExeName+"_"+hashStr)
		cfg.noCleanup = true
	} else {
		workDir = filepath.Join(cfg.poolDir, "pbundle_"+cfg.rExeName+"_"+generateRandomString(8))
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
	fs, ok := getFilesystem(cfg.appBundleFS)
	if !ok {
		return fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
	}

	var missingCmd bool
	for _, cmd := range fs.Commands {
		if _, err := exec.LookPath(cmd); err != nil {
			missingCmd = true
			break
		}
	}

	if missingCmd {
		cfg.staticToolsDir = filepath.Join(cfg.workDir, "static")
		if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
			return fmt.Errorf("failed to create static tools directory: %v", err)
		}

		if err := fh.extractStaticTools(cfg); err != nil {
			return fmt.Errorf("failed to extract static tools: %v", err)
		}

		updatePath("PATH", cfg.staticToolsDir)
	}

	return nil
}

func (f *fileHandler) extractStaticTools(cfg *RuntimeConfig) error {
	if _, err := f.file.Seek(int64(cfg.staticToolsOffset), io.SeekStart); err != nil {
		return fmt.Errorf("failed to seek to static tools: %w", err)
	}

	staticToolsLength := cfg.staticToolsEndOffset - cfg.staticToolsOffset
	staticToolsData := make([]byte, staticToolsLength)
	if _, err := io.ReadFull(f.file, staticToolsData); err != nil {
		return fmt.Errorf("failed to read static tools section: %w", err)
	}

	decoder, err := zstd.NewReader(bytes.NewReader(staticToolsData))
	if err != nil {
		return fmt.Errorf("zstd init: %w", err)
	}
	defer decoder.Close()

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
		}
	}

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
	if os.Getenv("PELF_LD_VAR") == "1" {
		updatePath("LD_LIBRARY_PATH", binDirs)
	}

	os.Setenv("SELF_TEMPDIR", cfg.mountDir)
	os.Setenv("SELF", cfg.selfPath)
	os.Setenv("ARGV0", filepath.Base(os.Args[0]))

	executableFile, err := lookPath(cfg.entrypoint, os.Getenv("PATH"))
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
	return newPath
}

func cleanup(cfg *RuntimeConfig) {
	if cfg.noCleanup {
		return
	}
	cmd := exec.Command(os.Args[0], "--pbundle_internal_Cleanup", cfg.mountDir, cfg.poolDir, cfg.workDir, T(cfg.doNotMount == true, "true", ""))
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
			for i := 0; i < 5; i++ {
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
	for _, dir := range strings.Split(pathenv, ":") {
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

func T[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}

func mountOrExtract(cfg *RuntimeConfig, fh *fileHandler) {
	if cfg.doNotMount {
		if err := extractImage(cfg, fh, ""); err != nil {
			logError("Failed to mount image", err, cfg)
		}
	}
	if err := mountImage(cfg, fh); err != nil {
		logError("Failed to mount image", err, cfg)
	}
}
