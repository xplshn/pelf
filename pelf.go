package main

import (
	"archive/tar"
	"bytes"
	"context"
	_ "embed"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/klauspost/compress/zstd"
	"github.com/urfave/cli/v3"
	"github.com/zeebo/blake3"
	"github.com/pkg/xattr"
)

const pelFVersion = "3.0"

// Global variable to store the PATH environment variable
var globalPath = os.Getenv("PATH")

//go:embed binaryDependencies.tar.zst
var binaryDependencies []byte

type Filesystem struct {
	Type       map[string]string
	Commands   []string
	CmdBuilder func(*Config) *exec.Cmd
}

var Filesystems = []Filesystem{
	{
		Type:     map[string]string{"squashfs": "sqfs"},
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
		Type:     map[string]string{"dwarfs": "dwfs"},
		Commands: []string{"dwarfs", "mkdwarfs", "fusermount3"},
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
	AppBundleID    string           `json:"appBundleID"`
	PelfVersion    string           `json:"pelfVersion"`
	HostInfo       string           `json:"hostInfo"`
	Offsets        map[string]int64 `json:"offsets"`
	FilesystemType string           `json:"filesystemType"`
	Hash           string           `json:"hash"`
}

type Config struct {
	AppDir                string
	AppBundleID           string
	OutputFile            string
	CompressionArgs       string
	CustomEmbedDir        string
	FilesystemType        string
	ArchivePath           string
	Runtime               string
	DoNotEmbedStaticTools bool
	UseUPX                bool
	RuntimeInfo           RuntimeConfig
	PreferToolsInPath     bool
	BinDepDir             string
	DisableRandomWorkDir  bool
}

// Modified lookPath function to handle custom PATH ordering
func lookPath(file string) (string, error) {
	errNotFound := fmt.Errorf("executable file not found in $PATH")
	if strings.Contains(file, "/") {
		err := isExecutableFile(file)
		if err == nil {
			return file, nil
		}
		return "", err
	}
	if globalPath == "" {
		return "", errNotFound
	}
	for _, dir := range strings.Split(globalPath, ":") {
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
	if fi.Mode()&0111 == 0 {
		return fmt.Errorf("%s is not executable", path)
	}
	return nil
}

func setupBinaryDependencies(config *Config) (string, error) {
	binDepDir, err := os.MkdirTemp("", "bindep_*.tmp")
	if err != nil {
		return "", fmt.Errorf("failed to create temporary directory for binary dependencies: %w", err)
	}

	config.BinDepDir = binDepDir

	zr, err := zstd.NewReader(bytes.NewReader(binaryDependencies))
	if err != nil {
		return "", err
	}
	defer zr.Close()

	tr := tar.NewReader(zr)

	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}

		if header.Typeflag == tar.TypeDir {
			dirPath := filepath.Join(binDepDir, header.Name)
			if err := os.MkdirAll(dirPath, 0755); err != nil {
				return "", err
			}
			continue
		}

		if header.Typeflag == tar.TypeSymlink {
			target := header.Linkname
			symlinkPath := filepath.Join(binDepDir, header.Name)
			dir := filepath.Dir(symlinkPath)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", err
			}

			if _, err := os.Lstat(symlinkPath); err == nil {
				os.Remove(symlinkPath)
			}

			if err := os.Symlink(target, symlinkPath); err != nil {
				return "", fmt.Errorf("failed to create symlink %s -> %s: %w", symlinkPath, target, err)
			}
			continue
		}

		filePath := filepath.Join(binDepDir, header.Name)
		dir := filepath.Dir(filePath)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return "", err
		}

		outFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return "", err
		}

		if _, err := io.Copy(outFile, tr); err != nil {
			outFile.Close()
			return "", err
		}
		outFile.Close()
	}

	var newPath string
	if config.PreferToolsInPath {
		newPath = globalPath + ":" + binDepDir
	} else {
		newPath = binDepDir + ":" + globalPath
	}

	return newPath, nil
}

