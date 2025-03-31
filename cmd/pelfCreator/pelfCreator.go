package main

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mholt/archives"
	"github.com/urfave/cli/v3"
	"github.com/zeebo/blake3"
	"golang.org/x/sys/unix"
)

//go:embed binaryDependencies.tar.zst
var binaryDependencies []byte

const (
	defaultRootfsURL = "https://github.com/xplshn/filesystems/releases/latest/download/AlpineLinux_edge-%s.tar.zst"
	dirPermissions   = 0o755
	filePermissions  = 0o644
)

type Config struct {
	Maintainer    string
	Name          string
	AppBundleID   string
	PkgAdd        string
	Entrypoint    string
	DontPack      bool
	Sharun        bool
	Lib4binArgs   string
	Sandbox       bool
	ToBeKeptFiles string
	GetridFiles   string
	AppBundleFS   string
	OutputTo      string
	LocalPath     string
	AppDir        string
	Date          string
	TempDir       string
}

func main() {
	var config Config

	app := &cli.Command{
		Name:  "pelfCreator",
		Usage: "Create self-contained AppBundle executables",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:        "maintainer",
				Aliases:     []string{"m"},
				Usage:       "Set the maintainer",
				Required:    true,
				Destination: &config.Maintainer,
			},
			&cli.StringFlag{
				Name:        "name",
				Aliases:     []string{"n"},
				Usage:       "Set the name of the app",
				Required:    true,
				Destination: &config.Name,
			},
			&cli.StringFlag{
				Name:        "pkg-add",
				Aliases:     []string{"p"},
				Usage:       "Packages to add with APK",
				Required:    true,
				Destination: &config.PkgAdd,
			},
			&cli.StringFlag{
				Name:        "entrypoint",
				Aliases:     []string{"e"},
				Usage:       "Set the entrypoint (required unless using --multicall)",
				Destination: &config.Entrypoint,
			},
			&cli.BoolFlag{
				Name:        "dontpack",
				Aliases:     []string{"z"},
				Usage:       "Disables .dwfs.AppBundle packaging, thus leaving only the AppDir",
				Destination: &config.DontPack,
			},
			&cli.StringFlag{
				Name:        "sharun",
				Aliases:     []string{"x"},
				Usage:       "Processes the desired binaries with lib4bin and adds sharun",
				Destination: &config.Lib4binArgs,
			},
			&cli.BoolFlag{
				Name:        "sandbox",
				Aliases:     []string{"s"},
				Usage:       "Enable sandbox mode (uses AppRun.rootfs-based)",
				Destination: &config.Sandbox,
			},
			&cli.StringFlag{
				Name:        "keep",
				Aliases:     []string{"k"},
				Usage:       "Only keeps the given files from the AppDir/proto (rootfs)",
				Destination: &config.ToBeKeptFiles,
			},
			&cli.StringFlag{
				Name:        "getrid",
				Aliases:     []string{"r"},
				Usage:       "Removes only the given from the AppDir/proto (rootfs)",
				Destination: &config.GetridFiles,
			},
			&cli.StringFlag{
				Name:        "filesystem",
				Aliases:     []string{"j"},
				Usage:       "Select a filesystem to use for the output AppBundle",
				Value:       "dwfs",
				Destination: &config.AppBundleFS,
			},
			&cli.StringFlag{
				Name:        "output-to",
				Aliases:     []string{"o"},
				Usage:       "Set the output file name (optional, default: <name>-<date>.dwfs.AppBundle)",
				Destination: &config.OutputTo,
			},
			&cli.StringFlag{
				Name:        "local",
				Usage:       "A directory from which to pick up files such as 'AppRun.sharun', 'rootfs.tgz', 'pelf', 'bwrap', etc",
				Destination: &config.LocalPath,
			},
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			config.Date = time.Now().Format("02_01_2006")
			config.AppBundleID = fmt.Sprintf("%s-%s-%s", config.Name, config.Date, config.Maintainer)
			config.AppDir = fmt.Sprintf("%s.AppDir", config.AppBundleID)
			if config.Lib4binArgs != "" {
				config.Sharun = true
				parts := strings.Fields(config.Lib4binArgs)
				for i, part := range parts {
					parts[i] = filepath.Join(config.AppDir, "proto", part)
				}
				config.Lib4binArgs = strings.Join(parts, " ")
			}

			var err error
			config.TempDir, err = os.MkdirTemp("", "pelfCreator-deps")
			if err != nil {
				return fmt.Errorf("failed to create temp dir: %v", err)
			}
			defer os.RemoveAll(config.TempDir)

			return runPelfCreator(config)
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func runPelfCreator(config Config) error {
	protoDir := filepath.Join(config.AppDir, "proto")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return fmt.Errorf("failed to create proto directory: %v", err)
	}

	genStepsPath := filepath.Join(config.AppDir, ".genSteps")
	genStepsContent := fmt.Sprintf("pelfCreator %s", strings.Join(os.Args[1:], " "))
	if err := os.WriteFile(genStepsPath, []byte(genStepsContent), 0755); err != nil {
		return fmt.Errorf("failed to create .genSteps: %v", err)
	}

	if err := setupDependencies(config); err != nil {
		return err
	}

	rootfsPath, err := findRootfs(config)
	if err != nil {
		return err
	}

	if err := extractToDirectory(rootfsPath, protoDir); err != nil {
		return fmt.Errorf("failed to extract rootfs: %v", err)
	}

	if err := setupAppRunAndPackages(config); err != nil {
		return err
	}

	if config.Entrypoint != "" {
		if err := createEntrypoint(config); err != nil {
			return fmt.Errorf("entrypoint creation failed: %v", err)
		}

		if strings.HasSuffix(config.Entrypoint, ".desktop") {
			if err := handleDesktopFile(config); err != nil {
				return fmt.Errorf("failed to handle desktop file: %v", err)
			}
		}
	}

	// Handle the three structures based on configuration
	if config.Sandbox {
		// Sandbox mode - uses AppRun.rootfs-based
		if err := setupSandboxMode(config); err != nil {
			return err
		}
	} else if config.Sharun {
		// Sharun or Hybrid mode
		if err := setupSharunMode(config); err != nil {
			return err
		}
	} else {
		// Default mode (similar to Hybrid but without Sharun)
		if err := setupDefaultMode(config); err != nil {
			return err
		}
	}

	if err := tidyUp(config); err != nil {
		return fmt.Errorf("cleanup failed: %v", err)
	}

	if !config.DontPack {
		if err := createBundle(config); err != nil {
			return fmt.Errorf("bundle creation failed: %v", err)
		}
	}

	fmt.Printf("Successfully created %s\n", config.OutputTo)
	return nil
}

