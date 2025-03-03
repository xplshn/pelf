package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"crypto/sha256"
	"encoding/hex"

	"golang.org/x/sys/unix"

	"github.com/klauspost/compress/zstd"
	"github.com/pkg/xattr"
	"github.com/urfave/cli/v3"
)

const pelFVersion = "3.0"

//go:embed binaryDependencies.tar.zst
var binaryDependencies []byte

type Filesystem struct {
	Type       string
	Commands   []string
	CmdBuilder func(*Config) *exec.Cmd
}

var Filesystems = []Filesystem{
	{
		Type:     "squashfs",
		Commands: []string{"mksquashfs", "squashfuse", "fusermount"},
		CmdBuilder: func(config *Config) *exec.Cmd {
			args := []string{"mksquashfs", config.AppDir, config.ArchivePath}
			compressionArgs := strings.Split(config.CompressionArgs, " ")
			if len(compressionArgs) == 1 && compressionArgs[0] == "" {
				compressionArgs = strings.Split("-comp zstd -Xcompression-level 22", " ")
			}
			args = append(args, compressionArgs...)
			return exec.Command(args[0], args[1:]...)
		},
	},
	{
		Type:     "dwarfs",
		Commands: []string{"mkdwarfs", "dwarfs", "fusermount3"},
		CmdBuilder: func(config *Config) *exec.Cmd {
			compressionArgs := strings.Split(config.CompressionArgs, " ")
			args := []string{"mkdwarfs", "--input", config.AppDir, "--progress=ascii", "--set-owner", "0", "--set-group", "0", "--no-create-timestamp", "--no-history"}
			if len(compressionArgs) == 1 && compressionArgs[0] == "" {
				compressionArgs = strings.Split("-l7 --metadata-compression null", " ")
			}
			args = append(args, compressionArgs...)
			args = append(args, "--output", config.ArchivePath)
			return exec.Command(args[0], args[1:]...)
		},
	},
}

type BuildInfo struct {
	StaticToolsSize int64
	ArchiveSize     int64
}

type RuntimeInfo struct {
	AppBundleID    string
	PelfVersion    string
	HostInfo       string
	FilesystemType string
	Offsets        map[string]int64
}

type RuntimeConfig struct {
	AppBundleID    string            `json:"appBundleID"`
	PelfVersion    string            `json:"pelfVersion"`
	HostInfo       string            `json:"hostInfo"`
	Offsets        map[string]int64  `json:"offsets"`
	FilesystemType string            `json:"filesystemType"`
}

type Config struct {
	AppDir             string
	AppBundleID        string
	OutputFile         string
	CompressionArgs    string
	CustomEmbedDir     string
	FilesystemType     string
	ArchivePath        string
	Runtime            string
	EmbedStaticTools   bool
	UseUPX             bool
	RuntimeInfo        RuntimeConfig
	PreferToolsInPath  bool
	BinDepDir          string
}

// Modified lookPath function to handle custom PATH ordering
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

// Helper function to check if a file is executable
func isExecutableFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
	// Check if file is executable
	if runtime.GOOS != "windows" {
		if fi.Mode()&0111 == 0 {
			return fmt.Errorf("%s is not executable", path)
		}
	}
	return nil
}

// Function to extract binary dependencies and set up PATH
func setupBinaryDependencies(config *Config) (string, error) {
	// Create temporary directory for binary dependencies
	binDepDir, err := os.MkdirTemp("", "bindep_*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory for binary dependencies: %w", err)
	}

	// Save the bin dependencies directory to config for later cleanup
	config.BinDepDir = binDepDir

	// Create a reader for the embedded binary dependencies
	zr, err := zstd.NewReader(bytes.NewReader(binaryDependencies))
	if err != nil {
		return "", err
	}
	defer zr.Close()

	// Create tar reader
	tr := tar.NewReader(zr)

	// Extract the binary dependencies
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		// Handle directory
		if header.Typeflag == tar.TypeDir {
			dirPath := filepath.Join(binDepDir, header.Name)
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return "", err
			}
			continue
		}

		// Handle regular file
		filePath := filepath.Join(binDepDir, header.Name)
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}

		// Create the file
		outFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return "", err
		}

		// Copy the file content
		if _, err := io.Copy(outFile, tr); err != nil {
			outFile.Close()
			return "", err
		}
		outFile.Close()
	}

	// Get the current PATH
	currentPath := os.Getenv("PATH")

	// Update PATH according to preference
	var newPath string
	if config.PreferToolsInPath {
		newPath = currentPath + ":" + binDepDir
	} else {
		newPath = binDepDir + ":" + currentPath
	}

	return newPath, nil
}

