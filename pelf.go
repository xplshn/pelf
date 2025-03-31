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
	"github.com/fxamacker/cbor/v2"
	"github.com/klauspost/compress/zstd"
	"github.com/urfave/cli/v3"
	"github.com/zeebo/blake3"
	"github.com/pkg/xattr"
)

const pelFVersion = "3.0"

var globalPath = os.Getenv("PATH")

//go:embed binaryDependencies.tar.zst
var binaryDependencies []byte

type Filesystem struct {
	Type     map[string]string
	Commands []string
	CmdBuilder func(*Config) *exec.Cmd
}

var Filesystems = []Filesystem{
	{
		Type:     map[string]string{"squashfs": "sqfs"},
		Commands: []string{"mksquashfs", "squashfuse"},
		CmdBuilder: func(config *Config) *exec.Cmd {
			args := []string{"mksquashfs", config.AppDir, config.ArchivePath}
			compressionArgs := strings.Split(config.CompressionArgs, " ")
			if len(compressionArgs) == 1 && compressionArgs[0] == "" {
				compressionArgs = strings.Split("-comp zstd -Xcompression-level 22", " ")
			}
			args = append(args, compressionArgs...)
			path, err := lookPath(args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return exec.Command(path, args[1:]...)
		},
	},
	{
		Type:     map[string]string{"dwarfs": "dwfs"},
		Commands: []string{"dwarfs", "mkdwarfs"},
		CmdBuilder: func(config *Config) *exec.Cmd {
			compressionArgs := strings.Split(config.CompressionArgs, " ")
			args := []string{"mkdwarfs", "--input", config.AppDir, "--progress=ascii", "--set-owner", "0", "--set-group", "0", "--no-create-timestamp", "--no-history"}
			if len(compressionArgs) == 1 && compressionArgs[0] == "" {
				compressionArgs = strings.Split("-l7 --metadata-compression null", " ")
			}
			args = append(args, compressionArgs...)
			args = append(args, "--output", config.ArchivePath)
			path, err := lookPath(args[0])
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
			return exec.Command(path, args[1:]...)
		},
	},
}

type BuildInfo struct {
	StaticToolsSize int64
	ArchiveSize     int64
}

type RuntimeInfo struct {
	AppBundleID          string `json:"appBundleID"`
	PelfVersion          string `json:"pelfVersion"`
	HostInfo             string `json:"hostInfo"`
	FilesystemType       string `json:"filesystemType"`
	Hash                 string `json:"hash"`
	DisableRandomWorkDir bool   `json:"disableRandomWorkDir"`
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
	RuntimeInfo           RuntimeInfo
	PreferToolsInPath     bool
	BinDepDir             string
	DisableRandomWorkDir  bool
}

func lookPath(file string) (string, error) {
	if strings.Contains(file, "/") {
		if isExecutableFile(file) == nil {
			return file, nil
		}
		return "", fmt.Errorf("executable file not found in $PATH")
	}
	for _, dir := range strings.Split(globalPath, ":") {
		if dir == "" {
			dir = "."
		}
		path := dir + "/" + file
		if isExecutableFile(path) == nil {
			return path, nil
		}
	}
	return "", fmt.Errorf("executable file not found in $PATH")
}

func isExecutableFile(path string) error {
	fi, err := os.Stat(path)
	if err != nil {
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", path)
	}
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

		filePath := filepath.Join(binDepDir, header.Name)
		if header.Typeflag == tar.TypeDir {
			if err := os.MkdirAll(filePath, 0755); err != nil {
				return "", err
			}
			continue
		}

		if header.Typeflag == tar.TypeSymlink {
			if err := os.Symlink(header.Linkname, filePath); err != nil {
				return "", fmt.Errorf("failed to create symlink %s -> %s: %w", filePath, header.Linkname, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return "", err
		}

		outFile, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY, os.FileMode(header.Mode))
		if err != nil {
			return "", err
		}
		defer outFile.Close()

		if _, err := io.Copy(outFile, tr); err != nil {
			return "", err
		}
	}

	newPath := fmt.Sprintf("%s:%s", binDepDir, globalPath)
	if config.PreferToolsInPath {
		newPath = fmt.Sprintf("%s:%s", globalPath, binDepDir)
	}
	return newPath, nil
}

func calculateB3Sum(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := blake3.Sum256(data)
	return hex.EncodeToString(hash[:]), nil
}

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

func getFilesystemTypeFromOutputFile(outputFile string) string {
	ext := filepath.Ext(outputFile)
	secondExt := filepath.Ext(strings.TrimSuffix(outputFile, ext))
	if ext == ".AppBundle" {
		if secondExt == ".dwfs" {
			return "dwarfs"
		} else if secondExt == ".sqfs" {
			return "squashfs"
		}
	}
	return "squashfs"
}

func main() {
	app := &cli.Command{
		Name:  "pelf",
		Usage: "Create self-contained AppDir executables",
		Flags: []cli.Flag{
			&cli.StringFlag{Name: "output-to", Aliases: []string{"o"}, Usage: "Specify the output file name for the bundle"},
			&cli.StringFlag{Name: "compression", Aliases: []string{"c"}, Usage: "Specify compression flags for the selected filesystem"},
			&cli.StringFlag{Name: "add-appdir", Aliases: []string{"a"}, Usage: "Add an AppDir"},
			&cli.StringFlag{Name: "appbundle-id", Aliases: []string{"i"}, Usage: "Specify the ID of the AppBundle"},
			&cli.BoolFlag{Name: "do-not-embed-static-tools", Aliases: []string{"t"}, Usage: "Do not embed static tools into the bundle"},
			&cli.StringFlag{Name: "static-tools-dir", Usage: "Specify a custom directory from which to get the static tools"},
			&cli.StringFlag{Name: "runtime", Usage: "Specify which runtime shall be used", Sources: cli.EnvVars("PBUNDLE_RUNTIME")},
			&cli.BoolFlag{Name: "upx", Usage: "Enables usage of UPX compression in the static tools"},
			&cli.StringFlag{Name: "filesystem", Aliases: []string{"j"}, Usage: "Specify the filesystem type: 'dwarfs' for DWARFS, 'squashfs' for SQUASHFS", Value: "squashfs", Sources: cli.EnvVars("PBUNDLE_FS")},
			&cli.BoolFlag{Name: "prefer-tools-in-path", Usage: "Prefer tools in PATH over embedded binary dependencies"},
			&cli.BoolFlag{Name: "list-static-tools", Usage: "List all binary dependencies with their B3SUMs"},
			&cli.BoolFlag{Name: "disable-use-random-workdir", Aliases: []string{"d"}, Usage: "Disable the use of a random working directory"},
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

			var err error
			globalPath, err = setupBinaryDependencies(config)
			if err != nil {
				return fmt.Errorf("failed to set up binary dependencies: %w", err)
			}
			defer os.RemoveAll(config.BinDepDir)

			if c.Bool("list-static-tools") {
				return listStaticTools(config.BinDepDir)
			}

			if c.String("add-appdir") == "" || c.String("appbundle-id") == "" || c.String("output-to") == "" {
				return fmt.Errorf("--add-appdir, --appbundle-id and --output-to are obligatory parameters")
			}

			if !c.IsSet("filesystem") && config.OutputFile != "" {
				config.FilesystemType = getFilesystemTypeFromOutputFile(config.OutputFile)
			} else {
				config.FilesystemType = c.String("filesystem")
			}

			if err := initRuntimeInfo(&config.RuntimeInfo, config.FilesystemType, config.AppBundleID, config.DisableRandomWorkDir); err != nil {
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

func initRuntimeInfo(runtimeInfo *RuntimeInfo, filesystemType, appBundleID string, disableRandomWorkDir bool) error {
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

	*runtimeInfo = RuntimeInfo{
		AppBundleID:    appBundleID,
		PelfVersion:    pelFVersion,
		HostInfo:       hostInfo,
		FilesystemType: filesystemType,
		Hash:           "",
		DisableRandomWorkDir: disableRandomWorkDir,
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
	for _, f := range Filesystems {
		if _, ok := f.Type[fsType]; ok {
			fs = &f
			break
		}
	}
	if fs == nil {
		return fmt.Errorf("unsupported filesystem type: %s", fsType)
	}

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
		return fmt.Errorf("failed to create image filesystem: %s", string(out))
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

		fi, err := os.Lstat(src)
		if err != nil {
			return err
		}

		if fi.Mode()&os.ModeSymlink != 0 {
			target, err := os.Readlink(src)
			if err != nil {
				return err
			}
			if err := os.Symlink(target, dest); err != nil {
				return fmt.Errorf("failed to create symlink %s -> %s: %w", dest, target, err)
			}
		} else {
			if err := copyFile(src, dest); err != nil {
				return err
			}
		}
	}
	return nil
}

func compressWithUPX(dir string) error {
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

		relPath, err := filepath.Rel(filepath.Clean(srcDir), file)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(fi, "")
		if err != nil {
			return err
		}
		header.Name = relPath

		if fi.Mode()&os.ModeSymlink != 0 {
			linkTarget, err := os.Readlink(file)
			if err != nil {
				return err
			}
			header.Linkname = linkTarget
			header.Typeflag = tar.TypeSymlink
		}

		header.Mode = int64(fi.Mode())

		// FIXME: Preserve original, instead of making all files executable
		header.Mode |= 0111

		if err := tw.WriteHeader(header); err != nil {
			return err
		}

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

	// Ensure DisableRandomWorkDir is set correctly in RuntimeInfo
	config.RuntimeInfo.DisableRandomWorkDir = config.DisableRandomWorkDir

	var err error
	config.RuntimeInfo.Hash, err = calculateB3Sum(filepath.Join(workDir, "archive."+config.FilesystemType))
	if err != nil {
		return fmt.Errorf("failed to calculate hash of filesystem image: %w", err)
	}

	if err := os.Chmod(config.OutputFile, 0755); err != nil {
		return fmt.Errorf("failed to make output file executable: %w", err)
	}

	runtimeInfoCBOR, err := cbor.Marshal(config.RuntimeInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal RuntimeInfo to CBOR: %w", err)
	}

	runtimeInfoTempFile, err := os.CreateTemp("", "runtime_info_*.cbor")
	if err != nil {
		return fmt.Errorf("failed to create tempfile for RuntimeInfo CBOR: %w", err)
	}
	defer os.Remove(runtimeInfoTempFile.Name())

	if _, err := runtimeInfoTempFile.Write(runtimeInfoCBOR); err != nil {
		return fmt.Errorf("failed to write RuntimeInfo CBOR to tempfile: %w", err)
	}
	if err := runtimeInfoTempFile.Close(); err != nil {
		return fmt.Errorf("failed to close RuntimeInfo CBOR tempfile: %w", err)
	}

	objcopyCmd := exec.Command("objcopy",
		"--add-section", ".static_tools="+filepath.Join(workDir, "static.tar.zst"),
		"--add-section", ".runtime_info="+runtimeInfoTempFile.Name(),
		config.OutputFile,
	)

	if out, err := objcopyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add ELF sections: %s", string(out))
	}

	outFile, err := os.OpenFile(config.OutputFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer outFile.Close()

	fsFile, err := os.Open(filepath.Join(workDir, "archive."+config.FilesystemType))
	if err != nil {
		return err
	}
	defer fsFile.Close()

	if _, err := io.Copy(outFile, fsFile); err != nil {
		return err
	}

	xattr.FRemove(outFile, "user.RuntimeConfig")

	return nil
}

func getFileSize(filePath string) int64 {
	fi, _ := os.Stat(filePath)
	return fi.Size()
}
