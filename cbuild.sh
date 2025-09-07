#!/bin/sh
# TODO: It really doesn't make sense to use `dbin` here if we (I) plan to support BSDs too.
#       The only way to build the tooling right now in BSDs is to manually gather and compress all dependencies
#       To then build each target (appbundle-runtime (universal), pelf)
#
# TODO: What to do about pelfCreator in non-Linux systems?
#

[ "$DEBUG" = "1" ] && set -x

OPWD="$PWD"
BASE="$(dirname "$(realpath "$0")")"
TEMP_DIR="/tmp/pelf_build_$(date +%s)"
# Change to BASE directory if not already there
if [ "$OPWD" != "$BASE" ]; then
    echo "Changing to $BASE"
    cd "$BASE" || exit 1
fi
trap 'cd "$OPWD"; [ -d "$TEMP_DIR" ] && rm -rf "$TEMP_DIR"' EXIT

# -Dbin-related-envs-------------------------------#
export DBIN_INSTALL_DIR="$BASE/binaryDependencies" #
export DBIN_NOCONFIG="1"                           #
# -Dependency-Revision-Tracking--------------------#
DWFS_VER="0.13.0"                                  #
# -------------------------------------------------#

if [ "$(uname -m)" = "aarch64" ]; then
    export GOARCH="arm64" # Weird things happen when it is not set, I think my GH action has this env already set.
fi

# Logging functions
log() {
    printf "\033[34m->\033[m %s\n" "$*"
}

log_warning() {
    printf "\033[33m->\033[m %s\n" "$*"
}

log_error() {
    printf "\033[31m->\033[m %s\n" "$*"
    exit 1
}

# Utility functions
unnappear() {
    "$@" >/dev/null 2>&1
}

available() {
    unnappear which "$1" || return 1
}

require() {
    available "$1" || log_error "[$1] is not installed. Please ensure the command is available [$1] and try again."
}

