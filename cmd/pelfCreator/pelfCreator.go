//TODO: Cleanup
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
	"slices"

	"github.com/xplshn/pelf/pkg/utils"

	"github.com/go-ini/ini"
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
	// colors
	warningColor = "\x1b[0;33m"
	errorColor   = "\x1b[0;31m"
	blueColor    = "\x1b[0;34m"
	resetColor   = "\x1b[0m"
	warning      = warningColor + "warning" + resetColor + ":"
)

type Config struct {
	Maintainer          string
	Name                string
	AppBundleID         string
	AppStreamID         string
	PkgAdd              string
	Entrypoint          string
	DontPack            bool
	Sharun              bool
	Sandbox             bool
	PreservePermissions bool
	Lib4binArgs         string
	ToBeKeptFiles       string
	GetridFiles         string
	AppBundleFS         string
	OutputTo            string
	LocalResources      string
	AppDir              string
	Date                string
	TempDir             string
}

// AppBundleIDHandler handles AppBundleID generation and validation
type AppBundleIDHandler struct {
	config *Config
}
func NewAppBundleIDHandler(config *Config) *AppBundleIDHandler {
	return &AppBundleIDHandler{config: config}
}
func (h *AppBundleIDHandler) ProcessAppBundleID() error {
	if h.config.AppBundleID == "" {
		return h.generateDefaultAppBundleID()
	}
	return h.validateProvidedAppBundleID()
}
func (h *AppBundleIDHandler) generateDefaultAppBundleID() error {
	name := h.config.AppStreamID
	if name == "" {
		name = h.config.Name
	}
	id := utils.AppBundleID{
		Name: name,
		Repo: h.config.Maintainer,
		Date: parseTime(h.config.Date),
	}
	h.config.AppBundleID = id.String()
	return nil
}
func (h *AppBundleIDHandler) validateProvidedAppBundleID() error {
	appBundleID, t, err := utils.ParseAppBundleID(h.config.AppBundleID)
	if err != nil {
		return fmt.Errorf("invalid AppBundleID: %v", err)
	}
	if t == utils.TypeI {
		fmt.Fprintf(os.Stderr, "%s AppBundleID is type I (%s). Recommended format is type II or type III, while type I shall only be used for filenames. Example: 'name#repo[:version][@date]'\n", warning, appBundleID.Raw)
	}
	if h.config.AppStreamID != "" {
		return fmt.Errorf("do not provide an AppBundleID and an AppStreamID at the same time. You can just put the AppStreamID as the name in the AppBundleID")
	}
	h.config.AppBundleID = appBundleID.String()
	return nil
}
func (h *AppBundleIDHandler) GenerateOutputFilename() error {
	appBundleID, _, err := utils.ParseAppBundleID(h.config.AppBundleID)
	if err != nil {
		return fmt.Errorf("invalid AppBundleID format: %v", err)
	}

	appBundleID.Name = h.config.Name

	if _, err := appBundleID.Compliant(); err != nil {
		return fmt.Errorf("AppBundleID must be in a valid format when --output-to is not provided: %v", err)
	}

	name, err := appBundleID.Format(utils.TypeI)
	if err != nil {
		return fmt.Errorf("cannot generate output filename: %v", err)
	}

	h.config.AppDir = fmt.Sprintf("%s.AppDir", name)
	if h.config.OutputTo == "" {
		h.config.OutputTo = fmt.Sprintf("%s.%s.AppBundle", name, h.config.AppBundleFS)
	}
	return nil
}

// NameResolver handles name resolution logic
type NameResolver struct {
	config *Config
}

func NewNameResolver(config *Config) *NameResolver {
	return &NameResolver{config: config}
}

