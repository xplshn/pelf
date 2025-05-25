+++
date = ''
draft = false
title = 'tooling.md'
[params.author]
  name = ''
  email = ''
+++
+++
date = "2025-05-24T21:04:50-03:00"
draft = false
title = "AppBundle Tooling: pelf and pelfCreator"
[params.author]
  name = "xplshn"
  email = "xplshn@murena.io"
+++

# AppBundle Tooling: pelf and pelfCreator

This document describes the `pelf` and `pelfCreator` tools, which are used to create and manage AppBundles, self-contained executable packages for Linux applications.

## pelf Tool

The `pelf` tool is responsible for assembling an AppBundle by combining an ELF runtime, runtime information, static tools, and a compressed filesystem image.

### Functionality

- **Purpose**: Creates an AppBundle from an AppDir, embedding necessary metadata and tools.
- **Key Operations**:
  - Reads an AppDir, verifies that it contains an executable AppRun
  - Copies the runtime to the output file
  - Embeds runtime information (MessagePack format) in the `.pbundle_runtime_info` section of the output file
  - If the runtime is a universal runtime (e.g: noEmbed edition), it puts a ZSTD-compressed tar archive of static tools (depending the chosen filesystem: e.g., `dwarfs`, `squashfuse`, `unsquashfs`) in the `.pbundle_static_tools` section of the output file.
  - Compresses the AppDir into a DwarFS or SquashFS filesystem image and appends it to the output file
  - Sets the AppBundle's executable permissions and finalizes the output file.

### Command-Line Usage

The pelf tool is can be invoked with the following flags:

