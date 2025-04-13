#!/bin/sh

[ "$DEBUG" = "1" ] && set -x

SELF="$(readlink -f "$0")"
OPWD="$PWD"
BASE="$(dirname "$(realpath "$0")")"
TEMP_DIR="/tmp/pelf_build_$(date +%s)"
export DBIN_INSTALL_DIR="$BASE/binaryDependencies"
export DBIN_NOCONFIG="1"

# Change to BASE directory if not already there
if [ "$OPWD" != "$BASE" ]; then
    echo "Changing to $BASE"
    cd "$BASE" || exit 1
fi
trap 'cd "$OPWD"; [ -d "$TEMP_DIR" ] && rm -rf "$TEMP_DIR"' EXIT

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

# Project functions
build_pelf() {
    if [ -f "./pelf.go" ]; then
        mkdir -p "$DBIN_INSTALL_DIR"
        echo ./appbundle-runtime/*.go | xargs go build -o "$DBIN_INSTALL_DIR/appbundle-runtime"
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
# The AlpineLinux Package Keeper has a very annoying error which can't be disabled!
# Track https://gitlab.alpinelinux.org/alpine/apk-tools/-/issues/11099
apk -U \
    --allow-untrusted \
    --no-interactive \
    --no-cache \
    --initdb add \
    $@ || true # Because of the aforementioned!
EOF
	chmod +x "$TEMP_DIR/binaryDependencies/pkgadd.sh"

    if [ ! -f "$TEMP_DIR/binaryDependencies/rootfs.tar.zst" ]; then
        log "Downloading rootfs"
        RELEASE_NAME="AlpineLinux_latest-stable-$(uname -m).tar.xz"
        curl -sLl "https://github.com/xplshn/filesystems/releases/latest/download/$RELEASE_NAME" -o "$TEMP_DIR/binaryDependencies/$RELEASE_NAME"
        cd "$TEMP_DIR/binaryDependencies" || log_error "Failed to change to temp directory"
        ln -sfT "$RELEASE_NAME" "rootfs.tar.${RELEASE_NAME##*.}"
    fi

    if [ ! -f "$TEMP_DIR/binaryDependencies/sharun" ]; then
        log "Downloading sharun-$(uname -m)-aio"
        curl -sLl "https://github.com/VHSgunzo/sharun/releases/latest/download/sharun-$(uname -m)-aio" -o "$TEMP_DIR/binaryDependencies/sharun"
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

clean_project() {
    log "Starting clean process"
    rm -rf ./pelf ./pelf.upx ./binaryDependencies ./binaryDependencies.tar.zst ./cmd/pelfCreator/dependencies
    log "Clean process completed"
}

retrieve_executable() {
    readlink -f ./pelf
    readlink -f ./cmd/pelfCreator/pelfCreator
}

handle_dependencies() {
    mkdir -p "$DBIN_INSTALL_DIR"
    DEPS="squashfuse/squashfuse_ll
          squashfs-tools/unsquashfs
          squashfs-tools/mksquashfs
          bintools/objcopy"

    if [ "$_RELEASE" = "1" ]; then
        unnappear rm "$DBIN_INSTALL_DIR/mkdwarfs"
        curl -sLl "https://github.com/mhx/dwarfs/releases/download/v0.12.1/dwarfs-universal-0.12.1-Linux-$(uname -m)" -o "$DBIN_INSTALL_DIR/mkdwarfs"
        chmod +x "$DBIN_INSTALL_DIR/mkdwarfs"

        unnappear rm "$DBIN_INSTALL_DIR/dwarfs" "$DBIN_INSTALL_DIR/dwarfsextract"
        curl -sLl "https://github.com/mhx/dwarfs/releases/download/v0.12.1/dwarfs-fuse-extract-0.12.1-Linux-$(uname -m)" -o "$DBIN_INSTALL_DIR/dwarfs"
        chmod +x "$DBIN_INSTALL_DIR/dwarfs" "$DBIN_INSTALL_DIR/dwarfsextract"

        unnappear rm "$DBIN_INSTALL_DIR/squashfuse_ll"
        curl -sLl "https://github.com/VHSgunzo/squashfuse-static/releases/latest/download/squashfuse_ll-musl-mimalloc-$(uname -m)" -o "$DBIN_INSTALL_DIR/squashfuse_ll"
        chmod +x "$DBIN_INSTALL_DIR/squashfuse_ll"
    else
        DEPS="dwarfs/dwarfs-tools
              $DEPS"
    fi

    #if [ -n "$(ls -A "$DBIN_INSTALL_DIR" 2>/dev/null)" ]; then
    #    log "Updating dependencies..."
    #    dbin update
    #else
        log "Installing dependencies..."
        # shellcheck disable=SC2086
        dbin add $DEPS
    #fi

    cd "$DBIN_INSTALL_DIR" && {
        log "Linking dependencies"
        [ -f ./dwarfs-tools ] && [ ! -h ./dwarfs-tools ] && {
            mv ./dwarfs-tools ./dwarfs
            ln -sfT dwarfs mkdwarfs
            upx dwarfs
        }
        ln -sfT dwarfs dwarfsextract
        upx mksquashfs
        upx objcopy
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
        log "Starting build process for targets: pelf, pelfCreator"
        build_pelf
        build_pelfCreator
        ;;
    "pelf")
        require go
        log "Starting build process for target: pelf"
        build_pelf
        ;;
    "pelfCreator")
        require go
        log "Starting build process for target: pelfCreator"
        build_pelf
        build_pelfCreator
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
        log_warning "Usage: $0 {build|pelfCreator|clean|retrieve|update-deps}"
        exit 1
        ;;
esac
