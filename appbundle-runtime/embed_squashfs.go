//go:build !noEmbed && squashfs

package main

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

//go:embed binaryDependencies/squashfuse
var squashfuseBinary []byte

//go:embed binaryDependencies/unsquashfs
var unsquashfsBinary []byte

var Filesystems = []*Filesystem{
	&Filesystem{
		Type:     "squashfs",
		Commands: []string{"squashfuse", "unsquashfs"},
		MountCmd: func(cfg *RuntimeConfig) CommandRunner {
			args := []string{
				"-o", "ro,nodev",
				"-o", "uid=0,gid=0",
				"-o", fmt.Sprintf("offset=%d", cfg.archiveOffset),
				cfg.selfPath,
				cfg.mountDir,
			}
			if os.Getenv("ENABLE_FUSE_DEBUG") != "" {
				logWarning("squashfuse's debug mode implies foreground. The AppRun won't be called.")
				args = append(args, "-o", "debug")
			}
			memitCmd, err := newMemitCmd(cfg, squashfuseBinary, "squashfuse", args...)
			if err != nil {
				logError("Failed to create memit command", err, cfg)
			}
			return memitCmd
		},
		ExtractCmd: func(cfg *RuntimeConfig, query string) CommandRunner {
			args := []string{"-d", cfg.mountDir, "-o", fmt.Sprintf("%d", cfg.archiveOffset), cfg.selfPath}
			if query != "" {
				for _, file := range strings.Split(query, " ") {
					args = append(args, "-e", file)
				}
			}
			memitCmd, err := newMemitCmd(cfg, unsquashfsBinary, "unsquashfs", args...)
			if err != nil {
				logError("Failed to create memit command", err, cfg)
			}
			return memitCmd
		},
	},
}
