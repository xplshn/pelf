### PELF
> PELF is an executable packaging format designed to pack applications, toolchains, window managers, and multiple programs into a single portable file.

PELF can serve as a drop-in replacement for AppImages. Both PELF and AppImages utilize the AppDir specification, making it easy to unpack an AppImage and re-package it as an AppBundle.

#### Advantages
- **Dwarfs Compression**: PELF uses Dwarfs by default, which generally performs better than SquashFS. Performance can be further optimized with advanced compression options such as PCMAUDIO ordering and FLAC compression.
- **Simplicity**: PELF is a minimalistic SH script that efficiently accomplishes the task. The resulting `.AppBundle` is a self-mounting archive created in POSIX SH, making it hackable, flexible, and easy to debug.
- **Custom Compression**: PELF can be configured to use `tar.gz` or `SquashFS` (see other branches/files under editions/).
- **Flexibility of AppBundles**: AppBundles do not force compliance with the AppDir standard. For example, you can bundle window managers and basic GUI utilities into a single file (as done with `Sway.AppBundle`). You can even package toolchains as single-file executables.
- **Endless Possibilities**: With a custom AppRun script, you can create versatile `.AppBundles`. For instance, packaging a Rick Roll video with a video player that works on both glibc and musl systems is straightforward. You can even generate AppBundles that overlay on top of each other.
- **Multi-Arch Compatibility**: The `.AppBundle` file is identified as a `sh` script, which can be executed on any architecture and operating system, making it easy to create multi-architecture AppBundles.
- **Complete tooling**: The `pelfd` daemon (and its GUI version) are available for use as system integrators, they're in charge of adding the AppBundles that you put under ~/Applications in your "start menu". This is one of the many programs that are part of the tooling, another great tool is pelfCreator, which lets you create programs via simple one-liners (by default it uses an Alpine rootfs + bwrap, but you can get smaller binaries via using -x to only keep the binaries you want), a one-liner to pack Chromium into a single-file executable looks like this: `pelfCreator --maintainer "xplshn" --name "org.chromium.Chromium" --pkg-add "chromium" --entrypoint "chromium.desktop"`

### Usage
```
pelf --add-appdir ./myApp.AppDir --appbundle-id myApp-16-10-2024-xplshn --output-to ./myApp.AppBundle --embed-static-tools
```
### Usage of the Resulting `.AppBundle`
> By using the `--pbundle_link` option, you can access files contained within the `./bin` or `./usr/bin` directories of an `.AppBundle`, inheriting environment variables like `PATH`. This allows multiple AppBundles to stack on top of each other, sharing libraries and binaries across "parent" bundles.

#### Explanation
You specify an `AppDir` to be packed and an ID for the app. This ID will be used when mounting the `.AppBundle` and should include the packing date, the project or program name, and the maintainer's information. While you can choose an arbitrary name, it’s not recommended.

Additionally, we embed the tools used for mounting and unmounting the `.AppBundle`, such as `dwarfs` and `fusermount`, when using `pelf-dwfs`.

![image](https://github.com/user-attachments/assets/f4459934-a5b6-4717-8299-86b56dc0cf48)

###### MISC:
- There's a runtime in shell, this one is used by default.
- There's a runtime made in Go, for those of you that want speed (21~ms improvement)

###### Planned
- Runtime in ODIN (mostly complete, but I'm too embarrased to share the code, it looks ugly)
- Runtime in Zig (not yet started)
- Make the runtimes support various formats at the same time? (sqfs, others?)
- AppImage type II flags, for compat with existing daemons (I'm salty about adding this. Very much so. Perhaps we could do this in a modular way that can be turned on/off?.. would shell be acceptable? What about `yaegis`? Or something else..?)

#### Resources:
- The [AppBundleHUB](https://github.com/xplshn/AppBundleHUB) a repo which builds a ton of portable AppBundles in an automated fashion, using GH actions. (we have a [webStore](https://fatbuffalo.neocities.org/AppBundleHUBStore) too, tho that is WIP)
- `[dbin])(https://github.com/xplshn/dbin)` a self-contained, portable, statically linked, package manager, 3105 binaries (portable, self-contained/static) are available in its repos at the time of writting. Among these, are the AppBundles from the AppBundleHUB and from pkgforge