func setupSandboxMode(config Config) error {
	// Copy sandbox-specific files
	if err := copyFromTemp(config, "bwrap", filepath.Join(config.AppDir, "usr/bin/bwrap"), 0755); err != nil {
		return fmt.Errorf("bwrap setup failed: %v", err)
	}

	// Setup sandbox-specific files
	protoDir := filepath.Join(config.AppDir, "proto")
	if err := setupSandboxFiles(protoDir); err != nil {
		return err
	}

	// Use AppRun.rootfs-based
	return copyFromTemp(config, "AppRun.rootfs-based", filepath.Join(config.AppDir, "AppRun"), 0755)
}

func setupSharunMode(config Config) error {
	// Process binaries with lib4bin
	if err := setupLib4bin(config); err != nil {
		return err
	}

	// Handle proto directory based on keep/getrid flags
	if config.ToBeKeptFiles != "" || config.GetridFiles != "" {
		// Hybrid mode - keep specified files
		if err := trimProtoDir(config); err != nil {
			return err
		}
		// Use AppRun.sharun.ovfsProto
		if err := copyFromTemp(config, "AppRun.sharun.ovfsProto", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
			return err
		}
	} else {
		// Pure Sharun mode - remove proto dir completely
		if err := os.RemoveAll(filepath.Join(config.AppDir, "proto")); err != nil {
			return err
		}
		// Use AppRun.sharun
		if err := copyFromTemp(config, "AppRun.sharun", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
			return err
		}
	}

	// Copy unionfs for hybrid mode
	if config.ToBeKeptFiles != "" || config.GetridFiles != "" {
		if err := copyFromTemp(config, "unionfs", filepath.Join(config.AppDir, "usr", "bin", "unionfs"), 0755); err != nil {
			return err
		}
	}

	return nil
}

