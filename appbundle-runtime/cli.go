package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func handleRuntimeFlags(fh *fileHandler, args *[]string, cfg *RuntimeConfig) error {
	switch (*args)[0] {
	case "--pbundle_help":
		fmt.Printf("This bundle was generated automatically by PELF %s, the machine on which it was created has the following \"uname -mrsp(v)\":\n %s\n\n", cfg.pelfVersion, cfg.pelfHost)
		fmt.Printf("  Internal variables:\n")
		fmt.Printf("  cfg.exeName: %s%s%s\n", blueColor, cfg.exeName, resetColor)
		fmt.Printf("  cfg.rExeName: %s%s%s\n", blueColor, cfg.rExeName, resetColor)
		fmt.Printf("  cfg.mountDir: %s%s%s\n", blueColor, cfg.mountDir, resetColor)
		fmt.Printf("  cfg.workDir: %s%s%s\n", blueColor, cfg.workDir, resetColor)
		fmt.Printf("  cfg.appBundleFS: %s%s%s\n", blueColor, cfg.appBundleFS, resetColor)
		fmt.Printf(`
  Flags:
  --pbundle_help: Needs no introduction
  --pbundle_list: List the contens of the AppBundle (including the static files that aren't part of the AppDir)
  --pbundle_link <binary>: Executes a given command, while leveraging the env variables of the AppBundle, including $PATH
                           You can use this flag to execute commands within the AppBundle
                           example: --pbundle_link sh -c "ls \$SELF_TEMPDIR" ; It'd output the contents of this AppBundle's AppDir
  --pbundle_pngIcon: Sends to stdout the base64 encoded .DirIcon, exits with error number 1 if the .DirIcon does not exist
  --pbundle_svgIcon: Sends to stdout the base64 encoded .DirIcon.svg, exits with error number 1 if the .DirIcon does not exist
  --pbundle_appstream: Same as --pbundle_pngIcon but it uses the first .xml file it encounters on the top level of the AppDir
  --pbundle_desktop: Same as --pbundle_pngIcon but it uses the first .desktop file it encounters on the top level of the AppDir
  --pbundle_portableHome: Creates a directory in the same place as the AppBundle, which will be used as $HOME during subsequent runs
  --pbundle_portableConfig: Creates a directory in the same place as the AppBundle, which will be used as $XDG_CONFIG_HOME during subsequent runs
  --pbundle_cleanup: Unmounts, removes, and tides up the AppBundle's workdir and mount pool. Does not affect other running AppBundles
                     Only affects other instances of this same AppBundle.
`)

	    if cfg.appBundleFS != "dwarfs" {
	    	fmt.Printf("  --pbundle_extract <[]globs>: Extracts the AppBundle's filesystem to ./%s\n", cfg.rExeName + "_" + cfg.appBundleFS)
	    	fmt.Println(`  If globs are provided, it will extract the matching files`)
	    } else {
	    	fmt.Printf("  --pbundle_extract: Extracts the AppBundle's filesystem to ./%s\n", cfg.rExeName + "_" + cfg.appBundleFS)
	    }

		fmt.Printf(`
  Compatibilty flags:
  --appimage-extract: Same as --pbundle_extract but hardcodes the output directory to ./squashfs-root
  --appimage-extract-and-run: Same as --pbundle_extract_and_run but for AppImage compatibility
  --appimage-mount: Same as --pbundle_mount but for AppImage compatibility
  --appimage-offset: Same as --pbundle_offset but for AppImage compatibility

  NOTE: EXE_NAME is the AppBundleID -> rEXE_NAME is the same, but sanitized to be used as a variable name
  NOTE: The -v option in uname may have not been saved, to allow for reproducibility (since uname -v will output the current date)
  NOTE: This runtime is written in Go, it is not the default runtime used by pelf
`)
		return fmt.Errorf("!no_return")

	case "--pbundle_list":
		mountOrExtract(cfg, fh)
		err := filepath.Walk(cfg.workDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			fmt.Println(path)
			return nil
		})
		if err != nil {
			return fmt.Errorf("%v", err)
		}
		return fmt.Errorf("!no_return")

	case "--pbundle_portableHome":
		homeDir := cfg.selfPath + ".home"
		if err := os.MkdirAll(homeDir, 0755); err != nil {
			return err
		}
		return fmt.Errorf("!no_return")

	case "--pbundle_portableConfig":
		configDir := cfg.selfPath + ".config"
		if err := os.MkdirAll(configDir, 0755); err != nil {
			return err
		}
		return fmt.Errorf("!no_return")

	case "--pbundle_link":
		if len(*args) < 2 {
			return fmt.Errorf("missing binary argument for --pbundle_link")
		}
		cfg.entrypoint = (*args)[1]
		*args = (*args)[2:]
		mountOrExtract(cfg, fh)
		_ = executeFile(*args, cfg)
		return fmt.Errorf("!no_return")

	case "--pbundle_pngIcon":
		mountOrExtract(cfg, fh)
		iconPath := cfg.mountDir + "/.DirIcon"
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("PNG icon not found", nil, cfg)

	case "--pbundle_svgIcon":
		mountOrExtract(cfg, fh)
		iconPath := cfg.mountDir + "/.DirIcon.svg"
		if _, err := os.Stat(iconPath); err == nil {
			return encodeFileToBase64(iconPath)
		}
		logError("SVG icon not found", nil, cfg)

	case "--pbundle_desktop":
		mountOrExtract(cfg, fh)
		return findAndEncodeFiles(cfg.mountDir, "*.desktop", cfg)

	case "--pbundle_appstream":
		mountOrExtract(cfg, fh)
		return findAndEncodeFiles(cfg.mountDir, "*.xml", cfg)

	case "--pbundle_extract":
		query := ""
		if len(*args) > 1 {
			query = strings.Join((*args)[1:], " ")
		}
		cfg.mountDir = cfg.rExeName + "_" + cfg.appBundleFS
		if err := extractImage(cfg, fh, query); err != nil {
			return err
		}
		fmt.Println("./" + cfg.mountDir)
		return fmt.Errorf("!no_return")

	case "--appimage-extract":
		query := ""
		if len(*args) > 1 {
			query = strings.Join((*args)[1:], " ")
		}
		cfg.mountDir = "squashfs-root"
		if err := extractImage(cfg, fh, query); err != nil {
			return err
		}
		fmt.Println("./" + cfg.mountDir)
		return fmt.Errorf("!no_return")

	case "--pbundle_extract_and_run", "--appimage-extract-and-run":
		cfg.doNotMount = true
		if err := extractImage(cfg, fh, ""); err != nil {
			return err
		}
		*args = (*args)[1:]
		_ = executeFile(*args, cfg)
		return fmt.Errorf("!no_return")

	case "--pbundle_mount", "--appimage-mount":
		cfg.doNotMount = false
		if err := mountImage(cfg, fh); err != nil {
			return err
		}
		fmt.Println(cfg.mountDir)
		// Is there a better way to idle?
		for {
			time.Sleep(time.Hour)
		}
		return fmt.Errorf("!no_return")

	case "--pbundle_offset", "--appimage-offset":
		fmt.Println(cfg.archiveOffset)
		return fmt.Errorf("!no_return")

	case "--pbundle_cleanup":
		fmt.Println("A cleanup job has been requested...")
		cfg.noCleanup = false
		cleanup(cfg)
		return fmt.Errorf("!no_return")

	default:
		mountOrExtract(cfg, fh)
		_ = executeFile(*args, cfg)
	}

	return nil
}
