// PELF - The AppBundle format and the AppBundle Creation Tool
// It used to stand for Pack an Elf, but we slowly evolved into a much simpler yet more featureful alternative to .AppImages
// PELF now refers to the tool used to create .AppBundles
//
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

	"github.com/klauspost/compress/zstd"
	"github.com/pkg/xattr"
	"github.com/shamaton/msgpack/v2"
	"github.com/urfave/cli/v3"
	"github.com/xplshn/pelf/pkg/utils"
	"github.com/zeebo/blake3"
	"golang.org/x/sys/unix"
)

const (
	pelfVersion = "3.0"
	// colors
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"
)

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
		Commands: []string{"squashfuse", "unsquashfs"},
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
		Commands: []string{"dwarfs", "dwarfsextract"},
		CmdBuilder: func(config *Config) *exec.Cmd {
			compressionArgs := strings.Split(config.CompressionArgs, " ")
			args := []string{"mkdwarfs", "--input", config.AppDir, "--progress=ascii", "--memory-limit=auto", "--set-owner", "0", "--set-group", "0", "--no-create-timestamp", "--no-history"}
			if len(compressionArgs) == 1 && compressionArgs[0] == "" {
				compressionArgs = strings.Split("-l7", " ")
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
	AppBundleID          string `json:"AppBundleID"`
	PelfVersion          string `json:"PelfVersion"`
	HostInfo             string `json:"HostInfo"`
	FilesystemType       string `json:"FilesystemType"`
	Hash                 string `json:"Hash"`
	DisableRandomWorkDir bool   `json:"DisableRandomWorkDir"`
	MountOrExtract       uint8  `json:"MountOrExtract"`
}

type elfSectionSpec struct {
	Name string
	Path string
	Temp bool
}

type Config struct {
	DoNotEmbedStaticTools bool
	UseUPX                bool
	PreferToolsInPath     bool
	DisableRandomWorkDir  bool
	MountOrExtract        bool
	AppImageCompat        bool
	AppDir                string
	AppBundleID           string
	OutputFile            string
	CompressionArgs       string
	CustomEmbedDir        string
	FilesystemType        string
	ArchivePath           string
	Runtime               string
	BinDepDir             string
	CustomSections        []string
	RuntimeInfo           RuntimeInfo
	RunBehavior           uint8
	elfSections           []elfSectionSpec
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
	return "dwarfs"
}

func validateRunBehavior(_ context.Context, _ *cli.Command, value uint) error {
	if value > 3 {
		return fmt.Errorf("run-behavior must be one of 0, 1, 2, or 3")
	}
	return nil
}

func checkAppDir(appDir string, appBundleID *utils.AppBundleID) error {
	fsys := os.DirFS(appDir)
	if _, err := os.Stat(appDir); os.IsNotExist(err) {
		return fmt.Errorf("AppDir does not exist: %s", appDir)
	}

	filesToCheck := []struct {
		name      string
		warning   string
		globs     []string
		mustExist bool
	}{
		{name: ".xml file", globs: []string{"*.xml"}},
		{name: ".desktop file", globs: []string{"*.desktop"}},
		{name: ".DirIcon file", globs: []string{"*.desktop"}},
		{name: "AppRun", globs: []string{"AppRun"}, mustExist: true},
	}

	for _, fileCheck := range filesToCheck {
		path, err := utils.FindFiles(fsys, ".", 1, fileCheck.globs)
		if err != nil {
			return fmt.Errorf("failed to check for %s in AppDir: %w", fileCheck.name, err)
		}
		if path == "" {
			if fileCheck.mustExist {
				return fmt.Errorf("error: %s does not exist in %s at depth %d", fileCheck.name, filepath.Base(appDir), 1)
			}
			fmt.Fprintf(os.Stderr, "%swarning%s: No %s found in the top-level of the AppDir: %s\n", warningColor, resetColor, fileCheck.name, appDir)
			if fileCheck.name == ".xml file" && !utils.IsAppStreamID(appBundleID.Name) {
				fmt.Fprintf(os.Stderr, "%s without an AppStream file and without an AppStreamID as the AppBundleID's name part, this AppBundle will not get metadata that's automatically populated by appstream-helper\n", strings.Repeat(" ", len("warning:")))
			}
		}
	}

	return nil
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
			&cli.StringFlag{Name: "static-tools-dir", Usage: "Specify a custom directory from which to get the static tools"},
			&cli.StringFlag{Name: "runtime", Usage: "Specify which runtime shall be used", Sources: cli.EnvVars("PBUNDLE_RUNTIME")},
			&cli.BoolFlag{Name: "upx", Usage: "Enables usage of UPX compression in the static tools"},
			&cli.StringFlag{Name: "filesystem", Aliases: []string{"j"}, Usage: "Specify the filesystem type: 'dwarfs' for DWARFS, 'squashfs' for SQUASHFS", Value: "dwarfs", Sources: cli.EnvVars("PBUNDLE_FS")},
			&cli.BoolFlag{Name: "prefer-tools-in-path", Usage: "Prefer tools in PATH over embedded binary dependencies"},
			&cli.BoolFlag{Name: "list-static-tools", Usage: "List all binary dependencies with their B3SUMs"},
			&cli.BoolFlag{Name: "disable-use-random-workdir", Aliases: []string{"d"}, Usage: "Disable the use of a random working directory"},
			&cli.BoolFlag{Name: "appimage-compat", Aliases: []string{"A"}, Usage: "Use AI as magic bytes for AppImage compatibility"},
			&cli.StringSliceFlag{Name: "add-runtime-info-section", Usage: "Add a custom section to runtime info in format '.sectionName:contentsOfSection'"},
			&cli.UintFlag{Name: "run-behavior", Aliases: []string{"b"}, Usage: "Specify the run behavior of the output AppBundle (0[Only FUSE mounting], 1[Only Extract & Run], 2[Try FUSE, fallback to Extract & Run], 3[2, but only if the file is <= 350MB])", Value: 3, Action: validateRunBehavior},
			&cli.StringSliceFlag{Name: "add-elf-section", Usage: "Add custom ELF sections from an .elfS file (e.g. --add-elf-section=./foo.elfS); section name is file name without .elfS extension, section contents are file contents"},
			&cli.StringFlag{Name: "add-updinfo", Usage: "Add an ELF section named upd_info, with a string as its contents"},
		},
		Action: func(_ context.Context, c *cli.Command) error {
			config := &Config{
				AppDir:               c.String("add-appdir"),
				AppBundleID:          c.String("appbundle-id"),
				OutputFile:           c.String("output-to"),
				CompressionArgs:      c.String("compression"),
				CustomEmbedDir:       c.String("static-tools-dir"),
				Runtime:              c.String("runtime"),
				UseUPX:               c.Bool("upx"),
				PreferToolsInPath:    c.Bool("prefer-tools-in-path"),
				DisableRandomWorkDir: c.Bool("disable-use-random-workdir"),
				AppImageCompat:       c.Bool("appimage-compat"),
				CustomSections:       c.StringSlice("add-runtime-info-section"),
				RunBehavior:          uint8(c.Uint("run-behavior")),
			}

			// Validate and process AppBundleID
			if config.AppBundleID == "" {
				return fmt.Errorf("--appbundle-id is an obligatory parameter")
			}
			appBundleID, t, err := utils.ParseAppBundleID(config.AppBundleID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%swarning%s: AppBundleID does not follow the spec-compliant format: %v\n", warningColor, resetColor, err)
			}
			if appBundleID == nil {
				appBundleID = &utils.AppBundleID{Raw: config.AppBundleID}
			}
			// If output-to is not provided, AppBundleID must be compliant
			if config.OutputFile == "" {
				if _, err := appBundleID.Compliant(); err != nil {
					return fmt.Errorf("AppBundleID must be in a valid format when --output-to is not provided: %v", err)
				}
			}

			if t == utils.TypeI {
				fmt.Fprintf(os.Stderr, "%swarning%s: AppBundleID is type I (%s). Recommended format is type II or type III, while type I shall only be used for filenames. Example: 'name#repo[:version][@date]'\n", warningColor, resetColor, appBundleID.Raw)
			}

			// Handle output file
			if config.OutputFile == "" {
				// Craft filename using AppBundleID in Type I format
				typeIOutput, err := appBundleID.Format(utils.TypeI)
				if err != nil {
					//// Fallback to Type II if Type I is not possible
					//typeIOutput, err = appBundleID.Format(utils.TypeII)
					//if err != nil {
						return fmt.Errorf("cannot generate output filename: %v", err)
					//}
				}
				fsExt := ".dwfs"
				if c.IsSet("filesystem") {
					if c.String("filesystem") == "squashfs" {
						fsExt = ".sqfs"
					}
				}
				config.OutputFile = typeIOutput + fsExt + ".AppBundle"
			}

			addSectionFiles := c.StringSlice("add-elf-section")
			updinfoStr := c.String("add-updinfo")
			var elfSections []elfSectionSpec

			for _, path := range addSectionFiles {
				if !strings.HasSuffix(path, ".elfS") {
					return fmt.Errorf("--add-elf-section file must have .elfS extension: %s", path)
				}
				sectionName := strings.TrimSuffix(filepath.Base(path), ".elfS")
				elfSections = append(elfSections, elfSectionSpec{
					Name: sectionName,
					Path: path,
					Temp: false,
				})
			}

			var updinfoTempFile string
			if updinfoStr != "" {
				tmpfile, err := os.CreateTemp("", "upd_info_*.elfS")
				if err != nil {
					return fmt.Errorf("failed to create temp .elfS file for upd_info: %w", err)
				}
				_, err = tmpfile.Write([]byte(updinfoStr))
				if err != nil {
					tmpfile.Close()
					os.Remove(tmpfile.Name())
					return fmt.Errorf("failed to write to temp .elfS file for upd_info: %w", err)
				}
				tmpfile.Close()
				updinfoTempFile = tmpfile.Name()
				elfSections = append(elfSections, elfSectionSpec{
					Name: "upd_info",
					Path: updinfoTempFile,
					Temp: true,
				})
			}

			config.elfSections = elfSections

			globalPath, err = setupBinaryDependencies(config)
			if err != nil {
				if updinfoTempFile != "" {
					os.Remove(updinfoTempFile)
				}
				return fmt.Errorf("failed to set up binary dependencies: %w", err)
			}
			defer func() {
				os.RemoveAll(config.BinDepDir)
				if updinfoTempFile != "" {
					os.Remove(updinfoTempFile)
				}
			}()

			if c.Bool("list-static-tools") {
				return listStaticTools(config.BinDepDir)
			}

			if c.String("add-appdir") == "" {
				return fmt.Errorf("--add-appdir is an obligatory parameter")
			}

			if !c.IsSet("filesystem") && config.OutputFile != "" {
				config.FilesystemType = getFilesystemTypeFromOutputFile(config.OutputFile)
			} else {
				config.FilesystemType = c.String("filesystem")
			}

			if err := initRuntimeInfo(&config.RuntimeInfo, config.FilesystemType, config.AppBundleID, config.DisableRandomWorkDir, config.RunBehavior); err != nil {
				return err
			}

			return run(config, appBundleID)
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "fatal error: %v\n", err)
		os.Exit(1)
	}
}