func (r *NameResolver) ResolveName() error {
	if r.config.Name == "" && r.config.AppStreamID == "" {
		return fmt.Errorf("either --name/-n or --appstream-id/-a must be provided")
	}

	if r.config.Name == "" {
		r.config.Name = utils.AppStreamIDToName(r.config.AppStreamID)
	}

	return nil
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
				Required:    false,
				Destination: &config.Name,
			},
			&cli.StringFlag{
				Name:        "appstream-id",
				Aliases:     []string{"a"},
				Usage:       "Set the appstream ID of the app",
				Required:    false,
				Destination: &config.AppStreamID,
			},
			&cli.StringFlag{
				Name:        "appbundle-id",
				Aliases:     []string{"i"},
				Usage:       "Set the AppBundleID of the app (optional, format: name#repo[@date])",
				Destination: &config.AppBundleID,
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
				Usage:       "Set the output file name (optional, default: name#repo:version@date.dwfs.AppBundle or name-date-maintainer.dwfs.AppBundle)",
				Destination: &config.OutputTo,
			},
			&cli.StringFlag{
				Name:        "local",
				Usage:       "A directory from which to pick up files such as 'AppRun.sharun', 'rootfs.tgz', 'pelf', 'bwrap', etc",
				Sources:     cli.EnvVars("PELFCREATOR_RESOURCES"),
				Destination: &config.LocalResources,
			},
			&cli.BoolFlag{
				Name:        "preserve-rootfs-permissions",
				Usage:       "Preserve the original permissions from the rootfs",
				Destination: &config.PreservePermissions,
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
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return runPelfCreator(config)
		},
	}

	if err := app.Run(context.Background(), os.Args); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func runPelfCreator(config Config) error {
	// Initialize configuration
	config.Date = time.Now().Format("20060102")

	// Resolve name
	nameResolver := NewNameResolver(&config)
	if err := nameResolver.ResolveName(); err != nil {
		return err
	}

	// Handle AppBundleID processing
	idHandler := NewAppBundleIDHandler(&config)
	if err := idHandler.ProcessAppBundleID(); err != nil {
		return err
	}

	// Configure Sharun if needed
	if config.Lib4binArgs != "" {
		config.Sharun = true
		config.Lib4binArgs = prepareSharunArgs(config)
	}

	// Setup temp directory
	var err error
	config.TempDir, err = os.MkdirTemp("", "pelfCreator-deps")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(config.TempDir)

	// Handle local resources
	if err := handleLocalResources(&config); err != nil {
		return err
	}

	// Generate output filename
	if err := idHandler.GenerateOutputFilename(); err != nil {
		return err
	}

	// Create AppDir structure
	if err := createAppDirStructure(config); err != nil {
		return err
	}

	// Setup dependencies
	if err := setupDependencies(config); err != nil {
		return err
	}

	// Extract rootfs
	if err := extractRootfs(config); err != nil {
		return err
	}

	// Setup AppRun and packages
	if err := setupAppRunAndPackages(config); err != nil {
		return err
	}

	// Handle entrypoint
	if err := handleEntrypoint(config); err != nil {
		return err
	}

	// Setup execution mode
	if err := setupExecutionMode(config); err != nil {
		return err
	}

	// Cleanup
	if err := tidyUp(config); err != nil {
		return fmt.Errorf("cleanup failed: %v", err)
	}

	// Create bundle
	if !config.DontPack {
		if err := createBundle(config); err != nil {
			return fmt.Errorf("bundle creation failed: %v", err)
		}
	}

	fmt.Printf("Successfully created %s\n", config.OutputTo)
	return nil
}

func prepareSharunArgs(config Config) string {
	parts := strings.Fields(config.Lib4binArgs)
	for i, part := range parts {
		parts[i] = filepath.Join(config.AppDir, "proto", part)
	}
	return strings.Join(parts, " ")
}

func handleLocalResources(config *Config) error {
	if config.LocalResources != "" && isArchive(config.LocalResources) {
		fileContent, err := os.ReadFile(config.LocalResources)
		if err != nil {
			return fmt.Errorf("failed to read local archive: %v", err)
		}
		binaryDependencies = fileContent
	}
	return nil
}

func createAppDirStructure(config Config) error {
	protoDir := filepath.Join(config.AppDir, "proto")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return fmt.Errorf("failed to create proto directory: %v", err)
	}

	genStepsPath := filepath.Join(config.AppDir, ".genSteps")
	genStepsContent := fmt.Sprintf("pelfCreator %s", strings.Join(os.Args[1:], " "))
	if err := os.WriteFile(genStepsPath, []byte(genStepsContent), 0755); err != nil {
		return fmt.Errorf("failed to create .genSteps: %v", err)
	}

	return nil
}

func extractRootfs(config Config) error {
	rootfsPath, err := findRootfs(config)
	if err != nil {
		return err
	}

	protoDir := filepath.Join(config.AppDir, "proto")
	if err := extractToDirectory(rootfsPath, protoDir, &config); err != nil {
		return fmt.Errorf("failed to extract rootfs: %v", err)
	}

	return nil
}

