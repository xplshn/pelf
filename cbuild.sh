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


fetchFromGithub() {
    _repo="$1"
    _tag="$2"
    _fileName="$3"
    _outputPath="$4"

    log "Fetching $_fileName from $_repo:$_tag"

    if [ "$_tag" = "latest" ]; then
        _downloadUrl="https://github.com/$_repo/releases/latest/download/$_fileName"
    else
        _downloadUrl="https://github.com/$_repo/releases/download/$_tag/$_fileName"
    fi

    if curl -fsSL --retry 2 --retry-delay 3 --max-time 120 \
            "$_downloadUrl" -o "$_outputPath" 2>/dev/null && validateDownload "$_outputPath" "$_fileName"; then
        return 0
    fi
    rm -f "$_outputPath"

    log_warning "Direct download failed, trying API method"
    if [ "$_tag" = "latest" ]; then
        _apiUrl="https://api.github.com/repos/$_repo/releases/latest"
    else
        _apiUrl="https://api.github.com/repos/$_repo/releases/tags/$_tag"
    fi
    _tempJson="/tmp/release_info_$$.json"

    if ! curl -fsSL -H "Accept: application/vnd.github.v3+json" \
               "$_apiUrl" -o "$_tempJson" 2>/dev/null; then
        rm -f "$_tempJson"
        log_error "Failed to fetch release info for $_repo:$_tag"
    fi

    if available "jq"; then
        _downloadUrl=$(jq -r ".assets[] | select(.name == \"$_fileName\") | .browser_download_url" "$_tempJson" 2>/dev/null)
    else
        _downloadUrl=$(grep -o "\"browser_download_url\":\"[^\"]*$_fileName\"" "$_tempJson" | cut -d'"' -f4)
    fi
    rm -f "$_tempJson"

    [ -z "$_downloadUrl" ] || [ "$_downloadUrl" = "null" ] && log_error "Asset '$_fileName' not found in $_repo:$_tag"

    if ! curl -fsSL --retry 3 --retry-delay 5 --max-time 300 \
               "$_downloadUrl" -o "$_outputPath" || ! validateDownload "$_outputPath" "$_fileName"; then
        log_error "Failed to download $_fileName"
    fi
}

validateDownload() {
    _file="$1"
    _name="$2"

    [ -f "$_file" ] && [ -s "$_file" ] || { log_error "Downloaded file $_name is empty or doesn't exist"; return 1; }
    file "$_file" 2>/dev/null | grep -q "HTML\|text" && { log_error "Downloaded HTML error page instead of $_name"; return 1; }
    return 0
}

checkElf() {
    _file="$1"
    # Check if file exists
    [ -f "$_file" ] || { log_error "File $_file does not exist"; return 1; }

    if available "xxd"; then
        magic=$(xxd -p -l 4 "$_file" 2>/dev/null)
    elif available "hexdump"; then
        magic=$(hexdump -ve '/1 "%02x"' -n 4 "$_file" 2>/dev/null)
    else
        log_error "Neither xxd nor hexdump is available to check ELF header"
        return 1
    fi

    # Check if the first 4 bytes match ELF magic number (7F454C46)
    [ "$magic" = "7f454c46" ] && return 0
    log_error "$_file is not an ELF file. Do clean and re-run the target to re-download"
    return 1
}

