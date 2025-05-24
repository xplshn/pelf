//go:build !noEmbed && !squashfs

package main

import (
	_ "embed"
	"fmt"
	"strings"
)

//go:embed binaryDependencies/dwarfs
var dwarfsBinary []byte

var Filesystems = []*Filesystem{
	&Filesystem{
		Type:     "dwarfs",
		Commands: []string{"dwarfs", "dwarfsextract"},
		MountCmd: func(cfg *RuntimeConfig) CommandRunner {
			cacheSize := getDwarfsCacheSize()
			args := []string{
				"-o", "ro,nodev",
				"-o", "cache_files,no_cache_image,clone_fd",
				"-o", "block_allocator=" + getEnvWithDefault(globalEnv, "DWARFS_BLOCK_ALLOCATOR", DWARFS_BLOCK_ALLOCATOR),
				"-o", getEnvWithDefault(globalEnv, "DWARFS_TIDY_STRATEGY", DWARFS_TIDY_STRATEGY),
				"-o", "debuglevel=" + T(getEnv(globalEnv, "ENABLE_FUSE_DEBUG") != "", "debug", "error"),
				"-o", "readahead=" + getEnvWithDefault(globalEnv, "DWARFS_READAHEAD", DWARFS_READAHEAD),
				"-o", "blocksize=" + getEnvWithDefault(globalEnv, "DWARFS_BLOCKSIZE", DWARFS_BLOCKSIZE),
				"-o", "cachesize=" + cacheSize,
				"-o", "workers=" + getDwarfsWorkers(&cacheSize),
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				cfg.selfPath,
				cfg.mountDir,
			}
			if e := getEnv(globalEnv, "DWARFS_ANALYSIS_FILE"); e != "" {
				args = append(args, "-o", "analysis_file="+e)
			}
			if e := getEnv(globalEnv, "DWARFS_PRELOAD_ALL"); e != "" {
				args = append(args, "-o", "preload_all")
			} else {
				args = append(args, "-o", "preload_category=hotness")
			}
			memitCmd, err := newMemitCmd(cfg, dwarfsBinary, "dwarfs", args...)
			if err != nil {
				logError("Failed to create memit command", err, cfg)
			}
			return memitCmd
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) CommandRunner {
			args := []string{
				"--input", cfg.selfPath,
				"--image-offset", fmt.Sprintf("%d", cfg.archiveOffset),
				"--output", cfg.mountDir,
			}
			if query != "" {
				for _, pattern := range strings.Split(query, " ") {
					args = append(args, "--pattern", pattern)
				}
			}
			memitCmd, err := newMemitCmd(cfg, dwarfsBinary, "dwarfsextract", args...)
			if err != nil {
				logError("Failed to create memit command", err, cfg)
			}
			return memitCmd
		},
	},
}

