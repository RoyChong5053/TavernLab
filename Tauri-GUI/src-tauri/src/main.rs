// Prevents additional console window on Windows in release, DO NOT REMOVE!!
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

use std::env;

/// Resolve which remote web UI to embed. Resolution order (last wins):
///
/// 1. `--base <http(s)://host[:port]/` CLI flag  -> per-invocation override, handy for pointing the
///    same binary at different deployed sites without rebuilding.
/// 2. `tavernlab_server_url` env var             -> good for launchers / shortcuts that pin a fixed
///    target without editing code.
/// 3. built-in default                           -> the LAN endpoint that runs 24/7 on this box.
///
/// The resolved URL is passed to `app_lib::run` so there is a single source of truth here.
fn resolve_base() -> String {
    if let Some(i) = env::args().position(|a| a == "--base") {
        if let Some(url) = env::args().nth(i + 1) {
            if !url.trim().is_empty() {
                return url;
            }
        }
    }

    if let Ok(url) = env::var("tavernlab_server_url") {
        if !url.trim().is_empty() {
            return url;
        }
    }

    // Default: reachable LAN endpoint on this box. Override via --base or the env var above.
    "http://192.168.10.2:8888/".to_string()
}

fn main() {
    let base = resolve_base();
    app_lib::run(base);
}
