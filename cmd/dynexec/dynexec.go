package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const dynexeName = "dynexe"

// Matches any linker name that follows the pattern "ld-linux" or "ld-musl" for x86_64 or aarch64.
var linkerRegexp = regexp.MustCompile(`ld-(linux|musl)-(x86_64|aarch64).so.[0-9]+`)

func matchLinkerName(sharedLib string) string {
	// Check files in the sharedLib directory and match against the linker pattern
	files, err := os.ReadDir(sharedLib)
	if err != nil {
		panic(fmt.Sprintf("failed to read shared library directory: %v", err))
	}

	for _, file := range files {
		if !file.IsDir() && linkerRegexp.MatchString(file.Name()) {
			return file.Name()
		}
	}
	return ""
}

func realpath(path string) string {
	absPath, err := filepath.EvalSymlinks(path)
	if err != nil {
		panic(err)
	}
	return absPath
}

func basename(path string) string {
	return filepath.Base(path)
}

func isFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func main() {
	// Get the executable path
	dynexe, err := os.Executable()
	if err != nil {
		panic(err)
	}
	dynexeDir := filepath.Dir(dynexe)
	lowerDir := filepath.Join(dynexeDir, "../") // TODO, what is this?

	// Check if the parent directory contains the dynexe binary
	if basename(dynexeDir) == "bin" && isFile(filepath.Join(lowerDir, dynexeName)) {
		dynexeDir = realpath(lowerDir)
	}

	// Collect command-line arguments
	execArgs := os.Args
	arg0 := execArgs[0]
	execArgs = execArgs[1:]

	sharedBin := filepath.Join(dynexeDir, "shared/bin")
	sharedLib := filepath.Join(dynexeDir, "shared/lib")

	// Determine the binary name to run
	binName := basename(arg0)
	if binName == dynexeName {
		binName = execArgs[0]
		execArgs = execArgs[1:]
	}
	bin := filepath.Join(sharedBin, binName)

	// Get the linker path by matching against shared lib files using regular expressions
	linkerName := matchLinkerName(sharedLib)
	if linkerName == "" {
		panic(fmt.Sprintf("no valid linker found in %s", sharedLib))
	}
	linker := filepath.Join(sharedLib, linkerName)

	// Prepare arguments for execve
	args := []string{linker, "--library-path", sharedLib, bin}
	args = append(args, execArgs...)

	// Prepare environment variables
	envs := os.Environ()

	// Execute the binary using syscall.Exec (equivalent to userland execve)
	err = syscall.Exec(linker, args, envs)
	if err != nil {
		panic(fmt.Sprintf("failed to execute %s: %v", linker, err))
	}
}