func handleEntrypoint(config Config) error {
	if config.Entrypoint == "" {
		return nil
	}

	if err := createEntrypoint(config); err != nil {
		return fmt.Errorf("entrypoint creation failed: %v", err)
	}

	if strings.HasSuffix(config.Entrypoint, ".desktop") {
		if err := handleDesktopFile(config); err != nil {
			return fmt.Errorf("failed to handle desktop file: %v", err)
		}
	}

	return nil
}

func setupExecutionMode(config Config) error {
	switch {
	case config.Sandbox:
		return setupSandboxMode(config)
	case config.Sharun:
		return setupSharunMode(config)
	default:
		return setupDefaultMode(config)
	}
}

func setupSandboxMode(config Config) error {
	if err := copyFromTemp(config, "bwrap", filepath.Join(config.AppDir, "usr/bin/bwrap"), 0755); err != nil {
		return fmt.Errorf("bwrap setup failed: %v", err)
	}
	protoDir := filepath.Join(config.AppDir, "proto")
	if err := setupSandboxFiles(protoDir); err != nil {
		return err
	}
	return copyFromTemp(config, "AppRun.rootfs-based", filepath.Join(config.AppDir, "AppRun"), 0755)
}

func setupSharunMode(config Config) error {
	// Process binaries with lib4bin
	if err := setupLib4bin(config); err != nil {
		return err
	}

	// Handle proto directory based on keep/getrid flags
	if config.ToBeKeptFiles != "" || config.GetridFiles != "" {
		if err := trimProtoDir(config); err != nil {
			return err
		}
		if err := copyFromTemp(config, "AppRun.sharun.ovfsProto", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
			return err
		}
		if err := copyFromTemp(config, "unionfs", filepath.Join(config.AppDir, "usr", "bin", "unionfs"), 0755); err != nil {
			return err
		}
		if err := copyFromTemp(config, "bwrap", filepath.Join(config.AppDir, "usr", "bin", "bwrap"), 0755); err != nil {
			return err
		}
	} else {
		// Pure Sharun mode - remove proto dir completely
		if err := os.RemoveAll(filepath.Join(config.AppDir, "proto")); err != nil {
			return err
		}
		if err := copyFromTemp(config, "AppRun.sharun", filepath.Join(config.AppDir, "AppRun"), 0755); err != nil {
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

	if err := extractToDirectory(tempArchive, config.TempDir, &config); err != nil {
		return fmt.Errorf("failed to extract binaryDependencies: %v", err)
	}

	return nil
}

func findRootfs(config Config) (string, error) {
	if config.LocalResources != "" {
		localRootfs, err := findFirstMatch(config.LocalResources, "rootfs.tar*")
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
	if config.LocalResources != "" {
		localSrcPath := filepath.Join(config.LocalResources, srcRelPath)
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

	// Run pkgadd.sh
	cmd := exec.Command(filepath.Join(config.AppDir, "AppRun"), "--Xbwrap", "--uid", "0", "--gid", "0", "--cap-add CAP_SYS_CHROOT", "--", "/app/pkgadd.sh", config.PkgAdd)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run pkgadd.sh: %v", err)
	}

	// Check for .version file and update AppBundleID and OutputTo if it exists
	versionFile := filepath.Join(config.AppDir, ".version")
	if versionData, err := os.ReadFile(versionFile); err == nil {
		version := strings.TrimSpace(string(versionData))
		if version != "" {
			// Parse the existing AppBundleID
			appBundleID, _, err := utils.ParseAppBundleID(config.AppBundleID)
			if err != nil {
				return fmt.Errorf("failed to parse AppBundleID after pkgadd: %v", err)
			}
			// Update to the new format: name#repo:version[@date]
			newAppBundleID := &utils.AppBundleID{
				Name:    appBundleID.Name,
				Repo:    appBundleID.Repo,
				Version: version,
				Date:    appBundleID.Date,
			}
			config.AppBundleID = newAppBundleID.String()
		}
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
	return os.WriteFile(filepath.Join(config.AppDir, "entrypoint"), []byte(config.Entrypoint+"\n"), 0755)
}

func handleDesktopFile(config Config) error {
	desktopFilePath := filepath.Join(config.AppDir, "proto", "usr", "share", "applications", config.Entrypoint)
	if _, err := os.Stat(desktopFilePath); os.IsNotExist(err) {
		return fmt.Errorf("desktop file not found: %s", desktopFilePath)
	}

	appDirDesktopPath := filepath.Join(config.AppDir, config.Entrypoint)
	if err := copyFile(desktopFilePath, appDirDesktopPath); err != nil {
		return fmt.Errorf("failed to copy desktop file: %v", err)
	}

	// Parse the desktop file using ini library
	cfg, err := ini.Load(appDirDesktopPath)
	if err != nil {
		return fmt.Errorf("failed to parse desktop file: %v", err)
	}

	// Get the [Desktop Entry] section
	section, err := cfg.GetSection("Desktop Entry")
	if err != nil {
		return fmt.Errorf("no [Desktop Entry] section in desktop file: %v", err)
	}

	// Extract the Exec entry
	executable := section.Key("Exec").String()
	if executable == "" {
		return fmt.Errorf("no Exec entry in desktop file")
	}
	// Take the first part of Exec (before arguments)
	execParts := strings.Fields(executable)
	if len(execParts) == 0 {
		return fmt.Errorf("invalid Exec entry in desktop file")
	}
	executable = execParts[0]

	// Extract the Icon entry
	iconName := section.Key("Icon").String()

	// Update entrypoint with the executable
	newConfig := config
	newConfig.Entrypoint = executable
	if err := createEntrypoint(newConfig); err != nil {
		return err
	}

	// Handle icon if present
	if iconName != "" {
		if err := findAndCopyIcon(config.AppDir, iconName); err != nil {
			log.Printf("%s failed to handle icon: %v", warning, err)
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

	script := "#!/bin/sh\n"
	script += "PATH=\"%s:$PATH\"\n"
	script += "\"%s/AppRun\" --Xbwrap --gid 0 --uid 0 sharun l --with-sharun --gen-lib-path --with-hooks --dst-dir /app %s\n"
	script = fmt.Sprintf(script, config.TempDir, config.AppDir, config.Lib4binArgs)

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
		if slices.Contains(excludedFiles, item) {
			keep = false
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
	dirsToCreate := []string{
		"Users",
	}
	for _, file := range filesToTouch {
		if err := os.WriteFile(filepath.Join(protoDir, file), []byte{}, 0644); err != nil {
			log.Printf("Unable to create empty file: %v\n", err)
		}
	}
	for _, dir := range dirsToCreate {
		if err := os.MkdirAll(filepath.Join(protoDir, dir), 0644); err != nil {
			log.Printf("Unable to create empty directory: %v\n", err)
		}
	}

	return nil
}

func createBundle(config Config) error {
	cmd := exec.Command(filepath.Join(config.TempDir, "pelf"),
		"--add-appdir", config.AppDir,
		"--appbundle-id", config.AppBundleID,
		"--output-to", config.OutputTo,
	)

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

func isArchive(filePath string) bool {
	// Check if the file is an archive by attempting to identify its format
	archiveFile, err := os.Open(filePath)
	if err != nil {
		return false
	}
	defer archiveFile.Close()

	_, _, identifyErr := archives.Identify(context.Background(), filePath, archiveFile)
	return identifyErr == nil
}

func handleFile(f archives.FileInfo, dst string, config *Config) error {
	dstPath, pathErr := securePath(dst, f.NameInArchive)
	if pathErr != nil {
		return pathErr
	}

	parentDir := filepath.Dir(dstPath)
	if dirErr := os.MkdirAll(parentDir, dirPermissions); dirErr != nil {
		return dirErr
	}

	mode := f.Mode()
	if !config.PreservePermissions {
		if f.IsDir() {
			mode = dirPermissions
		} else {
			mode = filePermissions
		}
	}

	if f.IsDir() {
		return os.MkdirAll(dstPath, mode)
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

func extractToDirectory(tarball, dst string, config *Config) error {
	archiveFile, openErr := os.Open(tarball)
	if openErr != nil {
		return fmt.Errorf("open tarball %s: %w", tarball, openErr)
	}
	defer archiveFile.Close()

	var format archives.Format
	var input io.Reader
	var identifyErr error
	format, input, identifyErr = archives.Identify(context.Background(), tarball, archiveFile)
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
		return handleFile(f, dst, config)
	}

	if extractErr := extractor.Extract(context.Background(), input, handler); extractErr != nil {
		return fmt.Errorf("extracting files: %w", extractErr)
	}

	return nil
}

func parseTime(s string) *time.Time {
	tm, err := time.Parse(utils.TimeLayoutYYYYMMDD, s)
	if err != nil {
		log.Fatalf("Failed to parse time %q: %v", s, err)
	}
	return &tm
}

func T[T any](cond bool, vtrue, vfalse T) T {
	if cond {
		return vtrue
	}
	return vfalse
}