build_appbundle_runtime() {
    log "Building appbundle-runtime variants"
    if [ "$(basename "$(uname -o)")" = "Linux" ]; then
    log "Preparing appbundle-runtime binary dependencies"
        export DBIN_INSTALL_DIR="$BASE/appbundle-runtime/binaryDependencies"
        mkdir -p "$DBIN_INSTALL_DIR"
        # Fetch required tools using curl and dbin
        curl -sL "https://github.com/mhx/dwarfs/releases/download/v$DWFS_VER/dwarfs-fuse-extract-$DWFS_VER-$(uname -o)-$(uname -m).upx" -o "$DBIN_INSTALL_DIR/dwarfs"
        chmod +x "$DBIN_INSTALL_DIR/dwarfs"
        curl -sL "https://github.com/VHSgunzo/squashfuse-static/releases/latest/download/squashfuse_ll-musl-mimalloc-$(uname -m)" -o "$DBIN_INSTALL_DIR/squashfuse"
        chmod +x "$DBIN_INSTALL_DIR/squashfuse"
        dbin add squashfs-tools/unsquashfs
        # UPX the unsquashfs binary
        if available "upx"; then
            log "Compressing unsquashfs for appbundle-runtime"
            upx "$DBIN_INSTALL_DIR/unsquashfs" || log_error "Unable to compress unsquashfs"
        else
            log_warning "upx not available. The unsquashfs binary will be unnecessarily large"
        fi
        chmod +x "$DBIN_INSTALL_DIR"/*

        # Build dwarfs version
        log "Building dwarfs appbundle-runtime"
        go build --tags dwarfs -o "$BASE/binaryDependencies/appbundle-runtime_dwarfs" ./appbundle-runtime || log_error "Unable to build appbundle-runtime_dwarfs"
        # Build squashfs version
        log "Building squashfs appbundle-runtime"
        go build --tags squashfs -o "$BASE/binaryDependencies/appbundle-runtime_squashfs" ./appbundle-runtime || log_error "Unable to build appbundle-runtime_squashfs"

        available "strip" && strip "$BASE/binaryDependencies/appbundle-runtime_dwarfs" "$BASE/binaryDependencies/appbundle-runtime_squashfs"
    else
        # Build standard version
        log "Building universal appbundle-runtime"
        go build --tags noEmbed -o "$BASE/binaryDependencies/appbundle-runtime" ./appbundle-runtime || log_error "Unable to build appbundle-runtime"
        available "strip" && strip "$BASE/binaryDependencies/appbundle-runtime"
    fi

    if ! available "strip"; then
        log_warning "strip not available. The binaries will be unnecessarily large"
    fi
}

build_pelf() {
    if [ -f "./pelf.go" ]; then
        build_appbundle_runtime

        export DBIN_INSTALL_DIR="$BASE/binaryDependencies"
        mkdir -p "$DBIN_INSTALL_DIR"

        [ "$NO_REMOTE" != "1" ] && handle_dependencies

        log "Creating binaryDependencies.tar.zst for pelf"
        tar -C binaryDependencies -c . | zstd -T0 -19 -fo binaryDependencies.tar.zst

        rm -f ./pelf
        export CGO_ENABLED=0
        export GOFLAGS="-ldflags=-static-pie -ldflags=-s -ldflags=-w"
        go build -o ./pelf || log_error "Unable to build ./pelf"

        if available "upx"; then
            log "Compressing ./pelf tool"
            upx ./pelf || log_error "unable to compress ./pelf"
            rm -f ./pelf.upx
        else
            log_warning "upx not available. The resulting binary will be unnecessarily large"
        fi
    else
        log_error "./pelf.go not found."
    fi
}

build_pelfCreator() {
    log "Building pelfCreator"

    # Create temporary build directory
    mkdir -p "$TEMP_DIR/binaryDependencies"

    # Copy only the necessary dependencies to temp dir
    log "Preparing dependencies for pelfCreator"
    cp "$BASE/pelf" "$TEMP_DIR/binaryDependencies/pelf" || log_error "Unable to move pelf to the binaryDependencies of pelfCreator"

    # Get the unionfs and bwrap binaries
    mkdir -p "$TEMP_DIR/binaryDependencies"
    DBIN_INSTALL_DIR="$TEMP_DIR/binaryDependencies" dbin add unionfs-fuse3/unionfs bwrap

    # Copy AppRun assets
    if [ -d "$BASE/assets" ]; then
        cp "$BASE/assets/AppRun"* "$BASE/assets/LAUNCH"* "$TEMP_DIR/binaryDependencies/" 2>/dev/null || log_warning "AppRun assets not found"
    else
        log_warning "assets directory not found, AppRun files might be missing"
    fi

	cat <<'EOF' > "$TEMP_DIR/binaryDependencies/pkgadd.sh"
#!/bin/sh
fakeroot apk \
        --allow-untrusted \
        --no-interactive  \
        --no-cache        \
        --initdb add      \
        $@ || true

# Check if each package in $@ is installed
for pkg in "$@"; do
    if ! fakeroot apk info | grep -q "^${pkg}$" >/dev/null 2>&1; then
        echo "error: Package $pkg not installed" >&2
        exit 1
    fi
done

# Get version of the first package ($1)
if [ -n "$1" ]; then
    version=$(fakeroot apk info "$1" 2>/dev/null | head -n 1 | cut -d' ' -f1 | cut -d'-' -f2-)
    if [ -n "$version" ]; then
        # Blue color for NOTE using ANSI escape codes
        printf "\033[34mNOTICE\033[0m: using %s's version as the AppBundle's version: [%s]\n" "$1" "$version"
    else
        echo "error: could not retrieve version for $1" >&2
        exit 1
    fi
fi
EOF
	chmod +x "$TEMP_DIR/binaryDependencies/pkgadd.sh"

    if [ ! -f "$TEMP_DIR/binaryDependencies/rootfs.tar.zst" ]; then
        log "Downloading rootfs"
        RELEASE_NAME="AlpineLinux_edge-$(uname -m).tar.xz"
        curl -sL "https://github.com/xplshn/filesystems/releases/latest/download/$RELEASE_NAME" -o "$TEMP_DIR/binaryDependencies/$RELEASE_NAME"
        cd "$TEMP_DIR/binaryDependencies" || log_error "Failed to change to temp directory"
        ln -sfT "$RELEASE_NAME" "rootfs.tar.${RELEASE_NAME##*.}"
    fi

    if [ ! -f "$TEMP_DIR/binaryDependencies/sharun" ]; then
        log "Downloading sharun-$(uname -m)-aio"
        curl -sL "https://github.com/VHSgunzo/sharun/releases/latest/download/sharun-$(uname -m)-aio" -o "$TEMP_DIR/binaryDependencies/sharun"
        chmod +x "$TEMP_DIR/binaryDependencies/sharun"
    fi

    unnappear rm -rf "$BASE/cmd/pelfCreator/binaryDependencies"
    mv "$TEMP_DIR/binaryDependencies" "$BASE/cmd/pelfCreator/binaryDependencies" || log_error "Unable to move binaryDependencies from temp to pelfCreator"

    # Create archive of binaryDependencies
    log "Creating binaryDependencies.tar.zst for pelfCreator"
    tar -C "$BASE/cmd/pelfCreator/binaryDependencies" -c . | zstd -T0 -19 -fo "$BASE/cmd/pelfCreator/binaryDependencies.tar.zst"

    log "Building pelfCreator"
    cd "$BASE/cmd/pelfCreator" || log_error "Unable to change directory to ./cmd/pelfCreator"
    go build || log_error "Unable to build pelfCreator"
    if available "upx"; then
        log "Compressing ./pelfCreator tool"
        upx ./pelfCreator || log_error "unable to compress ./pelfCreator"
        rm -f ./pelfCreator.upx
    else
        log_warning "upx not available. The resulting binary will be unnecessarily large"
    fi
    cd "$BASE" || log_error "Unable to go back to $BASE"

    # Clean up temporary directory
    rm -rf "$TEMP_DIR"
}

build_pelfCreator_extensions() {
    log "Building pelfCreator extensions"

    if [ ! -d "$BASE/cmd/pelfCreator/binaryDependencies" ]; then
        log_error "pelfCreator must be built first. Run './cbuild.sh pelfCreator' before building extensions"
    fi

    mkdir -p "$TEMP_DIR/binaryDependencies"

    # Copy existing dependencies (excluding rootfs)
    log "Copying dependencies from pelfCreator binaryDependencies (excluding rootfs)"
    for file in "$BASE/cmd/pelfCreator/binaryDependencies"/*; do
        [ -f "$file" ] || continue
        filename=$(basename "$file")
        case "$filename" in
            *.tar* | pkgadd.sh)
                log "Skipping $filename"
                ;;
            *)
                cp "$file" "$TEMP_DIR/binaryDependencies/"
                ;;
        esac
    done

    # Build ArchLinux extension
    log "Creating pelfCreator ArchLinux extension"

    cat <<'EOF' > "$TEMP_DIR/binaryDependencies/pkgadd.sh"
#!/bin/sh
fakeroot pacman -Sy --noconfirm $@
EOF
    chmod +x "$TEMP_DIR/binaryDependencies/pkgadd.sh"

    # Download ArchLinux rootfs
    if [ ! -f "$TEMP_DIR/binaryDependencies/rootfs.tar.zst" ]; then
        log "Downloading ArchLinux rootfs"
        RELEASE_NAME="ArchLinux-base_$(uname -m).tar.zst"
        curl -sL "https://github.com/xplshn/filesystems/releases/latest/download/$RELEASE_NAME" -o "$TEMP_DIR/binaryDependencies/$RELEASE_NAME"
        cd "$TEMP_DIR/binaryDependencies" || log_error "Failed to change to temp directory"
        ln -sfT "$RELEASE_NAME" "rootfs.tar.${RELEASE_NAME##*.}"
        cd "$BASE" || log_error "Unable to return to base directory"
    fi

    # Create the extension archive
    log "Creating pelfCreatorExtension_archLinux.tar.zst"
    tar -C "$TEMP_DIR/binaryDependencies" -c . | zstd -T0 -19 -fo "$BASE/cmd/pelfCreator/pelfCreatorExtension_archLinux.tar.zst"

    # Clean up
    rm -rf "$TEMP_DIR"

    log "pelfCreator ArchLinux extension created successfully"
}

build_appstream_helper() {
    log "Building appstream-helper"
    cd "$BASE/cmd/misc/appstream-helper" || log_error "Unable to change directory to ./cmd/misc/appstream-helper"
    go build || log_error "Unable to build appstream-helper"
    if available "upx"; then
        log "Compressing ./appstream-helper tool"
        upx ./appstream-helper
    else
        log_warning "upx not available. The resulting binary will be unnecessarily large"
    fi
    cd "$BASE" || log_error "Unable to go back to $BASE"
}

clean_project() {
    log "Starting clean process"
    rm -rf ./pelf ./pelf.upx ./binaryDependencies ./binaryDependencies.tar.zst ./appbundle-runtime/binaryDependencies ./cmd/pelfCreator/pelfCreator ./cmd/pelfCreator/binaryDependencies* ./cmd/pelfCreator/*.zst.tar ./cmd/misc/appstream-helper/appstream-helper ./cmd/misc/appstream-helper/appstream-helper.upx
    log "Clean process completed"
}

retrieve_executable() {
    readlink -f ./pelf
    readlink -f ./cmd/pelfCreator/pelfCreator
    readlink -f ./cmd/misc/appstream-helper/appstream-helper
    readlink -f ./cmd/pelfCreator/binaryDependencies_archLinux.tar.zst
}

handle_dependencies() {
    mkdir -p "$DBIN_INSTALL_DIR"
    DEPS="bintools/objcopy
          squashfs-tools/mksquashfs
          squashfs-tools/unsquashfs
          squashfuse/squashfuse_ll"

    unnappear rm "$DBIN_INSTALL_DIR/dwarfs-tools"
    curl -sL "https://github.com/mhx/dwarfs/releases/download/v$DWFS_VER/dwarfs-universal-$DWFS_VER-Linux-$(uname -m)" -o "$DBIN_INSTALL_DIR/dwarfs-tools"
    chmod +x "$DBIN_INSTALL_DIR/dwarfs-tools"
    unnappear rm "$DBIN_INSTALL_DIR/squashfuse_ll"
    curl -sL "https://github.com/VHSgunzo/squashfuse-static/releases/latest/download/squashfuse_ll-musl-mimalloc-$(uname -m)" -o "$DBIN_INSTALL_DIR/squashfuse_ll"
    chmod +x "$DBIN_INSTALL_DIR/squashfuse_ll"

    log "Installing dependencies..."
    # shellcheck disable=SC2086
    dbin add $DEPS

    cd "$DBIN_INSTALL_DIR" && {
        log "Linking dependencies"
        [ -f ./dwarfs-tools ] && [ ! -h ./dwarfs-tools ] && {
            mv ./dwarfs-tools ./dwarfs
            ln -sfT dwarfs mkdwarfs
        }
        ln -sfT dwarfs dwarfsextract
        upx mksquashfs mkdwarfs objcopy
        [ -f ./squashfuse_ll ] && [ ! -h ./squashfuse_ll ] && mv ./squashfuse_ll ./squashfuse
        ln -sfT squashfuse squashfuse_ll
    }
    unnappear rm ./*.upx
    cd "$BASE" || log_error "Unable to go back to $BASE"
}

update_dependencies() {
    dbin update
}

# Main case statement for actions
case "$1" in
    "" | "build")
        require go
        log "Starting build process for targets: pelf, pelfCreator, appstream-helper"
        build_pelf
        build_pelfCreator
        build_appstream_helper
        ;;
    "pelf")
        require go
        log "Starting build process for target: pelf"
        build_pelf
        ;;
    "appbundle-runtime")
        require go
        log "Starting build process for target: appbundle-runtime"
        build_appbundle_runtime
        ;;
    "pelfCreator")
        require go
        log "Starting build process for target: pelfCreator"
        build_pelf
        build_pelfCreator
        ;;
    "pelfCreator_extensions")
        log "Starting build process for target: pelfCreator_extensions"
        build_pelfCreator_extensions
        # TODO: Add moar
        ;;
    "appstream-helper")
        require go
        log "Starting build process for target: appstream-helper"
        build_appstream_helper
        ;;
    "clean")
        clean_project
        ;;
    "retrieve")
        retrieve_executable
        ;;
    "update-deps")
        update_dependencies
        ;;
    *)
        log_warning "Usage: $0 {build|pelf|appbundle-runtime|pelfCreator|pelfCreator_extensions|appstream-helper|clean|retrieve|update-deps}"
        exit 1
        ;;
esac
