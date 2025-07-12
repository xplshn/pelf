#!/bin/sh

OPWD="$PWD"
BASE="$(dirname "$(realpath "$0")")"
TEMP_DIR="/tmp/pelf_build_$(date +%s)"
# Change to BASE directory if not already there
if [ "$OPWD" != "$BASE" ]; then
    echo "Changing to $BASE"
    cd "$BASE" || exit 1
fi
trap 'cd "$OPWD"; [ -d "$TEMP_DIR" ] && rm -rf "$TEMP_DIR"' EXIT

# Check if we're really in WWW
if [ ! "$(basename "$BASE")" = "www" ] || [ ! -f "$PWD/config.toml" ]; then
    echo "\"\$(basename \"\$BASE\")\" != \"www\" || \"\$PWD/config.toml\" does not exist"
    exit 1
fi


process_markdown_files() {
    mkdir -p "$2"
    for FILE in "$1"/*.md; do
        if [ ! -f "$FILE" ]; then continue; fi
        if [ "$(basename "$FILE")" = "_index.md" ]; then
            echo "Skipping \"$FILE\""
            cp "$FILE" "./content/docs"
            _GENERATE_EMPTY_INDEX=0
            continue
        fi
        if [ "$(basename "$FILE")" = "index.md" ]; then
            echo "Skipping \"$FILE\""
            continue
        fi
        FILENAME="$(basename "$FILE")"
        DATE="$(git log -1 --format="%ai" -- "$FILE" | awk '{print $1 "T" $2}')"
        # Extract title from first line if it starts with '#'
        TITLE=$(head -n 1 "$FILE" | grep '^#' | sed 's/^# //')
        # Fallback to filename if no valid title found
        [ -z "$TITLE" ] && TITLE="$(basename "$FILE")"
        AUTHOR_NAME="$(git log --follow --format="%an" -- "$FILE" | tail -n 1)"
        AUTHOR_EMAIL="$(git log --follow --format="%ae" -- "$FILE" | tail -n 1)"

        {
            echo "+++"
            echo "date = '$DATE'"
            echo "draft = false"
            echo "title = '$TITLE'"
            echo "[params.author]"
            echo "  name = '$AUTHOR_NAME'"
            echo "  email = '$AUTHOR_EMAIL'"
            echo "+++"
            cat "$FILE"
        } >"$2/$FILENAME"
    done

    if [ "$_GENERATE_EMPTY_INDEX" != "0" ]; then
        echo "Automatically generated an empty \"_index.md\""
        if [ "$(find "$2" -maxdepth 1 -type f | wc -l)" -gt 0 ]; then
            {
                echo "---"
                echo "title: '$3'"
                echo "---"
            } >"$2/_index.md"
        fi
    fi
}


# Start actual processing
rm -rf -- ./content/docs/*
rm -rf -- ./static/assets/*
process_markdown_files "../docs" "./content/docs" "Documentation"
find ../assets/ -type f ! -name '*AppRun*' ! -name '*LAUNCH*' -exec cp {} ./static/assets/ \;
{
    echo "---"
    echo "title: 'Home'"
    echo "---"
} >./content/_index.md
sed 's|src="files/|src="assets/|g' ../README.md >>./content/_index.md

# Build with Hugo
hugo