func initRuntimeInfo(runtimeInfo *RuntimeInfo, filesystemType, appBundleID string, disableRandomWorkDir bool, runBehavior uint8) error {
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
		AppBundleID:          appBundleID,
		PelfVersion:          pelfVersion,
		HostInfo:             hostInfo,
		FilesystemType:       filesystemType,
		Hash:                 "",
		DisableRandomWorkDir: disableRandomWorkDir,
		MountOrExtract:       runBehavior,
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

func run(cfg *Config, appBundleID *utils.AppBundleID) error {
	if err := checkAppDir(cfg.AppDir, appBundleID); err != nil {
		return err
	}

	fsType := cfg.FilesystemType
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

	cfg.ArchivePath = filepath.Join(workDir, " archive."+fsType)
	if err := createArchive(cfg, fs); err != nil {
		return err
	}

	if err := createSelfExtractingArchive(cfg, workDir); err != nil {
		return err
	}

	magic := "AB"
	if cfg.AppImageCompat {
		magic = "AI"
	}
	if err := addMagic(cfg.OutputFile, magic); err != nil {
		return fmt.Errorf("failed to add magic bytes: %w", err)
	}

	return nil
}

func addMagic(path, magic string) error {
	magicBytes := fmt.Sprintf("%s\x02", magic)
	file, err := os.OpenFile(path, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	if _, err := file.Seek(8, io.SeekStart); err != nil {
		return err
	}

	if _, err := file.Write([]byte(magicBytes[:3])); err != nil {
		return err
	}

	return nil
}

func createArchive(config *Config, fs *Filesystem) error {
	cmd := fs.CmdBuilder(config)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() != 2 {
			return fmt.Errorf("failed to create image filesystem: %w", err)
		}
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

			if _, err := io.Copy(tw, f); err != nil {
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

func createSelfExtractingArchive(config *Config, workDir string) error {
	runtimePath := config.Runtime
	if runtimePath == "" {
		runtimePath = filepath.Join(config.BinDepDir, "appbundle-runtime_"+config.FilesystemType)
		config.DoNotEmbedStaticTools = true
		if _, err := os.Stat(runtimePath); os.IsNotExist(err) {
			runtimePath = filepath.Join(config.BinDepDir, "appbundle-runtime")
			config.DoNotEmbedStaticTools = false
			if _, err := os.Stat(runtimePath); os.IsNotExist(err) {
				return fmt.Errorf("User did not provide --runtime flag and we apparently lack a default embedded runtime")
			}
		}
	}

	if err := copyFile(runtimePath, config.OutputFile); err != nil {
		return fmt.Errorf("failed to copy runtime to output file: %w", err)
	}

	config.RuntimeInfo.DisableRandomWorkDir = config.DisableRandomWorkDir

	var err error
	config.RuntimeInfo.Hash, err = calculateB3Sum(config.ArchivePath)
	if err != nil {
		return fmt.Errorf("failed to calculate hash of filesystem image: %w", err)
	}

	if err := os.Chmod(config.OutputFile, 0755); err != nil {
		return fmt.Errorf("failed to make output file executable: %w", err)
	}

	runtimeInfoData, err := msgpack.Marshal(config.RuntimeInfo)
	if err != nil {
		return fmt.Errorf("failed to marshal RuntimeInfo: %w", err)
	}

	if len(config.CustomSections) > 0 {
		var runtimeInfoMap map[string]interface{}
		if err := msgpack.Unmarshal(runtimeInfoData, &runtimeInfoMap); err != nil {
			return fmt.Errorf("failed to unmarshal RuntimeInfo for modification: %w", err)
		}

		for _, section := range config.CustomSections {
			parts := strings.SplitN(section, ":", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid section format, expected '.sectionName:contents', got: %s", section)
			}
			sectionName := strings.TrimSpace(parts[0])
			if !strings.HasPrefix(sectionName, ".") {
				return fmt.Errorf("section name must start with '.', got: %s", sectionName)
			}
			runtimeInfoMap[sectionName[1:]] = parts[1]
		}

		runtimeInfoData, err = msgpack.Marshal(runtimeInfoMap)
		if err != nil {
			return fmt.Errorf("failed to remarshal modified RuntimeInfo: %w", err)
		}
	}

	runtimeInfoTempFile, err := os.CreateTemp("", "runtime_info_*.msgpack")
	if err != nil {
		return fmt.Errorf("failed to create tempfile for RuntimeInfo serialization: %w", err)
	}
	defer os.Remove(runtimeInfoTempFile.Name())

	if _, err := runtimeInfoTempFile.Write(runtimeInfoData); err != nil {
		return fmt.Errorf("failed to write RuntimeInfo serialization to tempfile: %w", err)
	}
	if err := runtimeInfoTempFile.Close(); err != nil {
		return fmt.Errorf("failed to close RuntimeInfo tempfile: %w", err)
	}

	objcopyPath, err := lookPath("objcopy")
	if err != nil {
		return fmt.Errorf("No objcopy binary in $PATH: %w", err)
	}

	var objcopyArgs []string

	for _, sec := range config.elfSections {
		if sec.Name == "" || sec.Path == "" {
			return fmt.Errorf("invalid custom ELF section: name=%q path=%q", sec.Name, sec.Path)
		}
		objcopyArgs = append(objcopyArgs, "--add-section", "."+sec.Name+"="+sec.Path)
	}

	if !config.DoNotEmbedStaticTools {
		objcopyArgs = append(objcopyArgs, "--add-section", ".pbundle_static_tools="+filepath.Join(workDir, "static.tar.zst"))
	}

	objcopyArgs = append(objcopyArgs, "--add-section", ".pbundle_runtime_info="+runtimeInfoTempFile.Name())
	objcopyArgs = append(objcopyArgs, config.OutputFile)

	objcopyCmd := exec.Command(objcopyPath, objcopyArgs...)
	if out, err := objcopyCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to add ELF sections: %s", string(out))
	}

	archiveInfo, err := os.Stat(config.ArchivePath)
	if err != nil {
		return fmt.Errorf("failed to get archive file info: %w", err)
	}
	expectedSize := archiveInfo.Size()
	fmt.Printf("Appending archive file (%d bytes) to output file...\n", expectedSize)

	outFile, err := os.OpenFile(config.OutputFile, os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open output file for appending: %w", err)
	}
	defer outFile.Close()

	fsFile, err := os.Open(config.ArchivePath)
	if err != nil {
		return fmt.Errorf("failed to open archive file: %w", err)
	}
	defer fsFile.Close()

	buf := make([]byte, 4*1024*1024)
	written, err := io.CopyBuffer(outFile, fsFile, buf)
	if err != nil {
		return fmt.Errorf("failed to append archive to output file: %w", err)
	}
	if err := outFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync output file: %w", err)
	}
	if written != expectedSize {
		return fmt.Errorf("failed to append entire archive: expected %d bytes, wrote %d bytes", expectedSize, written)
	}
	fmt.Printf("Successfully appended %d bytes to output file\n", written)

	xattr.FRemove(outFile, "user.RuntimeConfig")
	return nil
}

func getFileSize(filePath string) int64 {
	fi, _ := os.Stat(filePath)
	return fi.Size()
}
