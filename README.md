### PELF
> PELF is an executable packaging format designed to pack applications, toolchains, window managers, and multiple programs into a single portable file.

PELF can serve as a drop-in replacement for AppImages. Both PELF and AppImages utilize the AppDir specification, making it easy to unpack an AppImage and re-package it as an AppBundle.

#### Advantages
- **Dwarfs Compression**: PELF uses Dwarfs by default, which generally performs better than SquashFS. Performance can be further optimized with advanced compression options such as PCMAUDIO ordering and FLAC compression.
- **Simplicity**: PELF is a minimalistic SH script that efficiently accomplishes the task. The resulting `.AppBundle` is a self-mounting archive created in POSIX SH, making it hackable, flexible, and easy to debug.
- **Custom Compression**: PELF can be configured to use `tar.gz` or `SquashFS` (see alternative branches).
- **Flexibility of AppBundles**: AppBundles do not force compliance with the AppDir standard. For example, you can bundle window managers and basic GUI utilities into a single file (as done with `Sway.AppBundle`). You can even package toolchains as single-file executables.
- **Endless Possibilities**: With a custom AppRun script, you can create versatile `.AppBundles`. For instance, packaging a Rick Roll video with a video player that works on both glibc and musl systems is straightforward.
- **Multi-Arch Compatibility**: The `.AppBundle` file is identified as a `sh` script, which can be executed on any architecture and operating system, making it easy to create multi-architecture AppBundles.

#### Drawbacks
- **Limitations of SH**: While `sh` is powerful, it’s not the most ideal choice for every situation. A Go or C implementation of `pelf-dwfs` might be developed in the future alongside the `sh` version.
- **Lack of Thumbnailer Support**: Currently, no existing thumbnailers support the `.AppBundle` format. Therefore, `pelfd` must be used for integration, acting as the equivalent of `appimaged`.

### Usage
```
pelf --add-appdir ./myApp.AppDir myApp-28-09-2024-xplshn --output-to ./myApp.AppBundle --embed-static-tools
```
### Usage of the Resulting `.AppBundle`
> By using the `--pbundle_link` option, you can access files contained within the `./bin` or `./usr/bin` directories of an `.AppBundle`, inheriting environment variables like `PATH`. This allows multiple AppBundles to stack on top of each other, sharing libraries and binaries across "parent" bundles.

#### Explanation
You specify an `AppDir` to be packed and an ID for the app. This ID will be used when mounting the `.AppBundle` and should include the packing date, the project or program name, and the maintainer's information. While you can choose an arbitrary name, it’s not recommended.

Additionally, we embed the tools used for mounting and unmounting the `.AppBundle`, such as `dwarfs` and `fusermount`, when using `pelf-dwfs`.

![image](https://github.com/user-attachments/assets/97c58d55-293a-4f32-81a9-2f9738e0cb77)