func setupDefaultMode(config Config) error {
	// Handle proto directory based on keep/getrid flags
	if config.ToBeKeptFiles != "" || config.GetridFiles != "" {
		if err := trimProtoDir(config); err != nil {
			return err
		}
	}

	// Use AppRun.sharun.ovfsProto for default mode
	if err := copyFromTemp(config, "AppRun.sharun.ovfsProto", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
		return err
	}

	// Copy unionfs
	if err := copyFromTemp(config, "unionfs", filepath.Join(config.AppDir, "usr", "bin", "unionfs"), 0755); err != nil {
		return err
	}

	return nil
}

func setupDependencies(config Config) error {
	tempArchive := filepath.Join(config.TempDir, "binaryDependencies.tar.zst")
	if err := os.WriteFile(tempArchive, binaryDependencies, 0644); err != nil {
		return fmt.Errorf("failed to write temp archive: %v", err)
	}

	if err := extractToDirectory(tempArchive, config.TempDir); err != nil {
		return fmt.Errorf("failed to extract binaryDependencies: %v", err)
	}

	return nil
}

func findRootfs(config Config) (string, error) {
	if config.LocalPath != "" {
		localRootfs, err := findFirstMatch(config.LocalPath, "rootfs.tar*")
		if err == nil {
			return localRootfs, nil
		}
	}

	return findFirstMatch(config.TempDir, "rootfs.tar*")
}

func findFirstMatch(dir, pattern string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return "", fmt.Errorf("glob failed: %v", err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("no matches found for %s", pattern)
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("multiple matches found for %s: %v", pattern, matches)
	}
	return matches[0], nil
}

func copyFromTemp(config Config, srcRelPath, dest string, mode os.FileMode) error {
	srcPath := filepath.Join(config.TempDir, srcRelPath)
	if config.LocalPath != "" {
		localSrcPath := filepath.Join(config.LocalPath, srcRelPath)
		if _, err := os.Stat(localSrcPath); err == nil {
			srcPath = localSrcPath
		}
	}

	data, err := os.ReadFile(srcPath)
	if err != nil {
		return fmt.Errorf("failed to read source file: %v", err)
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0755); err != nil {
		return err
	}

	if err := os.WriteFile(dest, data, mode); err != nil {
		return fmt.Errorf("failed to write destination file: %v", err)
	}
	return nil
}

func setupAppRunAndPackages(config Config) error {
	entrypointPath := filepath.Join(config.AppDir, "entrypoint")
	if err := os.WriteFile(entrypointPath, []byte("sh"), 0755); err != nil {
		return err
	}

	if config.Sandbox {
		appRunPath := filepath.Join(config.AppDir, "AppRun.rootfs-based")
		if err := copyFromTemp(config, "AppRun.rootfs-based", appRunPath, 0755); err != nil {
			return err
		}
	}

	if err := os.WriteFile(filepath.Join(config.AppDir, "entrypoint"), []byte("sh"), 0755); err != nil {
		return err
	}

	if err := copyFromTemp(config, "AppRun.rootfs-based", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
		return err
	}

	pkgAddPath := filepath.Join(config.AppDir, "pkgadd.sh")
	if err := copyFromTemp(config, "pkgadd.sh", pkgAddPath, 0755); err != nil {
		return err
	}

	cmd := exec.Command(filepath.Join(config.AppDir, "AppRun"), "--Xbwrap", "--uid", "0", "--gid", "0", "--", pkgAddPath, config.PkgAdd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run pkgadd.sh: %v", err)
	}

	if config.Sandbox {
		protoLocalBinDir := filepath.Join(config.AppDir, "proto", "usr", "local", "bin")
		if err := os.MkdirAll(protoLocalBinDir, 0755); err != nil {
			return err
		}

		if err := os.WriteFile(filepath.Join(protoLocalBinDir, "default"),
			[]byte("/usr/local/bin/LAUNCH"), 0755); err != nil {
			return err
		}

		launchPath := filepath.Join(protoLocalBinDir, "LAUNCH")
		return copyFromTemp(config, "LAUNCH-multicall.rootfs.entrypoint", launchPath, 0755)
	}

	return nil
}

