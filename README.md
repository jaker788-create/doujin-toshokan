Doujin Toshokan is an app to organize all the digital doujin/manga you've accumulated. Browse and search your collection by keyword, title, or artist, and read titles in a scrollable viewer. Runs entirely offline and indexes your files in place — nothing ever gets moved, renamed, or modified.

This is a personal project, it's not meant to be a complete or "official" release — but there's no harm in it being public and open source unlike APM-Master. Wanna contribute? Go for it.

## Run the bundled release

Bundled ARM64 Windows release: https://github.com/jaker788-create/doujin-toshokan/releases — download `doujin.exe` and run it. Windows 11 already includes the WebView2 runtime it renders into; on Windows 10, install the Evergreen WebView2 runtime if prompted.

First launch creates `%APPDATA%\doujin\config.json`. Open the **Scan / Ingest** page and use **Add folder…** to point it at the folders your collection lives in (e.g. `C:\Manga`), then **Import all** (or import titles one at a time).

## Run from source

You'll need **Go 1.25+**, **Node 18+**, and the **Wails CLI**. From PowerShell:

```powershell
winget install GoLang.Go                                   # if you don't have Go
go install github.com/wailsapp/wails/v2/cmd/wails@latest   # the Wails CLI
git clone https://github.com/jaker788-create/doujin-toshokan.git
cd doujin-toshokan
wails dev          # run with hot reload, or:
wails build        # produce build\bin\doujin.exe
```

`wails build -platform windows/arm64` targets ARM64; drop `-platform` to build for your host. Run `wails doctor` to check prerequisites (Go, Node, WebView2).

## Legacy Python build

The original Python/FastAPI implementation lives on the [`legacy`](https://github.com/jaker788-create/doujin-toshokan/tree/legacy) branch and reads the same library and database. To run it instead, check that branch out — a worktree keeps it alongside the Go build:

```powershell
git worktree add ../doujin-legacy legacy
cd ../doujin-legacy
python -m venv .venv
.venv\Scripts\Activate.ps1     # macOS/Linux: source .venv/bin/activate
pip install .
doujin                          # serves http://127.0.0.1:8765
```