// Function to calculate sha256 hash of a file
func calculateB3Sum(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

// Function to list all static tools with their B3SUMs
func listStaticTools(binDepDir string) error {
	files, err := os.ReadDir(binDepDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() {
			continue
		}

		filePath := filepath.Join(binDepDir, file.Name())
		hash, err := calculateB3Sum(filePath)
		if err != nil {
			return err
		}

		fmt.Printf("%s # %s\n", file.Name(), hash)
	}

	return nil
}

func main() {
	app := &cli.Command{
		Name:  "pelf",
		Usage: "Create self-contained AppDir executables",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:     "output-to",
				Aliases:  []string{"o"},
				Usage:    "Specify the output file name for the bundle",
			},
			&cli.StringFlag{
				Name:    "compression",
				Aliases: []string{"c"},
				Usage:   "Specify compression flags for mkdwarfs/mksquashfs",
			},
			&cli.StringFlag{
				Name:     "add-appdir",
				Aliases:  []string{"a"},
				Usage:    "Add an AppDir",
			},
			&cli.StringFlag{
				Name:     "appbundle-id",
				Aliases:  []string{"i"},
				Usage:    "Specify the ID of the AppBundle",
			},
			&cli.BoolFlag{
				Name:    "do-not-embed-static-tools",
				Aliases: []string{"t"},
				Usage:   "Do not embed static tools into the bundle",
			},
			&cli.StringFlag{
				Name:  "static-tools-dir",
				Usage: "Specify a custom directory from which to get the static tools",
			},
			&cli.StringFlag{
				Name:    "runtime",
				Usage:   "Specify which runtime shall be used",
				Sources: cli.EnvVars("PBUNDLE_RUNTIME", "_VAR_CUSTOM_RUNTIME"),
			},
			&cli.BoolFlag{
				Name:  "upx",
				Usage: "Enables usage of UPX compression in the static tools",
			},
			&cli.StringFlag{
				Name:    "filesystem",
				Aliases: []string{"j"},
				Usage:   "Specify the filesystem type: 'dwarfs' for DWARFS, 'squashfs' for SQUASHFS",
				Value:   "dwarfs", // Default to DWARFS
			},
			&cli.BoolFlag{
				Name:  "prefer-tools-in-path",
				Usage: "Prefer tools in PATH over embedded binary dependencies",
			},
			&cli.BoolFlag{
				Name:  "list-static-tools",
				Usage: "List all binary dependencies with their B3SUMs",
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			config := &Config{
				AppDir:            c.String("add-appdir"),
				AppBundleID:       c.String("appbundle-id"),
				OutputFile:        c.String("output-to"),
				CompressionArgs:   c.String("compression"),
				EmbedStaticTools:  !c.Bool("do-not-embed-static-tools"),
				CustomEmbedDir:    c.String("static-tools-dir"),
				Runtime:           c.String("runtime"),
				UseUPX:            c.Bool("upx"),
				FilesystemType:    c.String("filesystem"),
				PreferToolsInPath: c.Bool("prefer-tools-in-path"),
			}

			// Extract binary dependencies and set up PATH
			newPath, err := setupBinaryDependencies(config)
			if err != nil {
				return fmt.Errorf("failed to set up binary dependencies: %w", err)
			}
			defer os.RemoveAll(config.BinDepDir)

			// Handle list-static-tools flag
			if c.Bool("list-static-tools") {
				return listStaticTools(config.BinDepDir)
			}

			// Set the new PATH for the current process
			os.Setenv("PATH", newPath)

			if err := initRuntimeInfo(&config.RuntimeInfo, config.FilesystemType, config.AppBundleID); err != nil {
				return err
			}

			return run(config)
		},
	}

	err := app.Run(context.Background(), os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func initRuntimeInfo(runtimeInfo *RuntimeConfig, filesystemType, appBundleID string) error {
	uname := unix.Utsname{}
	if err := unix.Uname(&uname); err != nil {
		return err
	}

	hostInfo := fmt.Sprintf("%s %s %s %s",
		bytesToString(uname.Sysname[:]),
		bytesToString(uname.Release[:]),
		bytesToString(uname.Version[:]),
		bytesToString(uname.Machine[:]),
	)

	*runtimeInfo = RuntimeConfig{
		AppBundleID:    appBundleID,
		PelfVersion:    pelFVersion,
		HostInfo:       hostInfo,
		FilesystemType: filesystemType,
		Offsets:        make(map[string]int64),
	}

	return nil
}

func bytesToString(b []byte) string {
	n := 0
	for i, c := range b {
		if c == 0 {
			break
		}
		n = i + 1
	}
	return string(b[:n])
}

func run(config *Config) error {
	if err := checkAppDir(config.AppDir); err != nil {
		return err
	}

	fsType := config.FilesystemType
	var fs *Filesystem
	for i := range Filesystems {
		if Filesystems[i].Type == fsType {
			fs = &Filesystems[i]
			break
		}
	}
	if fs == nil {
		return fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

	// Use our custom lookPath instead of exec.LookPath
	for _, cmd := range fs.Commands {
		path, err := lookPath(cmd, os.Getenv("PATH"))
		if err != nil {
			return fmt.Errorf("command not found: %s", cmd)
		}
		fmt.Printf("Using %s: %s\n", cmd, path)
	}

	workDir, err := os.MkdirTemp("", "pelf_*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temporary directory: %w", err)
	}
	defer os.RemoveAll(workDir)

	config.ArchivePath = filepath.Join(workDir, "archive."+fsType)
	if err := createArchive(config, fs, config.ArchivePath); err != nil {
		return err
	}

	var staticToolsSize, archiveSize int64
	if config.EmbedStaticTools {
		if err := embedStaticTools(config, workDir, fs); err != nil {
			return err
		}
		staticToolsSize = getFileSize(filepath.Join(workDir, "static.tar.zst"))
	}

	archiveSize = getFileSize(config.ArchivePath)

	buildInfo := BuildInfo{
		StaticToolsSize: staticToolsSize,
		ArchiveSize:     archiveSize,
	}

	if err := createSelfExtractingArchive(config, workDir, buildInfo); err != nil {
		return err
	}

	return nil
}

func checkAppDir(appDir string) error {
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return fmt.Errorf("AppDir does not exist: %s", appDir)
	}
	return nil
}

func createArchive(config *Config, fs *Filesystem, archivePath string) error {
	cmd := fs.CmdBuilder(config)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to create archive: %s", string(out))
	}
	return nil
}

