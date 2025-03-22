#!/bin/sh

[ "$DEBUG" = "1" ] && set -x

OPWD="$PWD"
BASE="$(dirname "$(realpath "$0")")"
export DBIN_INSTALL_DIR="$BASE/binaryDependencies"
export DBIN_NOCONFIG="1"

# Change to BASE directory if not already there
if [ "$OPWD" != "$BASE" ]; then
    echo "Changing to $BASE"
    cd "$BASE" || exit 1
fi
trap 'cd "$OPWD"' EXIT

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
build_project() {
    if [ -f "./pelf.go" ]; then
        mkdir -p "$DBIN_INSTALL_DIR"
        handle_dependencies

        #if [ ! -f "$DBIN_INSTALL_DIR/appbundle-runtime" ]; then
        #    log "appbundle-runtime executable not found in $DBIN_INSTALL_DIR"
            if [ -d "./appbundle-runtime" ]; then
                cd ./appbundle-runtime && {
                	log "Building appbundle-runtime"
                    go build || log_error "./appbundle-runtime could not be built by go command"
		    [ "$DEBUG" = "1" ] && ls

                    available "strip" || log_warning "strip tool not found, unable to remove debug sections from the runtime" && {
                        log "Stripping debug symbols from ./appbundle-runtime"
                        strip -s --strip-all ./appbundle-runtime || log_error "Strip of ./appbundle-runtime failed"
                    }

                    log "Moving appbundle-runtime to $DBIN_INSTALL_DIR"
                    mv ./appbundle-runtime "$DBIN_INSTALL_DIR"
                }
                cd "$BASE" || log_error "Unable to go back to $BASE"
            fi
        #fi

		log "Creating binaryDependencies.tar.zst"
		tar -C binaryDependencies -c . | zstd -T0 -19 -fo binaryDependencies.tar.zst
		#tar -C binaryDependencies -c . | zstd -T0 -fo binaryDependencies.tar.zst

        rm -f ./pelf
        export CGO_ENABLED=0
        export GOFLAGS="-ldflags=-static-pie -ldflags=-s -ldflags=-w"
        go build -o ./pelf || log_error "Unable to build ./pelf"

	available "upx" || log_warning "upx not available. The resulting binary will be unnecessarily large"
        available "upx" && {
            log "Compressing ./pelf tool"
            upx ./pelf || log_error "unable to compress ./pelf"
            rm -f ./pelf.upx
        }
    else
        log_error "./pelf.go not found."
    fi
}

clean_project() {
    log "Starting clean process"
    rm -rf ./pelf ./pelf.upx ./binaryDependencies ./binaryDependencies.tar.zst
    log "Clean process completed"
}

retrieve_executable() {
    readlink -f ./pelf
}

handle_dependencies() {
    mkdir -p "$DBIN_INSTALL_DIR"
	DEPS="dwarfs/dwarfs-tools
		  unionfs-fuse3/unionfs
		  squashfs-tools/unsquashfs
		  squashfs-tools/mksquashfs
		  squashfuse/squashfuse_ll
		  bintools/objcopy#nixpkgs.bintools"

    if [ -n "$(ls -A "$DBIN_INSTALL_DIR" 2>/dev/null)" ]; then
        log "Updating dependencies..."
        dbin update
    else
        log "Installing dependencies..."
        # shellcheck disable=SC2086
        dbin add $DEPS
    fi

    cd "$DBIN_INSTALL_DIR" && {
        log "Linking dependencies"
        [ -f ./dwarfs-tools ]  && [ ! -h ./dwarfs-tools ]  && mv ./dwarfs-tools     ./dwarfs
        ln -sfT                            dwarfs                                     mkdwarfs
        ln -sfT                            dwarfs                                     dwarfsextract
        upx                                dwarfs
        upx                                mksquashfs
        upx                                objcopy
        [ -f ./squashfuse_ll ] && [ ! -h ./squashfuse_ll ] && mv ./squashfuse_ll    ./squashfuse
        ln -sfT                            squashfuse                                 squashfuse_ll
        ln -sfT                            /usr/bin/fusermount                        fusermount
        ln -sfT                            /usr/bin/fusermount3                       fusermount3
    }
    cd "$BASE" || log_error "Unable to go back to $BASE"
}

update_dependencies() {
    dbin update
}

# Main case statement for actions
case "$1" in
    "" | "build")
        require go
        log "Starting build process"
        build_project
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
        log_warning "Usage: $0 {build|clean|retrieve|update-deps}"
        exit 1
        ;;
esac
