//go:build !noEmbed
package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/liamg/memit"
)

type memitCmd struct {
	*exec.Cmd
	file *os.File
}

func (c *memitCmd) SetStdout(w io.Writer) {
	c.Cmd.Stdout = w
}
func (c *memitCmd) SetStderr(w io.Writer) {
	c.Cmd.Stderr = w
}
func (c *memitCmd) SetStdin(r io.Reader) {
	c.Cmd.Stdin = r
}
func (c *memitCmd) CombinedOutput() ([]byte, error) {
	return c.Cmd.CombinedOutput()
}
func (c *memitCmd) Run() error {
	defer c.file.Close()
	return c.Cmd.Run()
}

func newMemitCmd(cfg *RuntimeConfig, binary []byte, name string, args ...string) (*memitCmd, error) {
	if os.Getenv("NO_MEMFDEXEC") == "1" {
		tempDir := filepath.Join(cfg.workDir, ".static")
		if err := os.MkdirAll(tempDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create temporary directory: %v", err)
		}
		tempFile := filepath.Join(tempDir, name)
		if err := os.WriteFile(tempFile, binary, 0755); err != nil {
			return nil, fmt.Errorf("failed to write temporary file: %v", err)
		}
		cmd := exec.Command(tempFile, args...)
		return &memitCmd{Cmd: cmd}, nil
	}
	cmd, file, err := memit.Command(bytes.NewReader(binary), args...)
	if err != nil {
		return nil, err
	}
	cmd.Args[0] = name
	return &memitCmd{Cmd: cmd, file: file}, nil
}

func checkDeps(cfg *RuntimeConfig, fh *fileHandler) (*Filesystem, error) {
	fs, ok := getFilesystem(cfg.appBundleFS)
	if !ok {
		return nil, fmt.Errorf("unsupported filesystem: %s", cfg.appBundleFS)
	}
	for _, cmd := range fs.Commands {
		if _, err := lookPath(cmd, globalPath); err != nil {
			return nil, fmt.Errorf("system command %s not found", cmd)
		}
	}
	return fs, nil
}
