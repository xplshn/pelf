//go:build !noEmbed && !squashfs

package main

import (
    _ "embed"
    "fmt"
    "os"
    "strings"
    "runtime"
)

//go:embed binaryDependencies/dwarfs
var dwarfsBinary []byte

var Filesystems = []*Filesystem{
    &Filesystem{
        Type:     "dwarfs",
        Commands: []string{"fusermount3", "dwarfs", "dwarfsextract"},
        MountCmd: func(cfg *RuntimeConfig) CommandRunner {
            args := []string{
                "-o", "ro,nodev",
                "-o", "cache_files,no_cache_image,clone_fd",
                "-o", "block_allocator="+getEnvWithDefault("DWARFS_BLOCK_ALLOCATOR", DWARFS_BLOCK_ALLOCATOR),
                "-o", getEnvWithDefault("DWARFS_TIDY_STRATEGY", DWARFS_TIDY_STRATEGY),
                "-o", "debuglevel="+T(os.Getenv("ENABLE_FUSE_DEBUG") != "", "debug", "error"),
                "-o", "cachesize="+getEnvWithDefault("DWARFS_CACHESIZE", DWARFS_CACHESIZE),
                "-o", "readahead="+getEnvWithDefault("DWARFS_READAHEAD", DWARFS_READAHEAD),
                "-o", "blocksize="+getEnvWithDefault("DWARFS_BLOCKSIZE", DWARFS_BLOCKSIZE),
                "-o", fmt.Sprintf("workers=%d", getEnvWithDefault("DWARFS_WORKERS", runtime.NumCPU())),
                "-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
                cfg.selfPath,
                cfg.mountDir,
            }
            if e := os.Getenv("DWARFS_ANALYSIS_FILE"); e != "" {
                args = append(args, "-o", "analysis_file="+e)
            }
            if e := os.Getenv("DWARFS_PRELOAD_ALL"); e != "" {
                args = append(args, "-o", "preload_all")
            } else {
                args = append(args, "-o", "preload_category=hotness")
            }
            memitCmd, err := newMemitCmd(dwarfsBinary, "dwarfs", args...)
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
            memitCmd, err := newMemitCmd(dwarfsBinary, "dwarfsextract", args...)
            if err != nil {
                logError("Failed to create memit command", err, cfg)
            }
            return memitCmd
        },
    },
}

func checkDeps(cfg *RuntimeConfig, fh *fileHandler) (*Filesystem, error) {
    fs, ok := getFilesystem(cfg.appBundleFS)
    if !ok {
        return nil, fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
    }
    for _, cmd := range fs.Commands {
        if cmd == "fusermount" || cmd == "fusermount3" {
            if _, err := lookPath(cmd, globalPath); err != nil {
                return nil, fmt.Errorf("system command %s not found", cmd)
            }
        }
    }
    return fs, nil
}
