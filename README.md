### PELF
An Executable packaging format which can be used to pack applications, toolchains, window managers, and more than 1 program into a single portable file.

PELF can be used as a drop-in replacement for AppImages, both PELF and AppImages use the AppDir spec, so unpacking an AppImage and re-packaging it as an AppBundle is trivial

#### Advantages
- PELF uses Dwarfs by default, which should perform a bit better than Squashfs, this can be further enhanced by using better compression options such as PCMAUDIO ordering and FLAC compression
- Simplicity, PELF is a very concise SH script that gets the job done, and the resulting .AppBundles are also a self-"extracting" (mounting, not extracting) archive made in POSIX SH. This has the advantage of being hackable, flexible and easy to debug.
- PELF can be adapted to use tar.gz or squashfs (see the other branches)
- AppBundles have the advantage of being very flexible, they don't force you to comply with the AppDir standard, thus, you can even ship Window managers + basic GUI utils in a single file, like I have with Sway.AppBundle, you can even ship Toolchains as single-file executables
- The possibilities are endless if you're able to provide an AppRun that does what you want, for example, creating an .AppBundle that contains a rick roll video + a video player and works on both glibc/musl systems is trivial
- Creating a multi-arch AppBundle is trivial, since the file is identified as a `sh` script and can be excuted under any arch & os

#### Drawbakcs
- As much of an advantage as `sh` is, it is certainly not ideal, a Go or C version of `pelf-dwfs` could appear in the future alongside the `sh` version...
- No existing thumbnailers support the `.AppBundle` format, thus, `pelfd` has to be used for integration, `pelfd` would be the equivalent of `appimaged`

### Usage
```
pelf --add-appdir ./myApp.AppDir myApp-28-09-2024-xplshn --output-to ./myApp.AppBundle --embed-static-tools
```
#### Explanation
We specify an appdir to be packed and an ID for the app, this id will be used for mounting the appbundle, it should contain the date on which the archive was packed, the name of the project or program packed and the maintainer of the .AppBundle, you may choose to ignore this and go with an arbitrary name, but it isn't recommended to do so.
We also embed the tools used for mounting and umounting the `.AppBundle`, which in the case of `pelf-dwfs` would be `dwarfs` & `fusermount`
