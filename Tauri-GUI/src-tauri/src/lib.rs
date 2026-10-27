//! Tauri (v2) client that embeds the remote web UI in one window with a one-click tray.
//!
//! There is no local runtime here: the Go server already runs 24/7, so this app only has to open a
//! Webview to the resolved target (see `main.rs`: `--base` flag > `tavernlab_server_url` env >
//! built-in LAN default). If the URL is missing, non-http, or the host is not reachable we keep
//! the offline fallback page instead of crashing -- so the client stays usable while the backend
//! or network is briefly down.

use tauri::menu::MenuItem;
use tauri::Manager;
use tauri::Url;

/// Build and run the single-window client that loads `base_url` at launch.
pub fn run(base_url: String) {
    tauri::Builder::default()
        .setup(move |app| {
            // 1) Point the main window at the resolved remote target.
            if let Some(win) = app.get_webview_window("main") {
                match Url::parse(&base_url) {
                    Ok(url) if url.scheme().starts_with("http") => {
                        let _ = win.navigate(url);
                    }
                    _ => eprintln!(
                        "tavernlab-desktop: invalid or non-http target {:?}; keeping offline page",
                        base_url
                    ),
                }
            }

            // 2) Attach a one-click tray menu (Show / Quit) to the tray declared in
            //    tauri.conf.json. On Linux a menu must be present for the icon to show.
            if let Some(tray) = app.tray_by_id("main") {
                let show = MenuItem::with_id(app, "show", "Show", true, None::<&str>)
                    .expect("failed to build Show tray item");
                let quit = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)
                    .expect("failed to build Quit tray item");

                let menu = tauri::menu::Menu::new(app).expect("failed to build tray menu");
                menu.append(&show).expect("failed to append Show item");
                menu.append(&quit).expect("failed to append Quit item");

                if let Err(e) = tray.set_menu(Some(menu)) {
                    eprintln!("tavernlab-desktop: failed to set tray menu: {}", e);
                }
            }

            // 3) Tray menu clicks: toggle the window, or quit the app.
            app.on_menu_event(|app_handle, event| {
                if event.id() == "show" {
                    if let Some(win) = app_handle.get_webview_window("main") {
                        match win.is_visible() {
                            Ok(true) => {
                                let _ = win.hide();
                            }
                            _ => {
                                let _ = win.show();
                                let _ = win.set_focus();
                            }
                        }
                    }
                } else if event.id() == "quit" {
                    app_handle.exit(0);
                }
            });

            Ok(())
        })
        .run(tauri::generate_context!())
        .expect("error while building tauri application");
}