-   **--add-appdir, -a <path>**: Specifies the AppDir to package.
-   **--appbundle-id, -i <id>**: Sets the unique AppBundleID for the AppBundle.
-   **--output-to, -o <file>**: Specifies the output file name (e.g., app.dwfs.AppBundle).
-   **--compression, -c <flags>**: Specifies compression flags for the filesystem.
-   **--static-tools-dir <path>**: Specifies a custom directory for static tools.
-   **--runtime <path>**: Specifies the runtime binary to use.
-   **--upx**: Enables UPX compression for static tools. (upx must be in the host system)
-   **--filesystem, -j <fs>:** Selects the filesystem type (squashfs or [dwarfs]).
-   **--prefer-tools-in-path:** Prefers tools in `$PATH` over embedded ones.
-   **--list-static-tools:** Lists embedded tools with their B3SUMs.
-   **--disable-use-random-workdir, -d:** Disables random working directory usage. This making AppBundles leave their mountpoint open and reusing it in each launch. This is ideal for big programs that need to launch ultra-fast, such as web browsers, messaging clients, etc
-   **--run-behavior, -b <0|1|2|3>:** Sets runtime behavior (0: FUSE only, 1: Extract only, 2: FUSE with extract fallback, 3: FUSE with extract fallback if ≤ 350MB).
-   **--appimage-compat, -A:** Sets the "AI" magic-bytes, so that AppBundles are detected as AppImages by AppImage integration software like [AppImageUpdate](https://github.com/AppImageCommunity/AppImageUpdate)
-   **--add-runtime-info-section <string>:** Adds custom runtime information fields. (e.g: '.MyCustomRuntimeInfoSection:Hello')
-   **--add-elf-section <path>:** Adds a custom ELF section from a .elfS file., where the filename of the .elfS file minus the extension is the section name, and the file contents are the data
-   **--add-updinfo <string>:** Adds an upd_info ELF section with the given string.

## pelfCreator Tool

The `pelfCreator` tool is a higher-level utility that prepares an AppDir and invokes `pelf` to create an AppBundle. It supports multiple modes for different use cases.

### Functionality

- **Purpose**: Creates an AppDir, populates it with a root filesystem, application files, and dependencies, and then packages it into an AppBundle.
- **Key Operations**:
  - Sets up a temporary directory for processing.
  - Downloads or uses a local root filesystem (e.g., Alpine or ArchLinux).
  - Installs specified packages using `apk` (Alpine) or `pacman` (ArchLinux).
  - Configures the AppRun script and entrypoint.
  - Optionally processes binaries with `lib4bin` for `sharun` mode.
  - Trims the filesystem based on `--keep` or `--getrid` flags.
  - Calls `pelf` to finalize the AppBundle.

### Command-Line Usage

The `pelfCreator` tool is invoked with the following flags:

- **`--maintainer <name>`**: Specifies the maintainer's name (required).
- **`--name <name>`**: Sets the application name (required).
- **`--appbundle-id <id>`**: Sets the `AppBundleID` (optional; defaults to `<name>-<date>-<maintainer>`).
- **`--pkg-add <packages>`**: Specifies packages to install in the root filesystem (required).
- **`--entrypoint <path>`**: Sets the entrypoint command or desktop file (required unless using `--multicall`).
- **`--keep <files>`**: Specifies files to keep in the `proto` directory.
- **`--getrid <files>`**: Specifies files to remove from the `proto` directory.
- **`--filesystem <fs>`**: Selects the filesystem type (`dwfs` or `squashfs`; default: `dwfs`).
- **`--output-to <file>`**: Specifies the output AppBundle file (optional; defaults to `<name>.<fs>.AppBundle`).
- **`--local <path>`**: Specifies a directory or archive containing resources (e.g., `rootfs.tar`, `AppRun`, `bwrap`).
- **`--preserve-rootfs-permissions`**: Preserves original filesystem permissions.
- **`--dontpack`**: Stops short of packaging the AppDir into an AppBundle, leaving only the AppDir.
- **`--sharun <binaries>`**: Processes specified binaries with `lib4bin` and uses `AppRun.sharun` or `AppRun.sharun.ovfsProto`.
- **`--sandbox`**: Enables sandbox mode using `AppRun.rootfs-based` with `bwrap`.

### Modes of Operation

1. **Sandbox Mode** (`--sandbox`):
   - Retains and binds the `proto` directory as the root filesystem, with host directory bindings (e.g., `/home`, `/tmp`, `/etc`).
   - Supports trimming of the `proto` directory using `--keep` or `--getrid` flags to reduce size.
   - Uses `AppRun.rootfs-based` to run the application in a `bwrap` sandbox.
   - Can be customized via env vars such as `SHARE_LOOK`, `SHARE_FONTS`, `SHARE_AUDIO`, and `UID0_GID0` for fine-grained control over sandboxing.
   - Suitable for applications requiring strict isolation from the host system. Or those that refuse to work with the default mode (hybrid)

2. **Sharun Mode** (`--sharun <binaries>`):
   - Processes specified binaries with `lib4bin` to ensure compatibility and portability.
   - Uses `AppRun.sharun` (if `proto` is removed) or `AppRun.sharun.ovfsProto` (if `proto` is retained).
   - When using `AppRun.sharun.ovfsProto`, employs `unionfs-fuse` to create a copy-on-write overlay of the `proto` directory.
   - Sets `LD_LIBRARY_PATH` to include library paths from the AppDir, ensuring binaries can find their dependencies.
   - Ideal for lightweight applications or when minimizing filesystem size is a priority.

3. **Default Mode** (can be combined with `--sharun`, to ship lightweight AppBundles that include a default config file, etc, but otherwise use the system's files unless they're missing):
   - Retains the `proto` directory
   - Supports trimming of the `proto` directory using `--keep` or `--getrid` flags to reduce size.
   - Uses `AppRun.sharun.ovfsProto` to execute the application with a `unionfs-fuse` overlay of the user's `/` & the AppDir's `proto`.
   - Suitable for most applications, as it allows the AppBundle to use files from the system if they don't exist in the AppDir's `proto` and vice-versa

## Notes

- The `pelfCreator` tool supports extensibility through custom root filesystems and package managers via the `--local` flag.