func embedStaticTools(config *Config, workDir string, fs *Filesystem) error {
	staticToolsDir := filepath.Join(workDir, "static", runtime.GOOS+"_"+runtime.GOARCH)
	if err := os.MkdirAll(staticToolsDir, 0755); err != nil {
		return fmt.Errorf("failed to create static tools directory: %w", err)
	}

	if err := copyTools(config.CustomEmbedDir, staticToolsDir, fs.Commands); err != nil {
		return err
	}

	if config.UseUPX {
		if err := compressWithUPX(staticToolsDir); err != nil {
			return err
		}
	}

	tarPath := filepath.Join(workDir, "static.tar.zst")
	if err := createTar(staticToolsDir, tarPath); err != nil {
		return err
	}

	return nil
}

func copyTools(customDir, destDir string, tools []string) error {
	for _, tool := range tools {
		var src string
		if customDir != "" {
			src = filepath.Join(customDir, tool)
		} else {
			// Use our custom lookPath instead of exec.LookPath
			path, err := lookPath(tool, os.Getenv("PATH"))
			if err != nil {
				return fmt.Errorf("command not found: %s", tool)
			}
			src = path
		}
		dest := filepath.Join(destDir, filepath.Base(src))
		if err := copyFile(src, dest); err != nil {
			return err
		}
	}
	return nil
}

