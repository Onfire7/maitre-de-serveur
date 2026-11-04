// Prevents additional console window on Windows in release, DO NOT REMOVE!!
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

fn main() {
    // WebKitGTK's DMA-BUF renderer dies with "Error 71 (Protocol error)
    // dispatching to Wayland display" on NVIDIA under Wayland compositors.
    // Opt out unless the user has chosen a value themselves.
    #[cfg(target_os = "linux")]
    if std::env::var_os("WEBKIT_DISABLE_DMABUF_RENDERER").is_none() {
        // SAFETY: single-threaded here; nothing else has started yet.
        unsafe { std::env::set_var("WEBKIT_DISABLE_DMABUF_RENDERER", "1") };
    }
    // A Service's page that keeps its data in a worker (Actual Budget)
    // needs SharedArrayBuffer, which WebKitGTK leaves out even in a view
    // that is cross-origin isolated (decision 294: Actual's view reported
    // crossOriginIsolated true and no SharedArrayBuffer). JavaScriptCore
    // exposes it on this option, read by every web process the Desktop
    // starts. The Desktop shows only the pages of the user's own Fleet.
    #[cfg(target_os = "linux")]
    if std::env::var_os("JSC_useSharedArrayBuffer").is_none() {
        // SAFETY: as above.
        unsafe { std::env::set_var("JSC_useSharedArrayBuffer", "1") };
    }
    maitre_desktop_lib::run()
}
