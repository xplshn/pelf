# AppBundle Runtime Execution

This document describes how the AppBundle runtime operates, including how it reads its own information, extracts static tools, determines environment variables, and handles runtime flags.

## Execution Flow

When an AppBundle is executed, the runtime performs the following steps:

1. **Read Runtime Information**:
   - The runtime reads the `.pbundle_runtime_info` section from the ELF file, which contains CBOR-encoded metadata.
   - This section includes:
     - `AppBundleID`: A unique identifier for the AppBundle. (e.g: "com.brave.Browser-xplshn-2025-05-19". You're not forced to follow this format, but if you do, you can create a [dbin](https://github.com/xplshn/dbin) repository that countains your AppBundle by using our [appstream-helper](https://github.com/xplshn/pelf/blob/master/cmd/misc/appstream-helper/appstream-helper.go) tool. `$NAME-$MAINTAINER-$DATE` or preferably: `$APPSTREAM_ID-$MAINTAINER-$DATE`, so that you don't have to include an AppStream file within the AppDir for appstream-helper to get metadata from it)
     - `PelfVersion`: The version of the `pelf` tool used to create the AppBundle.
     - `HostInfo`: System information from `uname -mrsp(v)` of the build machine.
     - `FilesystemType`: Either "dwarfs" or "squashfs".
     - `Hash`: A hash of the filesystem image for integrity verification.
     - `DisableRandomWorkDir`: A boolean indicating whether to use a fixed working directory.
     - `MountOrExtract`: A uint8 value (0–3) specifying the run behavior (see below).
   - The runtime uses this information to configure its behavior and locate the filesystem image.

2. **Extract Static Tools**:
   - The runtime accesses the static tools required for mounting or extracting the filesystem (e.g., `dwarfs`, `dwarfsextract`, `squashfuse`, `unsquashfs`).
   - The handling of static tools depends on the build mode:
     - **noEmbed Edition**: The tools are embedded in the `.pbundle_static_tools` ELF section as a ZSTD-compressed tar archive. The runtime determines the filesystem mounting and extraction commands at runtime, extracts the needed files from this archive to a temporary directory (`cfg.staticToolsDir`), and uses them to either mount or extract the filesystem.
     - **Embed Edition**: The tools are embedded directly in the binary using Go’s `embed` package, without compression. The runtime accesses these tools directly from the embedded filesystem, without needing to extract a compressed archive.

3. **Exported Env Variables**:
   - The runtime sets up several environment variables to facilitate execution:
     - **HOME**: If a portable home directory (`.AppBundleID.home`) exists in the same directory as the AppBundle, it is used as `$HOME`.
     - **XDG_DATA_HOME**: If a portable share directory (`.AppBundleID.share`) exists, it is used as `$XDG_DATA_HOME`.
     - **XDG_CONFIG_HOME**: If a portable config directory (`.AppBundleID.config`) exists, it is used as `$XDG_CONFIG_HOME`.
     - **APPDIR**: Set to the mount or extraction directory
     - **SELF**: The absolute path to the AppBundle executable.
     - **ARGV0**: The basename of `$SELF`
     - **PATH**: Augmented to include the AppBundle's `bin` directory and the directory containing the static tools.

4. **Mount or Extract Filesystem**:
   - The runtime decides whether to mount or extract the filesystem image based on the `MountOrExtract` value:
     - **0**: Mounts the filesystem using FUSE (e.g., `dwarfs` or `squashfuse`) and fails if FUSE is unavailable.
     - **1**: Extracts the filesystem to a temporary directory (usually in `tmpfs`) and runs from there.
     - **2**: Attempts to mount with FUSE; falls back to extraction if FUSE is unavailable.
     - **3**: Similar to 2, but only falls back to extraction if the AppBundle is smaller than 350MB.

5. **Execute the Application**:
   - The runtime executes the `AppRun` script within the AppDir.
   - If a specific command is provided via `--pbundle_link`, the runtime executes that command within the AppBundle's environment, instead of executing the AppRun.

## Runtime Flags

The AppBundle runtime supports several command-line flags to modify its behavior:

- **`--pbundle_help`**: Displays help information, including the `PelfVersion`, `HostInfo`, and internal configuration variables (e.g., `cfg.exeName`, `cfg.mountDir`).
- **`--pbundle_list`**: Lists the contents of the AppBundle's filesystem, including static tools.
- **`--pbundle_link <binary>`**: Executes a specified command within the AppBundle's environment, leveraging its `PATH` and other variables.
- **`--pbundle_pngIcon`**: Outputs the base64-encoded `.DirIcon` (PNG) if it exists; otherwise, exits with error code 1.
- **`--pbundle_svgIcon`**: Outputs the base64-encoded `.DirIcon.svg` if it exists; otherwise, exits with error code 1.
- **`--pbundle_appstream`**: Outputs the base64-encoded first `.xml` file (AppStream metadata) found in the AppDir.
- **`--pbundle_desktop`**: Outputs the base64-encoded first `.desktop` file found in the AppDir.
- **`--pbundle_portableHome`**: Creates a portable home directory (`.AppBundleID.home`) in the same directory as the AppBundle.
- **`--pbundle_portableConfig`**: Creates a portable config directory (`.AppBundleID.config`) in the same directory as the AppBundle.
- **`--pbundle_cleanup`**: Unmounts and removes the AppBundle's working directory and mount point, affecting only instances of the same AppBundle.
- **`--pbundle_mount`**: Mounts the filesystem to a specified or default directory and keeps the mount active.
- **`--pbundle_extract [globs]`**: Extracts the filesystem to a directory (default: `<rExeName>_<filesystemType>` or `squashfs-root` for AppImage compatibility). Supports selective extraction with glob patterns.
- **`--pbundle_extract_and_run`**: Extracts the filesystem and immediately executes the entrypoint.
- **`--pbundle_offset`**: Outputs the offset of the filesystem image within the AppBundle.
- **AppImage Compatibility Flags**:
  - `--appimage-extract`: Same as `--pbundle_extract`, but uses `squashfs-root` as the output directory.
  - `--appimage-extract-and-run`: Same as `--pbundle_extract_and_run`.
  - `--appimage-mount`: Same as `--pbundle_mount`.
  - `--appimage-offset`: Same as `--pbundle_offset`.

## Notes

- The choice between `noEmbed` and embed modes affects how static tools are stored and accessed. The `noEmbed` mode uses a compressed archive for flexibility, while the embed mode simplifies access by avoiding compression.
- The `AppRun` script (e.g., `AppRun.rootfs-based`, `AppRun.sharun`, or `AppRun.sharun.ovfsProto`) determines sandboxing and execution behavior, such as using `bwrap` or `unionfs-fuse`.
- The runtime ensures cleanup of temporary directories unless `--pbundle_cleanup` is explicitly called or `noCleanup` is set.
- The `noEmbed` build tag for the `appbundle-runtime` allows you to build a single appbundle-runtime binary, that determines which filesystem to use at runtime, after having read its .pbundle_runtime_info and decompressed the .tar.zst data within the .pbundle_static_tools ELF section
  - If you're writting a new runtime, I recommend you implement appbundle-runtime.go, cli.go and noEmbed.go. This edition of the runtime is the most portable and flexible. It is simplifies a lot the build process.
