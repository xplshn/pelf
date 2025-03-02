#!/bin/sh

OPWD="$PWD"
BASE="$(dirname "$(realpath "$0")")"
if [ "$OPWD" != "$BASE" ]; then
    echo "... $BASE is not the same as $PWD ..."
    echo "Going into $BASE and coming back here in a bit"
    cd "$BASE" || exit 1
fi
trap 'cd "$OPWD"' EXIT

# Function to log to stdout with green color
log() {
    _reset="\033[m"
    _blue="\033[34m"
    printf "${_blue}->${_reset} %s\n" "$*"
}

# Function to log_warning to stdout with yellow color
log_warning() {
    _reset="\033[m"
    _yellow="\033[33m"
    printf "${_yellow}->${_reset} %s\n" "$*"
}

# Function to log_error to stdout with red color
log_error() {
    _reset="\033[m"
    _red="\033[31m"
    printf "${_red}->${_reset} %s\n" "$*"
    exit 1
}

unnappear() {
    "$@" >/dev/null 2>&1
}

# Check if a dependency is available.
available() {
    unnappear which "$1" || return 1
}

# Exit if a dependency is not available
require() {
    available "$1" || log_error "[$1] is not installed. Please ensure the command is available [$1] and try again."
}

download_bwrap() {
    log "Downloading bwrap"
    if ! wget -qO "./bwrap" "https://bin.pkgforge.dev/$(uname -m)/bwrap-patched"; then
        log_error "Unable to download bwrap"
    fi
    chmod +x "./bwrap"
}

build_project() {
    if [ -f "./pelf.go" ]; then
    	if [ ! -f "./appbundle-runtime/runtime" ]; then
			log "appbundle-runtime executable not found"
			if [ -d "./appbundle-runtime" ]; then
				cd ./appbundle-runtime && log "Building appbundle-runtime" && {
					go build -o ./runtime
				}
				cd ..
			fi
    	fi
    	if available "strip"; then
    		log "Stripping debug symbols from ./appbundle-runtime/runtime"
    		strip -s --strip-all ./appbundle-runtime/runtime
    	else
    		log_warning "strip tool not found, unable to remove debug sections from the runtime"
    	fi

		rm -f ./pelf
		export CGO_ENABLED=0
		export GOFLAGS="-ldflags=-static-pie -ldflags=-s -ldflags=-w"
		go build -o ./pelf || log_error "Unable to build ./pelf"

		if available "upx"; then
			log "Compressing ./pelf tool"
			upx ./pelf || log_error "unable to compress ./pelf"
			rm -f ./pelf.upx
		else
			log_warning "upx not available. The resulting binary will be unecessarily large"
		fi
    fi
}

clean_project() {
    log "Starting clean process"
    rm ./pelf
    rm ./pelf.upx
    log "Clean process completed"
}

retrieve_executable() {
    readlink -f ./pelf
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
    *)
        log_warning "Usage: $0 {build|clean|retrieve}"
        exit 1
        ;;
esac
