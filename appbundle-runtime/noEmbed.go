//go:build noEmbed

package main

import (
    "fmt"
    "os"
    "os/exec"
    "debug/elf"
    "strings"
    "runtime"
    "io"
    "archive/tar"
    "bytes"
    "path/filepath"

    "github.com/klauspost/compress/zstd"

)

const runtimeEdition = "noEmbed"

type osExecCmd struct {
    *exec.Cmd
}

func (c *osExecCmd) SetStdout(w io.Writer) { c.Cmd.Stdout = w }
func (c *osExecCmd) SetStderr(w io.Writer) { c.Cmd.Stderr = w }
func (c *osExecCmd) SetStdin(r io.Reader)  { c.Cmd.Stdin = r }
func (c *osExecCmd) CombinedOutput() ([]byte, error) { return c.Cmd.CombinedOutput() }

var Filesystems = []*Filesystem{
    {
        Type:     "squashfs",
        Commands: []string{"fusermount", "squashfuse", "unsquashfs"},
        MountCmd: func(cfg *RuntimeConfig) CommandRunner {
            executable, err := lookPath("squashfuse", globalPath)
            if err != nil {
                println(globalPath)
                logError("squashfuse not available", err, cfg)
            }
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
            return &osExecCmd{exec.Command(executable, args...)}
        },
        ExtractCmd: func(cfg *RuntimeConfig, query string) CommandRunner {
            executable, err := lookPath("unsquashfs", globalPath)
            if err != nil {
                logError("unsquashfs not available", err, cfg)
            }
            args := []string{"-d", cfg.mountDir, "-o", fmt.Sprintf("%d", cfg.archiveOffset), cfg.selfPath}
            if query != "" {
                for _, file := range strings.Split(query, " ") {
                    args = append(args, "-e", file)
                }
            }
            return &osExecCmd{exec.Command(executable, args...)}
        },
    },
    {
        Type:     "dwarfs",
        Commands: []string{"fusermount3", "dwarfs", "dwarfsextract"},
        MountCmd: func(cfg *RuntimeConfig) CommandRunner {
            executable, err := lookPath("dwarfs", globalPath)
            if err != nil {
                logError("dwarfs not available", err, cfg)
            }
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
            return &osExecCmd{exec.Command(executable, args...)}
        },
        ExtractCmd: func(cfg *RuntimeConfig, query string) CommandRunner {
            executable, err := lookPath("dwarfsextract", globalPath)
            if err != nil {
                logError("dwarfsextract not available", err, cfg)
            }
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
            return &osExecCmd{exec.Command(executable, args...)}
        },
    },
}

func (f *fileHandler) extractStaticTools(cfg *RuntimeConfig) error {
	elfFile, err := elf.NewFile(f.file)
	if err != nil {
		return fmt.Errorf("parse ELF: %w", err)
	}

	staticToolsSection := elfFile.Section(".pbundle_static_tools")
	if staticToolsSection == nil {
		return fmt.Errorf("static_tools section not found")
	}

	staticToolsData, err := staticToolsSection.Data()
	if err != nil {
		return fmt.Errorf("failed to read static_tools section: %w", err)
	}

	decoder, err := zstd.NewReader(bytes.NewReader(staticToolsData))
	if err != nil {
		return fmt.Errorf("zstd init: %w", err)
	}
	defer decoder.Close()

	sizeCache := make(map[string]int64)
	err = filepath.Walk(cfg.staticToolsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			relPath, err := filepath.Rel(cfg.staticToolsDir, path)
			if err != nil {
				return err
			}
			sizeCache[relPath] = info.Size()
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to cache file sizes: %w", err)
	}

	tr := tar.NewReader(decoder)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("tar read: %w", err)
		}

		fpath := filepath.Join(cfg.staticToolsDir, hdr.Name)
		relPath, err := filepath.Rel(cfg.staticToolsDir, fpath)
		if err != nil {
			return fmt.Errorf("failed to get relative path: %w", err)
		}

		if _, exists := sizeCache[relPath]; exists {
			continue
		}

		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(fpath, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", fpath, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			f, err := os.OpenFile(fpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return fmt.Errorf("create: %w", err)
			}
			_, err = io.Copy(f, tr)
			f.Close()
			if err != nil {
				return fmt.Errorf("write: %w", err)
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			if err := os.Symlink(hdr.Linkname, fpath); err != nil {
				return fmt.Errorf("symlink: %w", err)
			}
		case tar.TypeLink:
			if err := os.MkdirAll(filepath.Dir(fpath), 0755); err != nil {
				return fmt.Errorf("mkdir parent: %w", err)
			}
			if err := os.Link(hdr.Linkname, fpath); err != nil {
				return fmt.Errorf("hardlink: %w", err)
			}
		}
	}

	return nil
}

func checkDeps(cfg *RuntimeConfig, fh *fileHandler) (*Filesystem, error) {
    fs, ok := getFilesystem(cfg.appBundleFS)
    if !ok {
        return nil, fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
    }

    updatePath("PATH", cfg.staticToolsDir)
    var missingCmd bool
    for _, cmd := range fs.Commands {
        if _, err := lookPath(cmd, globalPath); err != nil {
            missingCmd = true
            break
        }
    }

    if missingCmd {
        if err := os.MkdirAll(cfg.staticToolsDir, 0755); err != nil {
            return nil, fmt.Errorf("failed to create static tools directory: %v", err)
        }

        if err := fh.extractStaticTools(cfg); err != nil {
            return nil, fmt.Errorf("failed to extract static tools: %v", err)
        }
    }

    return fs, nil
}
