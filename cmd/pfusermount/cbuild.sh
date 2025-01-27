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

download() {
    log "Downloading $1"
    if ! wget -U "dbin" -O "./$(basename "$1")" "https://bin.pkgforge.dev/$(uname -m)_$(uname)/$1"; then
        log_error "Unable to download [$1]"
    fi
    chmod +x "./$1"
}

build_project() {
    # FUSERMOUNT
    log 'Compiling "pfusermount"'
    go build -o ./fusermount ./fusermount.go
    # FUSERMOUNT3
    log 'Compiling "pfusermount3"'
    go build -o ./fusermount3 ./fusermount3.go
}

clean_project() {
    log "Starting clean process"
    echo "rm ./*fusermount"
    unnappear rm ./*fusermount
    echo "rm ./*fusermount3"
    unnappear rm ./*fusermount3
    log "Clean process completed"
}

retrieve_executable() {
    readlink -f ./pfusermount
    readlink -f ./pfusermount3
}

# Main case statement for actions
case "$1" in
    "" | "build")
        require go
        #log "Checking if embeddable assets are available"
        #if [ ! -f "./fusermount" ] || [ ! -f "./fusermount3" ]; then
            log "Procuring embeddable goodies"
            download "Baseutils/fuse/fusermount"
            download "Baseutils/fuse3/fusermount3"
        #fi
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
