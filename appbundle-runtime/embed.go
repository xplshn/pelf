//go:build !noEmbed
package main

import (
	"bytes"
	"io"
	"os"
	"os/exec"

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
func (c *memitCmd) SetStdin(r io.Reader)  {
     c.Cmd.Stdin = r
}
func (c *memitCmd) CombinedOutput() ([]byte, error) {
    return c.Cmd.CombinedOutput()
}
func (c *memitCmd) Run() error {
    defer c.file.Close()
    return c.Cmd.Run()
}

func newMemitCmd(binary []byte, name string, args ...string) (*memitCmd, error) {
    cmd, file, err := memit.Command(bytes.NewReader(binary), args...)
    if err != nil {
        return nil, err
    }
    cmd.Args[0] = name
    return &memitCmd{Cmd: cmd, file: file}, nil
}