func createEntrypoint(config Config) error {
	return os.WriteFile(filepath.Join(config.AppDir, "entrypoint"), []byte(config.Entrypoint), 0755)
}

func handleDesktopFile(config Config) error {
	desktopFilePath := filepath.Join(config.AppDir, "proto", "usr", "share", "applications", config.Entrypoint)
	if _, err := os.Stat(desktopFilePath); os.IsNotExist(err) {
		return fmt.Errorf("desktop file not found: %s", desktopFilePath)
	}

	appDirDesktopPath := filepath.Join(config.AppDir, config.Entrypoint)
	//if err := os.Symlink(filepath.Join("proto", "usr", "share", "applications", config.Entrypoint), appDirDesktopPath); err != nil {
	//	// Fallback to copy if symlink fails
		if err := copyFile(desktopFilePath, appDirDesktopPath); err != nil {
			return fmt.Errorf("failed to link/copy desktop file: %v", err)
		}
	//}

	desktopContent, err := os.ReadFile(appDirDesktopPath)
	if err != nil {
		return err
	}

	var iconName, executable string
	for _, line := range strings.Split(string(desktopContent), "\n") {
		if strings.HasPrefix(line, "Icon=") {
			iconName = strings.TrimPrefix(line, "Icon=")
		} else if strings.HasPrefix(line, "Exec=") {
			execParts := strings.SplitN(strings.TrimPrefix(line, "Exec="), " ", 2)
			executable = execParts[0]
		}
	}

	if executable == "" {
		return fmt.Errorf("no Exec entry in desktop file")
	}

	newConfig := config
	newConfig.Entrypoint = executable
	if err := createEntrypoint(newConfig); err != nil {
		return err
	}

	if iconName != "" {
		if err := findAndCopyIcon(config.AppDir, iconName); err != nil {
			log.Printf("Warning: Failed to handle icon: %v", err)
		}
	}

	return nil
}

func findAndCopyIcon(appDir, iconName string) error {
	iconDirs := []string{
		filepath.Join(appDir, "proto", "usr", "share", "icons"),
		filepath.Join(appDir, "proto", "usr", "share", "icons", "hicolor"),
	}

	var bestPngIcon string

	for _, iconDir := range iconDirs {
		filepath.Walk(iconDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			for _, badDir := range []string{"/16x16/", "/24x24/", "/32x32/", "/48x48/", "/64x64/", "/96x96/"} {
				if strings.Contains(path, badDir) {
					return nil
				}
			}

			fileName := filepath.Base(path)
			if (strings.HasPrefix(fileName, iconName+".") || strings.HasPrefix(fileName, iconName+"-")) &&
				strings.HasSuffix(fileName, ".png") {
				bestPngIcon = path
				return filepath.SkipDir
			}
			return nil
		})
	}

	if bestPngIcon != "" {
		if err := copyFile(bestPngIcon, filepath.Join(appDir, ".DirIcon")); err != nil {
			return fmt.Errorf("failed to copy PNG icon: %v", err)
		}
	}

	for _, iconDir := range iconDirs {
		filepath.Walk(iconDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}

			fileName := filepath.Base(path)
			if (strings.HasPrefix(fileName, iconName+".") || strings.HasPrefix(fileName, iconName+"-")) &&
				strings.HasSuffix(fileName, ".svg") {
				if err := copyFile(path, filepath.Join(appDir, ".DirIcon.svg")); err != nil {
					log.Printf("Failed to copy SVG icon: %v", err)
				}
				return filepath.SkipDir
			}
			return nil
		})
	}

	return nil
}