build_appbundle_runtime() {
    log "Building appbundle-runtime variants"
    if [ "$(basename "$(uname -o)")" = "Linux" ]; then
        log "Preparing appbundle-runtime binary dependencies"
        export DBIN_INSTALL_DIR="$BASE/appbundle-runtime/binaryDependencies"
        mkdir -p "$DBIN_INSTALL_DIR"
        # Fetch required tools using curl and dbin
        fetchFromGithub "mhx/dwarfs" "v$DWFS_VER" "dwarfs-fuse-extract-$DWFS_VER-$(basename "$(uname -o)")-$(uname -m).upx" "$DBIN_INSTALL_DIR/dwarfs"
        checkElf "$DBIN_INSTALL_DIR/dwarfs"
        chmod +x "$DBIN_INSTALL_DIR/dwarfs"
        fetchFromGithub "VHSgunzo/squashfuse-static" "latest" "squashfuse_ll-musl-mimalloc-$(uname -m)" "$DBIN_INSTALL_DIR/squashfuse"
        checkElf "$DBIN_INSTALL_DIR/squashfuse"
        chmod +x "$DBIN_INSTALL_DIR/squashfuse"
        dbin add squashfs-tools/unsquashfs
        # UPX the unsquashfs binary
        if available "upx"; then
            log "Compressing unsquashfs for appbundle-runtime"
            checkElf "$DBIN_INSTALL_DIR/unsquashfs"
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
            checkElf "./pelf"
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
    checkElf "$TEMP_DIR/binaryDependencies/pelf"

    # Get the unionfs and bwrap binaries
    mkdir -p "$TEMP_DIR/binaryDependencies"
    DBIN_INSTALL_DIR="$TEMP_DIR/binaryDependencies" dbin add unionfs-fuse3/unionfs bwrap
    checkElf "$TEMP_DIR/binaryDependencies/unionfs"
    checkElf "$TEMP_DIR/binaryDependencies/bwrap"

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
        fetchFromGithub "xplshn/filesystems" "latest" "$RELEASE_NAME" "$TEMP_DIR/binaryDependencies/$RELEASE_NAME"
        cd "$TEMP_DIR/binaryDependencies" || log_error "Failed to change to temp directory"
        ln -sfT "$RELEASE_NAME" "rootfs.tar.${RELEASE_NAME##*.}"
    fi

    if [ ! -f "$TEMP_DIR/binaryDependencies/sharun" ]; then
        log "Downloading sharun-$(uname -m)-aio"
        fetchFromGithub "VHSgunzo/sharun" "latest" "sharun-$(uname -m)-aio" "$TEMP_DIR/binaryDependencies/sharun"
        checkElf "$TEMP_DIR/binaryDependencies/sharun"
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
        checkElf "./pelfCreator"
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
            AppRun* | *.tar* | pkgadd.sh )
                log "Skipping $filename"
                ;;
            *)
                cp "$file" "$TEMP_DIR/binaryDependencies/"
                checkElf "$TEMP_DIR/binaryDependencies/$filename"
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
        fetchFromGithub "xplshn/filesystems" "latest" "$RELEASE_NAME" "$TEMP_DIR/binaryDependencies/$RELEASE_NAME"
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
        checkElf "./appstream-helper"
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
          squashfs-tools/unsquashfs" #squashfuse/squashfuse_ll

    unnappear rm "$DBIN_INSTALL_DIR/dwarfs-tools"
    fetchFromGithub "mhx/dwarfs" "v$DWFS_VER" "dwarfs-universal-$DWFS_VER-Linux-$(uname -m)" "$DBIN_INSTALL_DIR/dwarfs-tools"
    checkElf "$DBIN_INSTALL_DIR/dwarfs-tools"
    chmod +x "$DBIN_INSTALL_DIR/dwarfs-tools"
    fetchFromGithub "VHSgunzo/squashfuse-static" "latest" "squashfuse_ll-musl-mimalloc-$(uname -m)" "$DBIN_INSTALL_DIR/squashfuse_ll"
    checkElf "$DBIN_INSTALL_DIR/squashfuse_ll"
    chmod +x "$DBIN_INSTALL_DIR/squashfuse_ll"

    log "Installing dependencies..."
    # shellcheck disable=SC2086
    dbin add $DEPS
    for dep in $DEPS; do
        dep_name=$(echo "$dep" | cut -d'/' -f2)
        checkElf "$DBIN_INSTALL_DIR/$dep_name"
    done

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
