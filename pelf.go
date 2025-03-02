package main

import (
	"archive/tar"
	"context"
	_ "embed"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/klauspost/compress/zstd"
	"github.com/pkg/xattr"
	"github.com/urfave/cli/v3"
)

const pelFVersion = "3.0"

//go:embed appbundle-runtime/runtime
var appbundleRuntime []byte

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
	AppBundleID string            `json:"appBundleID"`
	PelfVersion string            `json:"pelfVersion"`
	HostInfo    string            `json:"hostInfo"`
	Offsets     map[string]int64  `json:"offsets"`
	FilesystemType string         `json:"filesystemType"`
}

type Config struct {
	AppDir          string
	AppBundleID     string
	OutputFile      string
	CompressionArgs string
	CustomEmbedDir  string
	FilesystemType  string
	ArchivePath     string
	Runtime         string
	EmbedStaticTools bool
	UseUPX          bool
	RuntimeInfo     RuntimeConfig
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
				Required: true,
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
				Required: true,
			},
			&cli.StringFlag{
				Name:     "appbundle-id",
				Aliases:  []string{"i"},
				Usage:    "Specify the ID of the AppBundle",
				Required: true,
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
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			config := &Config{
				AppDir:          c.String("add-appdir"),
				AppBundleID:     c.String("appbundle-id"),
				OutputFile:      c.String("output-to"),
				CompressionArgs: c.String("compression"),
				EmbedStaticTools: !c.Bool("do-not-embed-static-tools"),
				CustomEmbedDir:  c.String("static-tools-dir"),
				Runtime:         c.String("runtime"),
				UseUPX:          c.Bool("upx"),
				FilesystemType:  c.String("filesystem"),
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
		AppBundleID:   appBundleID,
		PelfVersion:   pelFVersion,
		HostInfo:      hostInfo,
		FilesystemType: filesystemType,
		Offsets:       make(map[string]int64),
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

	if err := checkCommands(fs.Commands); err != nil {
		return err
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

func checkCommands(commands []string) error {
	for _, cmd := range commands {
		if _, err := exec.LookPath(cmd); err != nil {
			return fmt.Errorf("command not found: %s", cmd)
		}
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
			path, err := exec.LookPath(tool)
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
	if _, err := exec.LookPath("upx"); err != nil {
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
		runtimePath = filepath.Join(workDir, "appbundle-runtime")
		if err := os.WriteFile(runtimePath, appbundleRuntime, 0755); err != nil {
			return fmt.Errorf("failed to write embedded runtime: %w", err)
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