func setupLib4bin(config Config) error {
	l4bCmdPath := filepath.Join(config.AppDir, ".l4bCmd")
	script := fmt.Sprintf(`#!/bin/sh
export PATH="%s:%s"
export LD_LIBRARY_PATH="%s/proto/lib:%s/proto/usr/lib:%s/proto/lib64:%s/proto/usr/lib64:%s/proto/lib32:%s/proto/usr/lib32"
sharun l --with-sharun --gen-lib-path --with-hooks --dst-dir "%s" %s
`, config.TempDir, os.Getenv("PATH"),
		config.AppDir, config.AppDir, config.AppDir, config.AppDir, config.AppDir, config.AppDir,
		config.AppDir, config.Lib4binArgs,
	)
	if err := os.WriteFile(l4bCmdPath, []byte(script), 0755); err != nil {
		return err
	}

	cmd := exec.Command(l4bCmdPath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func trimProtoDir(config Config) error {
	protoTrimmedDir := filepath.Join(config.AppDir, "proto_trimmed")
	if err := os.MkdirAll(protoTrimmedDir, 0755); err != nil {
		return err
	}

	excludedFiles := strings.Fields(config.GetridFiles)
	for _, item := range strings.Fields(config.ToBeKeptFiles) {
		keep := true
		for _, excluded := range excludedFiles {
			if item == excluded {
				keep = false
				break
			}
		}

		if keep {
			sourcePath := filepath.Join(config.AppDir, "proto", item)
			destPath := filepath.Join(protoTrimmedDir, item)

			if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
				return err
			}

			if err := copyPath(sourcePath, destPath); err != nil {
				return fmt.Errorf("failed to copy %s: %v", item, err)
			}
		}
	}

	if err := os.RemoveAll(filepath.Join(config.AppDir, "proto")); err != nil {
		return err
	}

	return os.Rename(protoTrimmedDir, filepath.Join(config.AppDir, "proto"))
}

func tidyUp(config Config) error {
	protoDir := filepath.Join(config.AppDir, "proto")
	if _, err := os.Stat(protoDir); os.IsNotExist(err) {
		return nil
	}

	// Remove specified files
	for _, excluded := range strings.Fields(config.GetridFiles) {
		excludedPath := filepath.Join(protoDir, excluded)
		if _, err := os.Stat(excludedPath); err == nil {
			if err := os.RemoveAll(excludedPath); err != nil {
				return err
			}
		}
	}

	// Create required directories
	if err := os.MkdirAll(filepath.Join(protoDir, "app"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(protoDir, "host"), 0755); err != nil {
		return err
	}

	// Remove standard files
	filesToRemove := []string{
		"etc/machine-id", "etc/resolv.conf", "etc/passwd", "etc/group", "etc/hostname",
		"etc/localtime", "__w", "github",
	}

	for _, file := range filesToRemove {
		if err := os.RemoveAll(filepath.Join(protoDir, file)); err != nil {
			return err
		}
	}

	if config.Sandbox {
		if err := setupSandboxFiles(protoDir); err != nil {
			return err
		}
	}

	return nil
}

func setupSandboxFiles(protoDir string) error {
	// Create font directories
	if err := os.MkdirAll(filepath.Join(protoDir, "usr", "share", "fonts"), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(protoDir, "usr", "share", "fontconfig"), 0755); err != nil {
		return err
	}

	// Touch required files
	filesToTouch := []string{
		"etc/machine-id", "etc/hostname", "etc/localtime", "etc/passwd", "etc/group",
		"etc/hosts", "etc/nsswitch.conf", "etc/resolv.conf", "etc/asound.conf",
	}

	for _, file := range filesToTouch {
		if err := os.WriteFile(filepath.Join(protoDir, file), []byte{}, 0644); err != nil {
			return err
		}
	}

	return nil
}

func createBundle(config Config) error {
	if config.OutputTo == "" {
		config.OutputTo = fmt.Sprintf("%s.%s.AppBundle", config.AppBundleID, config.AppBundleFS)
	}

	cmd := exec.Command(filepath.Join(config.TempDir, "pelf"),
		"--add-appdir", config.AppDir,
		"--appbundle-id", config.AppBundleID,
		"--output-to", config.OutputTo)

	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyFile(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dest, data, 0644)
}

func copyPath(src, dest string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath := strings.TrimPrefix(path, src)
		destPath := filepath.Join(dest, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		return copyFile(path, destPath)
	})
}

func unixMachine() string {
	var utsname unix.Utsname
	if err := unix.Uname(&utsname); err != nil {
		return "unknown"
	}
	return string(utsname.Machine[:])
}

func securePath(basePath, relativePath string) (string, error) {
	relativePath = filepath.Clean("/" + relativePath)
	relativePath = strings.TrimPrefix(relativePath, string(os.PathSeparator))
	dstPath := filepath.Join(basePath, relativePath)

	if !strings.HasPrefix(filepath.Clean(dstPath)+string(os.PathSeparator), filepath.Clean(basePath)+string(os.PathSeparator)) {
		return "", fmt.Errorf("illegal file path: %s", dstPath)
	}
	return dstPath, nil
}

func calculateB3Sum(filePath string) (string, error) {
	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}
	hash := blake3.Sum256(data)
	return string(hash[:]), nil
}

