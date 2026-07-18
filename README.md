Doujin Toshokan is an app to organize all the digital doujin/manga you've accumulated. Browse and search your collection by keyword, title, or artist, and read titles in a scrollable viewer. Runs entirely offline and indexes your files in place — nothing ever gets moved, renamed, or modified.

This is the pre Go rewrite version. Legacy code, use as you wish if it has any foundation you want to build off.

Bundled release for ARM64 https://github.com/jaker788-create/doujin-toshokan/releases/tag/v0.2.0
There is no bundled x86-64 release, but the source can be ran anywhere with the python dependancies installed. You'll need Python 3.11+ and git. From PowerShell:

```powershell
git clone https://github.com/jaker788-create/doujin-toshokan.git
cd doujin-toshokan
python -m venv .venv
.venv\Scripts\Activate.ps1
pip install .
doujin
```

On macOS or Linux, swap the activate step for `source .venv/bin/activate`. First launch creates a config file (Windows: `%APPDATA%\doujin\config.json`) — set `library_roots` to the folders your collection lives in, e.g. `["C:\\Manga"]`, and run `doujin` again. The library opens at http://127.0.0.1:8765.