func compressWithUPX(dir string) error {
	// Use our custom lookPath instead of exec.LookPath
	if _, err := lookPath("upx", os.Getenv("PATH")); err != nil {
		return fmt.Errorf("UPX not found")
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, file := range files {
		cmd := exec.Command("upx", filepath.Join(dir, file.Name()))
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("failed to compress with UPX: %s", string(out))
		}
	}
	return nil
}

func createTar(srcDir, tarPath string) error {
	file, err := os.Create(tarPath)
	if err != nil {
		return err
	}
	defer file.Close()

	zw, err := zstd.NewWriter(file)
	if err != nil {
		return err
	}
	defer zw.Close()

	tw := tar.NewWriter(zw)
	defer tw.Close()

	return filepath.Walk(srcDir, func(file string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(fi, file)
		if err != nil {
			return err
		}
		header.Name, err = filepath.Rel(filepath.Clean(srcDir), file)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !fi.IsDir() {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(tw, f)
			return err
		}
		return nil
	})
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}

func createSelfExtractingArchive(config *Config, workDir string, buildInfo BuildInfo) error {
	runtimePath := config.Runtime
	if runtimePath == "" {
		// Use appbundle-runtime from extracted binary dependencies if available
		runtimePath = filepath.Join(config.BinDepDir, "appbundle-runtime")
		if _, err := os.Stat(runtimePath); os.IsNotExist(err) {
			return fmt.Errorf("User did not provide --runtime flag and we apparently lack a default embedded runtime")
		}
	}

	if err := copyFile(runtimePath, config.OutputFile); err != nil {
		return err
	}

	out, err := os.OpenFile(config.OutputFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := fmt.Fprintf(out, "\n__APPBUNDLE_ID__: %s\n__PELF_VERSION__: %s\n__PELF_HOST__: %s\n__APPBUNDLE_FS__: %s\n",
		config.RuntimeInfo.AppBundleID, config.RuntimeInfo.PelfVersion, config.RuntimeInfo.HostInfo, config.RuntimeInfo.FilesystemType); err != nil {
		return err
	}

	var staticToolsOffset, staticToolsEndOffset, archiveOffset int64

	if config.EmbedStaticTools {
		tarFile, err := os.Open(filepath.Join(workDir, "static.tar.zst"))
		if err != nil {
			return err
		}
		defer tarFile.Close()

		out.WriteString("\n__STATIC_TOOLS__\n")
		staticToolsOffset, err = out.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, tarFile); err != nil {
			return err
		}
		staticToolsEndOffset, err = out.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		out.WriteString("\n__STATIC_TOOLS_EOF__\n")
	}

	fsFile, err := os.Open(filepath.Join(workDir, "archive."+config.FilesystemType))
	if err != nil {
		return err
	}
	defer fsFile.Close()

	out.WriteString("\n__ARCHIVE_MARKER__\n")
	archiveOffset, err = out.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, fsFile); err != nil {
		return err
	}

	config.RuntimeInfo.Offsets["staticToolsOffset"] = staticToolsOffset
	config.RuntimeInfo.Offsets["staticToolsEndOffset"] = staticToolsEndOffset
	config.RuntimeInfo.Offsets["archiveOffset"] = archiveOffset

	xattrData := fmt.Sprintf("%s\n%d\n%d\n%d\n%s\n%s\n%s\n",
		config.RuntimeInfo.FilesystemType,
		staticToolsOffset,
		staticToolsEndOffset,
		archiveOffset,
		config.RuntimeInfo.AppBundleID,
		config.RuntimeInfo.PelfVersion,
		config.RuntimeInfo.HostInfo)
	if err := xattr.FSet(out, "user.RuntimeConfig", []byte(xattrData)); err != nil {
		return fmt.Errorf("failed to set xattr: %w", err)
	}

	if err := os.Chmod(config.OutputFile, 0755); err != nil {
		return fmt.Errorf("failed to make output file executable: %w", err)
	}

	return nil
}

func getFileSize(filePath string) int64 {
	fi, _ := os.Stat(filePath)
	return fi.Size()
}
