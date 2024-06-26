# PELF

## Overview

PELF (Pack an ELF) is a toolset designed to simplify the process of turning your binaries into single-file executables, similar to AppImages. The format used by PELF is called `.AppBundle` or `.blob`. This README provides an overview of the tools included in the PELF project and their functionalities.

## Tools Included

### `pelf` ![pin](assets/pin.svg)
`pelf` is the main tool used to create an `.AppBundle` from your binaries. It takes your executable files and packages them into a single file for easy distribution and execution.

**Usage:**
```sh
./pelf [ELF_SRC_PATH] [DST_PATH.blob] <--add-library [LIB_PATH]> <--add-binary [BIN_PATH]> <--add-metadata [icon128x128.xpm|icon128x128.png|icon.svg|app.desktop]> <--add-arbitrary [DIR|FILE]>
```

### `pelf_linker`
`pelf_linker` is a utility that facilitates access to binaries inside an `.AppBundle` for other programs. It ensures that dependencies within the bundle are properly linked and accessible.

**Usage:**
```sh
pelf_linker [options] <AppBundle>
```

### `pelf_extract`
`pelf_extract` allows you to extract the contents of a PELF bundle to a specified folder. This can be useful for inspecting the contents of a bundle or for modifying its contents.

**Usage:**
```sh
./pelf_extract [input_file] [output_directory]
```

### `pelfd` ![pin](assets/pin.svg)
`pelfd` is a daemon written in Go that automates the installation of `.AppBundle` files. It handles the placement of your AppBundles in the appropriate directories, such as `.local/share/applications` and `.local/share/icons`.

**Usage:**
```sh
pelfd [options]
```

## Overlaying Bundles ![pin](assets/pin.svg)

One of the key features of PELF is its ability to overlay bundles on top of each other. This means that programs inside one bundle can access binaries and libraries from other bundles. For example, if you bundle `wezterm` as a single file and add `wezterm-mux-server` to the same bundle using `--add-binary`, programs run by `wezterm` will be able to see all of the binaries and libraries inside the `wezterm` bundle.

This feature is particularly powerful because you can stack an infinite number of PELF bundles. For instance:

- `spectrwm.blob` contains `dmenu`, `xclock`, and various X utilities like `scrot` and `rofi`.
- `wezterm.blob` contains some Lua programs and utilities.
- `mpv.blob` contains `ani-cli`, `ani-skip`, `yt-dlp`, `fzf`, and `curl`.

Using the `pelf_linker`, the `mpv.blob` can access binaries inside `spectrwm.blob` as well as its own binaries. By doing `mpv.blob --pbundle_link ani-cli`, you ensure that `mpv.blob` can access `ani-cli` and other utilities in the linked bundles.

## Installation ![pin](assets/pin.svg)

To install the PELF toolkit and its associated tools, follow these steps:

1. Clone the repository:
    ```sh
    git clone https://github.com/xplshn/pelf.git ~/Programs
    cd ~/Programs
    rm LICENSE README.md
    cd cmd && go build && mv ./pelfd && cd - # Build the helper daemon
    rm -rf ./cmd ./examples
    ```
2. Add ~/Programs to your $PATH in your .profile or .shrc (.kshrc|.ashrc|.bashrc)

## Usage Examples ![pin](assets/pin.svg)
### Creating an `.AppBundle`
To create an `.AppBundle` from your binaries, use the `pelf` tool:
```sh
./pelf /usr/bin/wezterm wezterm.AppBundle --add-binary /usr/bin/wezterm-mux-server --add-metadata /usr/share/applications/wezterm.desktop --add-metadata ./wezterm128x128.png --add-metadata ./wezterm128x128.svg --add-metadata ./wezterm128x128.xpm
```

### Linking an `.AppBundle`
To make the binaries inside of your (open & overlayed) PELFs visible and usable to other programs, you can use the `pelf_linker` tool:
```sh
pelf_linker ytfzf
```

### Extracting an `.AppBundle`
To extract the contents of an `.AppBundle` to a folder, use the `pelf_extract` tool:
```sh
./pelf_extract openArena.AppBundle ./openArena.bundleDir
```

### Running the `pelfd` Daemon
To start the `pelfd` daemon and have it automatically manage your `.AppBundle` files:
```sh
pelfd &
```

## Contributing
Contributions to PELF are welcome! If you find a bug or have a feature request, please open an Issue. For direct contributions, fork the repository and submit a pull request with your changes.

## License
PELF is licensed under the 3BSD License. See the [LICENSE](LICENSE) file for more details.
