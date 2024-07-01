# PELF
<!-- [![Go Report Card](https://goreportcard.com/badge/github.com/xplshn/pelf)](https://goreportcard.com/report/github.com/xplshn/pelf) -->
![License](https://img.shields.io/github/license/xplshn/pelf)
![GitHub code size in bytes](https://img.shields.io/github/languages/code-size/xplshn/pelf)

PELF (Pack an ELF) is a toolset designed to simplify the process of turning your binaries into single-file executables, similar to AppImages. The format used by PELF is called `.AppBundle` or `.blob`. The PELF files are portable across systems of the same architecture and ABI. Architecture and LIBC-independent bundles can be achieved using Wrappers.

If you only intend on using .AppBundles, not necesarily work with them, you don't need any of this. You can get started by simply executing the bundle. The helper daemon is optional. NOTE: Your tar command must support GZIP archives. (which covers most tar implementations, including the BSDs and Busybox's)
![2024-07-01-023335_1280x720_scrot](https://github.com/xplshn/pelf/assets/114888778/888033cc-8759-4990-b193-5f870ad639f0)

## Example AppBundles/Binaries for you to try (amd64):
- POSIX = Runs on any Unix clone that has some degree of POSIX compatibility/compliance
- MUSL  = Runs on Musl-based Linux distros
- GLIBC = Runs on Glibc-based Linux distros
- Linux = Runs on any Linux distro.
These are .small.AppBundle, so these are (VERY) compressed and do require gunzip to be available, most if not all Linux distros have it.
###### If you don't know what this means, you should select Glibc.
- FreeDOOM. [GLIBC](https://github.com/xplshn/pelf/raw/master/examples/freedoom_amd64LinuxGlibc.small.AppBundle?download=) - [MUSL](https://github.com/xplshn/pelf/raw/master/examples/freedoom_amd64LinuxMusl.small.AppBundle?download=)
- Chocolate-Doom (server + client) [GLIBC](https://github.com/xplshn/pelf/raw/master/examples/chocolate-doom_amd64LinuxGlibc.small.AppBundle?download=) - [MUSL](https://github.com/xplshn/pelf/raw/master/examples/chocolate-doom_amd64LinuxMusl.small.AppBundle?download=)
- MPV + ROFI + ANI-CLI + YT-DLP. [GLIBC](https://github.com/xplshn/pelf/raw/master/examples/mpv_amd64LinuxGlibc.small.AppBundle?download=) - [MUSL](https://github.com/xplshn/pelf/raw/master/examples/mpv_amd64LinuxMusl.small.AppBundle?download=)
- Zig v0.14.0 (language and C compiler). [LINUX](https://github.com/xplshn/pelf/raw/master/examples/zig_amd64Linux.small.AppBundle?download=)
- Go v1.22.4 (entire Go toolchain). [LINUX](https://github.com/xplshn/pelf/raw/master/examples/go_amd64Linux.small.AppBundle?download=)
- Obfuscated RickRoll. [POSIX](https://github.com/xplshn/pelf/raw/master/examples/rickRoll.any.AppBundle?download=)

## Tools Included
### `pelf` ![pin](assets/pin.svg)
`pelf` is the main tool used to create an `.AppBundle` from your binaries. It takes your executable files and packages them into a single file for easy distribution and execution.

**Usage:**
```sh
./pelf [ELF_SRC_PATH] [DST_PATH.blob] <--add-library [LIB_PATH]> <--add-binary [BIN_PATH]> <--add-metadata [icon128x128.xpm|icon128x128.png|icon.svg|app.desktop]> <--add-arbitrary [DIR|FILE]>
```

### `pelf_linker`
`pelf_linker` is a utility that facilitates access to binaries inside an `.AppBundle` for other programs. It ensures that binaries and dependencies within the (open, overlayed) bundles are accesible to external programs. This relies on the concept of Bundle Overlays.

**Usage:**
```sh
pelf_linker [--export] [binary]
```

### `pelf_extract`
`pelf_extract` allows you to extract the contents of a PELF bundle to a specified folder. This can be useful for inspecting the contents of a bundle or for modifying its contents.

**Usage:**
```sh
./pelf_extract [input_file] [output_directory]
```

### `pelfd` ![pin](assets/pin.svg)
`pelfd` is a daemon written in Go that automates the "installation" of `.AppBundle` files. It automatically puts the metadata of your .AppBundles in the appropriate directories, such as `.local/share/applications` and `.local/share/icons`. So that the .AppBundles you put in ~/Programs (for example) will appear in your menus.

**Usage:**
```sh
pelfd &
```

## Overlaying Bundles ![pin](assets/pin.svg)
One of the key features of PELF is its ability to overlay bundles on top of each other. This means that programs inside one bundle can access binaries and libraries from other bundles. For example, if you bundle `wezterm` as a single file and add `wezterm-mux-server` to the same bundle using `--add-binary`, programs run by `wezterm` will be able to see all of the binaries and libraries inside the `wezterm` bundle.

This feature is particularly powerful because you can stack an infinite number of PELF bundles. For instance:

- `spectrwm.AppBundle` contains `dmenu`, `xclock`, and various X utilities like `scrot` and `rofi`.
- `wezterm.AppBundle` contains some Lua programs and utilities.
- `mpv.AppBundle` contains `ani-cli`, `ani-skip`, `yt-dlp`, `fzf`, and `curl`.

Using the `pelf_linker`, the `mpv.AppBundle` can access binaries inside `spectrwm.AppBundle` as well as its own binaries. By doing `mpv.AppBundle --pbundle_link ani-cli`, you can launch an instance of the `ani-cli` included in the bundle, as well as ensure that it can access other utilities in the linked/stacked bundles.

![SpectrWM window manager AppBundle/.blob that contains all of my X utils including Wezterm](https://github.com/xplshn/pelf/assets/114888778/b3b99c24-825d-4be0-a1c8-ea9433776692)
As you can see, I have my window manager and all of my X utilities, including my terminal (Wezterm) as a single file, named SpectrWM.AppBundle. You can also see the concept of overlays in action, the `ani-cli` binary inside of the mpv.AppBundle, will have access to the ROFI binary packed in my SpectrWM.AppBundle, because it will be running as a child of that process. There is PATH and LD_LIBRARY_PATH inheritance.

## Installation ![pin](assets/pin.svg)
To install the PELF toolkit, follow these steps:

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

### Using binaries inside of an `.AppBundle` from outside it, or from other programs
To leverage access to the binaries inside of your (open & overlayed) PELFs to other programs, you can use the `pelf_linker` tool:
```sh
pelf_linker ytfzf
```
Overlayed access: Here we made ytfzf be able to access all the PATHs set in $PELF_BINDIRS
```sh
mpv.AppBundle --pbundle_link ytfzf
```
Scoped access: In this other example, we made ytfzf gain access to the programs inside of the example mpv.AppBundle.

### Extracting an `.AppBundle`
To extract the contents of an `.AppBundle` to a folder, use the `pelf_extract` tool:
```sh
./pelf_extract openArena.AppBundle ./openArena_bundleDir
```

### Running the `pelfd` Daemon
To start the `pelfd` daemon and have it automatically manage your `.AppBundle` files:
```sh
pelfd &
```
On the first-run, it will create a config file which you can modify:
**~/.config/pelfd.json**, this is how your config would look after the first run:
```json
{
  "options": {
    "directories_to_walk": [
      "~/Programs"
    ],
    "probe_interval": 90,
    "icon_dir": "/home/anto/.local/share/icons",
    "app_dir": "/home/anto/.local/share/applications",
    "probe_extensions": [
      ".AppBundle",
      ".blob"
    ]
  },
  "tracker": {}
}
```
- `"directories_to_walk"`: This is an array of directories that the `pelfd` daemon will monitor for `.AppBundle` or `.blob` files. By default, it is set to `["~/Programs"]`, meaning the daemon will only check for AppBundles in the `~/Programs` directory. You can add more directories to this array if you want the daemon to watch multiple locations.
- `"probe_interval"`: This specifies the interval in seconds at which the `pelfd` daemon will check the specified directories for new or modified AppBundles. By default, it is set to `90` seconds.
- `"icon_dir"`: This is the directory where icons extracted from .AppBundles will be copied to `pelfd`. By default, it is set to `"~/.local/share/icons"`, which is a common location for application icons on modern Unix clones.
- `"app_dir"`: This is the directory where the desktop files extracted from .AppBundles will be copied to by `pelfd`. By default, it is set to `"~/.local/share/applications"`. `.desktop` files provide information about the application, such as its name, icon, and command to execute. By default, it is set to `"~/.local/share/applications"`, which is the standard location for application shortcuts on modern Unix clones.
- `"probe_extensions"`: This is an array of file extensions that `pelfd` will look for when probing the specified directories. By default, it is set to `[".AppBundle", ".blob"]`, meaning the daemon will only consider files with these extensions as AppBundles. (CASE-SENSITIVE)
- The "tracker" object in the config file is used to store information about the tracked AppBundles, this way, when an AppBundle is removed, its files can be safely "uninstalled", etc.
- `"correct_desktop_files"`: This option makes PELFD automatically correct the .desktop files provided by the bundles. So that they can be found by your menu application.

### Editions!
.AppBundles may come archived with different formats or encodings. For example, the included `pelf_small` edition, will create bundles without using base64 and using GZIP directly with -9, for the best available compression, it may even end up being faster, due to avoiding base64. Remember to ALWAYS signal which edition a bundle was made with by adding it to the bundle's name! For example, .raw.AppBundle if your PELF tool was patched/modified to remove base64 encoding, .small.AppBundle if you used the pelf_small example included here.

### Current roadmap:
 1. Versioning.
 2. Optionally provide the option to mount the TAR archive instead of instead of copying the files. Or replace TAR altogether for a format that is also widely available.
 3. Employ the same tricks that the APE loader uses to be recognized as an ELF, preferably, implement a tool that can be used to turn any SH script into a (fake) "ELF".
 4. Simplify everything by splitting the loader into a very barebones loader and some helper binaries written in shell, for example, the embedded thumbnail generator could be one such helper. The idea being that even if the user isn't able to run the .AppBundle, he can always extract it and repackage it again without having to start from scratch.
 5. Consider using the AppDir layout, since it is well-thought out and .AppImages already work this way, thus allowing us to contribute.

### Current setbacks:
  - Code has to be readable to stay hackable, given that this is SH, it may end up being an unbearably disgusting mess, so I have to be specially careful.
  - Helpers to pack QT and other programs made with intricate toolkits probably won't be supported because of the lack of manpower, and the inability to piggyback from AppDirTool.go since the code is unreadable, at least for me, it is too deeply tied to the Appimage ecosystem. I'd be great if it were independent enough to be compiled/installed with just `go install`.
  - My time to work on PELFd is limited, and there are no other projects like this one apart from AppImages, and I can't benefit much from that ecosystem

###### Read these two if you are interested in how APE works:
1. https://justine.lol/ape.html
2. https://github.com/jart/cosmopolitan/blob/master/ape/loader.c
3. https://github.com/jart/cosmopolitan/blob/master/ape/ape.S

## Contributing
Contributions to PELF are welcome! If you find a bug or have a feature request, please open an Issue. For direct contributions, fork the repository and submit a pull request with your changes.

## License
PELF is licensed under the 3BSD License. See the [LICENSE](LICENSE) file for more details.

### Special thanks to:
- [Chris DeBoy](https://codeberg.org/chris_deboy)
- [Jade Mandelbrot+ for writting the README.md](https://hf.co/chat/assistant/667646ec7dd4a03abaf916f4)
