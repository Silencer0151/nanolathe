# Windows shortcut icon

`Nanolathe.ico` uses the same MIT-licensed Nanolathe website mark as the Mac
app, with 16, 24, 32, 48, 64, 128 and 256 pixel images. Its vector source is
[`../macos/icon.svg`](../macos/icon.svg), copyright 2026 Nanolathe contributors.
Rasterize that SVG at each size and package those images as a multi-image ICO
to regenerate it. Icon generation is an authoring step; players need no image
conversion software.

The PowerShell source installer copies the ICO from the resolved source
archive into the candidate release before promotion. Its Start Menu shortcut
names that installed file as its icon, independently of its PowerShell launch
target, and updates replace the shortcut's icon reference alongside the
release selection.

Release builds (`tools/native_build.py`) instead compile the ICO into the
version resource of `nanolathe.exe` and `nanolathe-launcher.exe`. The
launcher's shortcuts name the installed launcher executable's own icon, so
no separate ICO is installed, and `icon.go` embeds the same file for the
launcher's window icon.
