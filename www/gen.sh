#!/bin/sh

check_directory() {
    if [ ! "$(basename "$PWD")" = "www" ] || [ ! -f "$PWD/config.toml" ]; then
        if [ -d "$PWD/www" ]; then
            echo "You must enter ./www"
        else
            echo "Where the fuck are we? You must enter https://github.com/xplshn/alicelinux/www, run me within of the ./www directory!"
        fi
        exit 1
    fi
}

process_markdown_files() {
    mkdir -p "$2"
    for FILE in "$1"/*.md; do
    	if [ "$FILE" = "_index.md" ]; then
			echo "Skipping \"$FILE\""
    	fi
        FILENAME="$(basename "$FILE")"
        DATE="$(git log -1 --format="%ai" -- "$FILE" | awk '{print $1 "T" $2}')"
        TITLE="$(basename "$FILE")"
        AUTHOR_NAME="$(git log --follow --format="%an" -- "$FILE" | tail -n 1)"
        AUTHOR_EMAIL="$(git log --follow --format="%ae" -- "$FILE" | tail -n 1)"

		case "$TITLE" in
        "_index.md")
            cp "$FILE" "./content/docs"
            continue
            ;;
         "index.md")
         	echo "Skipping \"$FILE\""
            continue
            ;;
    	esac

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

    # Disabled because I manually created the ./content/docs/_index.md
    #if [ "$(find "$2" -maxdepth 1 -type f | wc -l)" -gt 0 ]; then
    #    {
    #        echo "---"
    #        echo "title: '$3'"
    #        echo "---"
    #    } >"$2/_index.md"
    #fi
}

# Main script execution
check_directory
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