func handleFile(f archives.FileInfo, dst string) error {
	dstPath, pathErr := securePath(dst, f.NameInArchive)
	if pathErr != nil {
		return pathErr
	}

	parentDir := filepath.Dir(dstPath)
	if dirErr := os.MkdirAll(parentDir, dirPermissions); dirErr != nil {
		return dirErr
	}

	if f.IsDir() {
		return os.MkdirAll(dstPath, f.Mode())
	}

	if f.LinkTarget != "" {
		return os.Symlink(f.LinkTarget, dstPath)
	}

	if f.Mode()&os.ModeNamedPipe != 0 {
		return syscall.Mkfifo(dstPath, uint32(f.Mode().Perm()))
	}

	if f.Mode()&os.ModeSocket != 0 {
		return syscall.Mknod(dstPath, syscall.S_IFSOCK|0600, 0)
	}

	if _, err := os.Stat(dstPath); err == nil {
		// File exists, compare hashes
		existingHash, err := calculateB3Sum(dstPath)
		if err != nil {
			return fmt.Errorf("failed to calculate hash for existing file: %v", err)
		}

		reader, err := f.Open()
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer reader.Close()

		tempFile, err := os.CreateTemp("", "tempfile")
		if err != nil {
			return fmt.Errorf("create temp file: %w", err)
		}
		defer os.Remove(tempFile.Name())
		defer tempFile.Close()

		_, err = io.Copy(tempFile, reader)
		if err != nil {
			return fmt.Errorf("copy to temp file: %w", err)
		}

		newHash, err := calculateB3Sum(tempFile.Name())
		if err != nil {
			return fmt.Errorf("failed to calculate hash for new file: %v", err)
		}

		if existingHash == newHash {
			// Hashes match, skip the file
			return nil
		}
	}

	// Hashes do not match or file does not exist, overwrite the file
	reader, err := f.Open()
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer reader.Close()

	dstFile, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, f.Mode())
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer dstFile.Close()

	buf := make([]byte, 32*1024) // 32KB buffer
	_, err = io.CopyBuffer(dstFile, reader, buf)
	if err != nil {
		return fmt.Errorf("copy: %w", err)
	}

	return nil
}

func extractToDirectory(tarball, dst string) error {
	archiveFile, openErr := os.Open(tarball)
	if openErr != nil {
		return fmt.Errorf("open tarball %s: %w", tarball, openErr)
	}
	defer archiveFile.Close()

	format, input, identifyErr := archives.Identify(context.Background(), tarball, archiveFile)
	if identifyErr != nil {
		return fmt.Errorf("identify format: %w", identifyErr)
	}

	extractor, ok := format.(archives.Extractor)
	if !ok {
		return fmt.Errorf("unsupported format for extraction")
	}

	if dirErr := os.MkdirAll(dst, dirPermissions); dirErr != nil {
		return fmt.Errorf("creating destination directory: %w", dirErr)
	}

	handler := func(ctx context.Context, f archives.FileInfo) error {
		return handleFile(f, dst)
	}

	if extractErr := extractor.Extract(context.Background(), input, handler); extractErr != nil {
		return fmt.Errorf("extracting files: %w", extractErr)
	}

	return nil
}