// Function to calculate b3sum hash of a file
func calculateB3Sum(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	hash := blake3.Sum256(data)
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

// Function to determine filesystem type from output file extension
func getFilesystemTypeFromOutputFile(outputFile string) string {
	ext := filepath.Ext(outputFile)
	secondExt := filepath.Ext(strings.TrimSuffix(outputFile, ext))

	// Check if it's .dwfs.AppBundle or .sqfs.AppBundle
	if ext == ".AppBundle" {
		if secondExt == ".dwfs" {
			return "dwarfs"
		} else if secondExt == ".sqfs" {
			return "squashfs"
		}
	}

	// Default to squashfs if no matching extension found
	return "squashfs"
}

func main() {
	app := &cli.Command{
		Name:  "pelf",
		Usage: "Create self-contained AppDir executables",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "output-to",
				Aliases: []string{"o"},
				Usage:   "Specify the output file name for the bundle",
			},
			&cli.StringFlag{
				Name:    "compression",
				Aliases: []string{"c"},
				Usage:   "Specify compression flags for the selected filesystem",
			},
			&cli.StringFlag{
				Name:    "add-appdir",
				Aliases: []string{"a"},
				Usage:   "Add an AppDir",
			},
			&cli.StringFlag{
				Name:    "appbundle-id",
				Aliases: []string{"i"},
				Usage:   "Specify the ID of the AppBundle",
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
				Value:   "squashfs",
			},
			&cli.BoolFlag{
				Name:  "prefer-tools-in-path",
				Usage: "Prefer tools in PATH over embedded binary dependencies",
			},
			&cli.BoolFlag{
				Name:  "list-static-tools",
				Usage: "List all binary dependencies with their B3SUMs",
			},
			&cli.BoolFlag{
				Name:    "disable-use-random-workdir",
				Aliases: []string{"d"},
				Usage:   "Disable the use of a random working directory",
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			config := &Config{
				AppDir:                c.String("add-appdir"),
				AppBundleID:           c.String("appbundle-id"),
				OutputFile:            c.String("output-to"),
				CompressionArgs:       c.String("compression"),
				DoNotEmbedStaticTools: c.Bool("do-not-embed-static-tools"),
				CustomEmbedDir:        c.String("static-tools-dir"),
				Runtime:               c.String("runtime"),
				UseUPX:                c.Bool("upx"),
				PreferToolsInPath:     c.Bool("prefer-tools-in-path"),
				DisableRandomWorkDir:  c.Bool("disable-use-random-workdir"),
			}

			// Extract binary dependencies and set up PATH
			var err error
			globalPath, err = setupBinaryDependencies(config)
			if err != nil {
				return fmt.Errorf("failed to set up binary dependencies: %w", err)
			}
			defer os.RemoveAll(config.BinDepDir)

			// Handle list-static-tools flag
			if c.Bool("list-static-tools") {
				return listStaticTools(config.BinDepDir)
			} else {
				if c.String("add-appdir") == "" || c.String("appbundle-id") == "" || c.String("output-to") == "" {
					return fmt.Errorf("--add-appdir, --appbundle-id and --output-to are obligatory parameters")
				}
			}

			// Determine filesystem type based on output file extension if not explicitly set
			if !c.IsSet("filesystem") && config.OutputFile != "" {
				config.FilesystemType = getFilesystemTypeFromOutputFile(config.OutputFile)
			} else {
				config.FilesystemType = c.String("filesystem")
			}

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
		Hash:           "",
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
		for fsName := range Filesystems[i].Type {
			if fsName == fsType {
				fs = &Filesystems[i]
				break
			}
		}
		if fs != nil {
			break
		}
	}
	if fs == nil {
		return fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

	// Use our custom lookPath instead of exec.LookPath
	for _, cmd := range fs.Commands {
		path, err := lookPath(cmd)
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
	if !config.DoNotEmbedStaticTools {
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
			path, err := lookPath(tool)
			if err != nil {
				return fmt.Errorf("command not found: %s", tool)
			}
			src = path
		}
		dest := filepath.Join(destDir, filepath.Base(src))

		// Check if the source is a symlink
		fi, err := os.Lstat(src)
		if err != nil {
			return err
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			// Read the symlink target
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			// Create the symlink at the destination
			if err := os.Symlink(target, dest); err != nil {
				return fmt.Errorf("failed to create symlink %s -> %s: %w", dest, target, err)
			}
		} else {
			// Copy regular file or directory
			if err := copyFile(src, dest); err != nil {
				return err
			}
		}
	}
	return nil
}

func compressWithUPX(dir string) error {
	// Use our custom lookPath instead of exec.LookPath
	if _, err := lookPath("upx"); err != nil {
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

	//zw, err := zstd.NewWriter(file, zstd.WithEncoderLevel(4))
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

		// Get the relative path for the header name
		relPath, err := filepath.Rel(filepath.Clean(srcDir), file)
		if err != nil {
			return err
		}

		// Create the header using the file info
		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}

		// Set the Name field correctly with the relative path
		header.Name = relPath

		// Format the header correctly for symlinks
		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(file)
			if err != nil {
				return err
			}
			header.Linkname = linkTarget
			header.Typeflag = tar.TypeSymlink
		}

		// Ensure the full file mode is preserved
		header.Mode = int64(fi.Mode())

		// FIXME: Preserve original, instead of making all files executable
		header.Mode |= 0111

		// Debug output
		fmt.Printf("File: %s, Original Mode: %o, Header Mode: %o, Typeflag: %c\n", header.Name, fi.Mode(), header.Mode, header.Typeflag)

		// Write the header
		if err := tw.WriteHeader(header); err != nil {
			return err
		}

		// If it's not a directory or symlink, copy the file content
		if !fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
			f, err := os.Open(file)
			if err != nil {
				return err
			}
			defer f.Close()

			if _, err = io.Copy(tw, f); err != nil {
				return err
			}
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

	if config.DisableRandomWorkDir {
		if _, err := fmt.Fprintf(out, "__APPBUNDLE_OPTS__: disableRandomWorkDir"); err != nil {
			return err
		}
	}

	var staticToolsOffset, staticToolsEndOffset, archiveOffset int64

	if !config.DoNotEmbedStaticTools {
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

	xattr.FRemove(out, "user.RuntimeConfig")

	if err := os.Chmod(config.OutputFile, 0755); err != nil {
		return fmt.Errorf("failed to make output file executable: %w", err)
	}

	return nil
}

func getFileSize(filePath string) int64 {
	fi, _ := os.Stat(filePath)
	return fi.Size()
}

func ternary[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}

