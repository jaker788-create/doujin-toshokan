# PyInstaller spec for Doujin Bunko.
#
# Build with:  pyinstaller doujin.spec
# Output:      dist/doujin.exe (Windows) / dist/doujin (macOS, Linux)

from PyInstaller.utils.hooks import collect_submodules

# uvicorn dispatches its worker class / loop / lifespan implementations by
# string at runtime ("uvicorn.protocols.http.h11_impl", "uvicorn.loops.asyncio",
# etc.), so PyInstaller's static-import analyzer doesn't see them. Pull the
# whole package in by name to be safe.
hiddenimports = collect_submodules("uvicorn")

a = Analysis(
    ["doujin/__main__.py"],
    pathex=[],
    binaries=[],
    # Templates and static assets are loaded at runtime via
    # Path(__file__).parent / "templates" (see doujin/app.py). Mirror the
    # package layout inside the bundle so that path resolution keeps working.
    datas=[
        ("doujin/templates", "doujin/templates"),
        ("doujin/static", "doujin/static"),
    ],
    hiddenimports=hiddenimports,
    hookspath=[],
    hooksconfig={},
    runtime_hooks=[],
    excludes=[],
    noarchive=False,
    optimize=0,
)

pyz = PYZ(a.pure)

exe = EXE(
    pyz,
    a.scripts,
    a.binaries,
    a.datas,
    [],
    name="doujin",
    debug=False,
    bootloader_ignore_signals=False,
    strip=False,
    upx=True,
    upx_exclude=[],
    runtime_tmpdir=None,
    console=True,
    disable_windowed_traceback=False,
    argv_emulation=False,
    target_arch=None,
    codesign_identity=None,
    entitlements_file=None,
)
