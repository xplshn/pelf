+++
date = '2025-05-25T00:00:29'
draft = false
title = 'format.md'
[params.author]
  name = 'xplshn'
  email = 'xplshn@murena.io'
+++
# AppBundle File Format Specification

This document outlines the structure and composition of an AppBundle, a self-contained executable format designed to package applications with their dependencies for portable execution on Linux systems.

## File Structure

An AppBundle is a single executable file that combines an ELF (Executable and Linkable Format) runtime with an appended filesystem image containing the application's data. The structure is as follows:

1. **ELF Runtime**:
   - The AppBundle begins with an ELF executable, identifiable by the magic bytes "AB" or optionally "AI" at the start of the file.
   - This runtime is responsible for handling the execution logic, including mounting or extracting the filesystem image and setting up the environment.

2. **Runtime Information Section (.pbundle_runtime_info)**:
   - The ELF file contains a section named `.pbundle_runtime_info`, which stores metadata in CBOR (Concise Binary Object Representation) format.
   - The structure of this section is defined in Go as:
     ```go
     type RuntimeInfo struct {
         AppBundleID          string `json:"AppBundleID"` // Unique identifier for the AppBundle
         PelfVersion          string `json:"PelfVersion"` // Version of the pelf tool used to create the AppBundle
         HostInfo             string `json:"HostInfo"` // System information from `uname -mrsp(v)`
         FilesystemType       string `json:"FilesystemType"` // Filesystem type: "dwarfs" or "squashfs"
         Hash                 string `json:"Hash"` // Hash of the filesystem image
         DisableRandomWorkDir bool   `json:"DisableRandomWorkDir"` // Whether to use a fixed working directory
         MountOrExtract       uint8  `json:"MountOrExtract"` // Run behavior: 0 (FUSE only), 1 (Extract only), 2 (FUSE with extract fallback), 3 (FUSE with extract fallback for files < 350MB)
     }
     ```

3. **Static Tools Section (.pbundle_static_tools)**:
   - The ELF file includes a section named `.pbundle_static_tools`, containing a Zstandard (ZSTD)-compressed tar archive.
   - This archive holds tools necessary for mounting or extracting the filesystem image, such as `dwarfs`, `dwarfsextract`, `squashfuse`, or `unsquashfs`, depending on the filesystem type.

4. **Filesystem Image**:
   - Immediately following the ELF runtime, the AppBundle contains the compressed filesystem image (either DwarFS or SquashFS).
   - This image encapsulates the application's AppDir, including all necessary files and dependencies.

## Creation of an AppBundle

An AppBundle is created using the `pelf` tool, which performs the following steps:

1. **Prepare the AppDir**:
   - The `pelfCreator` tool constructs an AppDir, a directory containing the application's files, including:
     - `AppRun`: The entrypoint script that orchestrates the execution.
     - `.DirIcon`: An optional icon file (PNG, in sizes 512x512, 256x256, or 128x128).
     - `.DirIcon.svg`: An optional SVG icon.
     - `program.desktop`: An optional desktop entry file.
     - `program.appdata.xml`: An optional AppStream metadata file.
     - `proto` or `rootfs`: A directory containing the application's filesystem, typically based on a minimal Linux distribution like Alpine or ArchLinux.
   - The AppDir may also include additional binaries and configuration files as needed.

2. **Embed Runtime Information**:
   - The `pelf` tool embeds the `.pbundle_runtime_info` section with metadata about the AppBundle, including its ID, filesystem type, and runtime behavior.

3. **Embed Static Tools**:
   - Tools required for mounting or extracting the filesystem (e.g., `dwarfs`, `squashfuse`) are compressed into a ZSTD tar archive and embedded in the `.pbundle_static_tools` section.

4. **Append Filesystem Image**:
   - The AppDir is compressed into a DwarFS or SquashFS image, depending on the configuration, and appended to the ELF runtime.
   - The offset of the filesystem image is recorded in the runtime configuration for access during execution.

5. **Finalize the Executable**:
   - The `pelf` tool combines the ELF runtime, runtime information, static tools, and filesystem image into a single executable file with the `.AppBundle` extension.

## Run Behaviors

The `MountOrExtract` field in the `.pbundle_runtime_info` section determines how the AppBundle behaves when executed:

- **0 (FUSE Mounting Only)**: The AppBundle uses FUSE to mount the filesystem image. If FUSE is unavailable, it fails without falling back to extraction.
- **1 (Extract and Run)**: The AppBundle extracts the filesystem image to a temporary directory (typically in `tmpfs`) and executes from there, ignoring FUSE even if available.
- **2 (FUSE with Fallback)**: The AppBundle attempts to use FUSE to mount the filesystem. If FUSE is unavailable, it falls back to extracting the filesystem to `tmpfs`.
- **3 (FUSE with Conditional Fallback)**: Similar to option 2, but fallback to extraction only occurs if the AppBundle file is smaller than 350MB.

## Expected Contents of the Filesystem Image

The filesystem image within the AppBundle is expected to be an AppDir with at least the following:

- **AppRun**: A shell script that serves as the entrypoint for the application. It sets up the environment and executes the main program.
- **Optional Files**:
  - `.DirIcon`: A PNG icon in a standard size (512x512, 256x256, or 128x128).
  - `.DirIcon.svg`: An SVG icon
  - `program.desktop`: A desktop entry file for integration with desktop environments.
  - `program.appdata.xml`: An AppStream metadata file for application metadata.
  - `proto` or `rootfs`: A directory containing the application's filesystem, including binaries, libraries, and configuration files.

## Notes

- The AppBundle format is designed to be self-contained, requiring no external dependencies for execution in most cases, assuming the necessary tools are embedded or available on the host system.
- The choice of filesystem (DwarFS or SquashFS) affects the tools included in the `.pbundle_static_tools` section and the runtime behavior.
