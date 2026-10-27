# Tavab desktop client (Tauri v2)

A thin one-window **desktop GUI** around the existing remote Tavab web UI. There is no Go
runtime bundled here -- the server already runs 24/7, so this app only opens a Webview that
loads its URL and shows it with a one-click application tray on the Linux taskbar/dock.

## Architecture
- `src-tauri/src/main.rs` -- decides **which remote server** to point at. Single source of truth
  for targets; no config editing or rebuild needed to switch sites/URLs.
- `src-tauri/src/lib.rs` -- builds the Tauri v2 client: one window + a system tray (Show / Quit).
  Navigates the window to the resolved URL and falls back to an offline page if the target is
  missing, non-http, or unreachable (no hard crash).
- `src-tauri/assets/index.html` -- offline fallback page shown while connecting or when the
  backend/network is down.
- No extra crates beyond what Tauri brings (plus the optional `tray-icon` feature), so build
  times stay predictable for CI.

## Which server to load (resolution order, last wins)
1. CLI flag: launch with `--base <http(s)://host[:port]/` to point at any deployed site without
   editing code or rebuilding.
2. Env var: set `tavernlab_server_url` (e.g. in a desktop shortcut/launcher) for the same effect.
3. Built-in default: the LAN endpoint that runs 24/7 on this box.

Examples:
```bash
./target/release/app                     # uses the built-in LAN default
./target/release app --base https://host # per-invocation override
tavernlab_server_url=https://host ./target/release/app
```

## Build
```bash
cd src-tauri
cargo build --release          # or: cargo tauri build  (produces AppImage + deb)
```
The release binary is at `src-tauri/target/release/app`. `cargo tauri build` also produces
`tavernlab-desktop_0.1.0_amd64.AppImage` and `..._amd64.deb` under
`src-tauri/target/release/bundle`.

## Tray (Linux)
Right/left-click the tray icon: **Show** toggles the window, **Quit** closes the app. On Linux a
menu must be attached for the icon to appear, so the tray menu is built in `lib.rs`.

## Notes for the deploy pipeline
- Build locally first and confirm it compiles/bundles green before pushing -- the pinned crate
  versions (tauri 2.12.5, tauri-build 2.7.4) must match the remote build to avoid shipping a
  binary that will not build there.
- The Webview loads the remote host per `tauri.conf.json`; if the backend is briefly down the
  client shows the offline page instead of crashing.
