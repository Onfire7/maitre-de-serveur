//! A Service's page in a web view of its own (decision 294). The page used
//! to be an iframe inside the Console's document, which made it a foreign
//! frame to the app (refused by X-Frame-Options and frame-ancestors,
//! cookies dropped as third-party) and a tenant of the Console's own web
//! process (a page that hung or crashed took the Console with it). Each
//! page is now a child web view of the main window: a top-level document to
//! the app and to the engine, in a web process of its own, drawn by the
//! engine above the Console's HTML.
//!
//! Being drawn above the Console is why nothing of the Console may float
//! over it (decision 293): the Console says where the view sits (the page
//! area, slid right by an open panel's width), hides it under a modal, and
//! asks for a few of its colours for the modal's stand-in. The engine's
//! snapshot is shrunk to that grid at once and never kept.
//!
//! What the view reports comes back as `service-view` events on the main
//! window: the engine's load started and finished, a crash of the page's
//! web process, and what the script the proxy adds to each document says
//! (load, push, replace, pop, unload) through the proxy's beacon.
//!
//! On Linux, Tauri packs a child view into the window's vertical box, where
//! it would sit under the Console's own view, so the main view is wrapped
//! in an overlay whose upper layer is a fixed-position container that the
//! child views are moved into (`placement`). The view may be wider than the
//! window: the part past the window's edge is clipped, which is how the
//! page slides aside without resizing.
//!
//! Dev tools, for driving the views from outside (a compositor's capture
//! does not show a view, and WebDriver reaches only the Console's own
//! document): `view_snapshot` writes what a view shows to a PNG,
//! `view_eval` runs a script in a view, `view_crash` ends a view's web
//! process. Opening and closing wait on the main thread, so their commands
//! are async: a sync command runs on the main thread and would wait for
//! itself.

use serde::{Deserialize, Serialize};
use std::collections::HashMap;
use std::sync::Mutex;
use tauri::webview::{NewWindowResponse, PageLoadEvent, WebviewBuilder};
use tauri::{AppHandle, Emitter, LogicalPosition, LogicalSize, Manager, Webview, WebviewUrl, Wry};

/// Where a view sits in the window, in logical pixels of the main view.
#[derive(Clone, Copy, Debug, Deserialize)]
pub struct Bounds {
    pub x: f64,
    pub y: f64,
    pub width: f64,
    pub height: f64,
}

/// What a view says to the Console (event `service-view`).
#[derive(Clone, Serialize)]
pub struct Report {
    pub service: String,
    /// `started`, `finished`, `crashed`; or from the page's script: `load`,
    /// `push`, `replace`, `pop`, `unload`.
    pub event: String,
    pub url: String,
}

pub const EVENT: &str = "service-view";

pub fn report(app: &AppHandle, service: &str, event: &str, url: &str) {
    let _ = app.emit_to("main", EVENT, Report { service: service.into(), event: event.into(), url: url.into() });
}

/// The open views, by Service name.
#[derive(Default)]
pub struct Views {
    open: Mutex<HashMap<String, Webview<Wry>>>,
}

fn label(service: &str) -> String {
    format!("svc-{service}")
}

impl Views {
    fn get(&self, service: &str) -> Result<Webview<Wry>, String> {
        self.open.lock().unwrap().get(service).cloned().ok_or_else(|| format!("{service} has no view open"))
    }

    /// Opens a view for `service` at `url`, placed at `bounds` and shown
    /// or not; a view already open is only placed.
    pub fn open(&self, app: &AppHandle, service: &str, url: &str, bounds: Bounds, shown: bool) -> Result<(), String> {
        if self.open.lock().unwrap().contains_key(service) {
            return self.place(service, bounds);
        }
        let url: tauri::Url = url.parse().map_err(|e| format!("{url}: {e}"))?;
        let window = app.get_window("main").ok_or("the main window is gone")?;
        let name = service.to_string();
        let app1 = app.clone();
        let name1 = name.clone();
        let builder = WebviewBuilder::new(label(service), WebviewUrl::External(url))
            .on_page_load(move |_, payload| {
                let event = match payload.event() {
                    PageLoadEvent::Started => "started",
                    PageLoadEvent::Finished => "finished",
                };
                report(&app1, &name1, event, payload.url().as_str());
            })
            // A link that asks for a new window (target=_blank) goes to
            // the system browser: the Console shows one page per Service.
            .on_new_window(|url, _| {
                let _ = tauri_plugin_opener::open_url(url.as_str(), None::<&str>);
                NewWindowResponse::Deny
            });
        let view = window.add_child(builder, LogicalPosition::new(bounds.x, bounds.y), LogicalSize::new(bounds.width, bounds.height)).map_err(|e| e.to_string())?;
        #[cfg(target_os = "linux")]
        {
            let app2 = app.clone();
            let name2 = name.clone();
            view.with_webview(move |pw| {
                let inner = pw.inner();
                if let Err(why) = placement::adopt(&inner, bounds) {
                    eprintln!("views: {name2}'s view could not be placed: {why}");
                }
                let _ = crate::webkit::disable_view_transitions(&inner);
                use webkit2gtk::WebViewExt;
                inner.connect_web_process_terminated(move |view, reason| {
                    report(&app2, &name2, "crashed", &format!("{reason:?}"));
                    let _ = view;
                });
            })
            .map_err(|e| e.to_string())?;
        }
        if !shown {
            view.hide().map_err(|e| e.to_string())?;
        }
        self.open.lock().unwrap().insert(name, view);
        Ok(())
    }

    pub fn place(&self, service: &str, bounds: Bounds) -> Result<(), String> {
        let view = self.get(service)?;
        #[cfg(target_os = "linux")]
        {
            view.with_webview(move |pw| placement::place(&pw.inner(), bounds)).map_err(|e| e.to_string())
        }
        #[cfg(not(target_os = "linux"))]
        {
            view.set_bounds(tauri::Rect { position: LogicalPosition::new(bounds.x, bounds.y).into(), size: LogicalSize::new(bounds.width, bounds.height).into() }).map_err(|e| e.to_string())
        }
    }

    pub fn show(&self, service: &str, shown: bool) -> Result<(), String> {
        let view = self.get(service)?;
        if shown { view.show() } else { view.hide() }.map_err(|e| e.to_string())
    }

    pub fn close(&self, service: &str) -> Result<(), String> {
        let Some(view) = self.open.lock().unwrap().remove(service) else { return Ok(()) };
        view.close().map_err(|e| e.to_string())
    }

    pub fn navigate(&self, service: &str, url: &str) -> Result<(), String> {
        let url: tauri::Url = url.parse().map_err(|e| format!("{url}: {e}"))?;
        self.get(service)?.navigate(url).map_err(|e| e.to_string())
    }

    pub fn reload(&self, service: &str) -> Result<(), String> {
        self.get(service)?.reload().map_err(|e| e.to_string())
    }

    pub fn stop(&self, service: &str) -> Result<(), String> {
        self.get(service)?.eval("window.stop()").map_err(|e| e.to_string())
    }

    /// A few of the page's colours, `cols` by `rows`, from the engine's
    /// snapshot of what the view shows; empty where the engine cannot say.
    pub async fn sample(&self, service: &str, cols: i32, rows: i32) -> Result<Vec<Vec<String>>, String> {
        let view = self.get(service)?;
        #[cfg(target_os = "linux")]
        {
            let (tx, rx) = tokio::sync::oneshot::channel();
            view.with_webview(move |pw| {
                use webkit2gtk::{SnapshotOptions, SnapshotRegion, WebViewExt};
                pw.inner().snapshot(SnapshotRegion::Visible, SnapshotOptions::NONE, None::<&gtk::gio::Cancellable>, move |res| {
                    let grid = res.ok().and_then(|s| shrink(&s, cols, rows)).unwrap_or_default();
                    let _ = tx.send(grid);
                });
            })
            .map_err(|e| e.to_string())?;
            rx.await.map_err(|_| "the snapshot never came".to_string())
        }
        #[cfg(not(target_os = "linux"))]
        {
            let _ = (view, cols, rows);
            Ok(Vec::new())
        }
    }
}

/// Dev tool: evaluates `js` in a view and returns the result as JSON.
pub async fn eval_in(views: &Views, service: &str, js: String) -> Result<String, String> {
    let view = views.get(service)?;
    let (tx, rx) = tokio::sync::oneshot::channel::<String>();
    let tx = std::sync::Mutex::new(Some(tx));
    view.eval_with_callback(js, move |out| {
        if let Some(tx) = tx.lock().unwrap().take() {
            let _ = tx.send(out);
        }
    })
    .map_err(|e| e.to_string())?;
    rx.await.map_err(|_| "no result".to_string())
}

/// Dev tool: ends a view's web process, as a crash would.
pub fn crash(views: &Views, service: &str) -> Result<(), String> {
    let view = views.get(service)?;
    #[cfg(target_os = "linux")]
    {
        view.with_webview(|pw| {
            use webkit2gtk::WebViewExt;
            pw.inner().terminate_web_process();
        })
        .map_err(|e| e.to_string())
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = view;
        Err("Linux only".into())
    }
}

/// Writes what a view shows to a PNG at `path`, for checking the views
/// from outside (a compositor's capture does not show them): the main
/// view for an empty `service`. Linux only; elsewhere an error.
pub async fn snapshot_to(app: &AppHandle, views: &Views, service: &str, path: String) -> Result<(), String> {
    #[cfg(target_os = "linux")]
    {
        let (tx, rx) = tokio::sync::oneshot::channel::<Result<(), String>>();
        let take = move |pw: tauri::webview::PlatformWebview| {
            use webkit2gtk::{SnapshotOptions, SnapshotRegion, WebViewExt};
            pw.inner().snapshot(SnapshotRegion::Visible, SnapshotOptions::NONE, None::<&gtk::gio::Cancellable>, move |res| {
                let out = res.map_err(|e| e.to_string()).and_then(|s| {
                    let img = gtk::cairo::ImageSurface::try_from(s).map_err(|_| "not an image".to_string())?;
                    let mut f = std::fs::File::create(&path).map_err(|e| e.to_string())?;
                    img.write_to_png(&mut f).map_err(|e| e.to_string())
                });
                let _ = tx.send(out);
            });
        };
        if service.is_empty() {
            app.get_webview("main").ok_or("no main view")?.with_webview(take).map_err(|e| e.to_string())?;
        } else {
            views.get(service)?.with_webview(take).map_err(|e| e.to_string())?;
        }
        rx.await.map_err(|_| "the snapshot never came".to_string())?
    }
    #[cfg(not(target_os = "linux"))]
    {
        let _ = (app, views, service, path);
        Err("snapshots are Linux only".into())
    }
}

/// Paints the snapshot onto a `cols` by `rows` image with a filter that
/// averages the pixels it shrinks, and reads the colours off it.
#[cfg(target_os = "linux")]
fn shrink(src: &gtk::cairo::Surface, cols: i32, rows: i32) -> Option<Vec<Vec<String>>> {
    use gtk::cairo;
    let img = cairo::ImageSurface::try_from(src.clone()).ok()?;
    let (w, h) = (img.width(), img.height());
    if w <= 0 || h <= 0 || cols <= 0 || rows <= 0 {
        return None;
    }
    let mut small = cairo::ImageSurface::create(cairo::Format::Rgb24, cols, rows).ok()?;
    {
        let cr = cairo::Context::new(&small).ok()?;
        cr.scale(cols as f64 / w as f64, rows as f64 / h as f64);
        cr.set_source_surface(&img, 0.0, 0.0).ok()?;
        cr.source().set_filter(cairo::Filter::Good);
        cr.paint().ok()?;
    }
    small.flush();
    let stride = small.stride() as usize;
    let data = small.data().ok()?;
    Some(grid_of_rgb24(&data, stride, cols as usize, rows as usize))
}

/// Cairo's RGB24 is one native-endian 32-bit word per pixel, xRGB: on a
/// little-endian machine the bytes run B, G, R, unused.
#[cfg(target_os = "linux")]
fn grid_of_rgb24(data: &[u8], stride: usize, cols: usize, rows: usize) -> Vec<Vec<String>> {
    (0..rows)
        .map(|r| {
            (0..cols)
                .map(|c| {
                    let px = u32::from_ne_bytes(data[r * stride + c * 4..r * stride + c * 4 + 4].try_into().unwrap());
                    format!("#{:02x}{:02x}{:02x}", (px >> 16) & 0xff, (px >> 8) & 0xff, px & 0xff)
                })
                .collect()
        })
        .collect()
}

/// Where the child views go on Linux: a fixed-position layer over the
/// main view, so a view sits where the Console says and may hang past the
/// window's edge.
#[cfg(target_os = "linux")]
pub mod placement {
    use super::Bounds;
    use gtk::prelude::*;
    use std::cell::RefCell;

    thread_local! {
        static LAYER: RefCell<Option<gtk::Fixed>> = const { RefCell::new(None) };
    }

    /// Wraps the main view, in place in its box, in an overlay with the
    /// layer above it. Pointer events over the layer's empty parts reach
    /// the main view.
    pub fn prepare(main: &webkit2gtk::WebView) -> Result<(), &'static str> {
        let Some(parent) = main.parent() else { return Err("the main view has no parent") };
        let Some(vbox) = parent.downcast_ref::<gtk::Box>() else { return Err("the main view is not in a box") };
        let overlay = gtk::Overlay::new();
        let layer = gtk::Fixed::new();
        vbox.remove(main);
        overlay.add(main);
        overlay.add_overlay(&layer);
        overlay.set_overlay_pass_through(&layer, true);
        vbox.pack_start(&overlay, true, true, 0);
        overlay.show_all();
        LAYER.with(|l| *l.borrow_mut() = Some(layer));
        Ok(())
    }

    /// Moves a child view Tauri packed into the box onto the layer.
    pub fn adopt(view: &webkit2gtk::WebView, b: Bounds) -> Result<(), &'static str> {
        LAYER.with(|l| {
            let l = l.borrow();
            let Some(layer) = l.as_ref() else { return Err("the layer was not prepared") };
            if let Some(parent) = view.parent() {
                if let Some(c) = parent.downcast_ref::<gtk::Container>() {
                    c.remove(view);
                }
            }
            let (x, y, w, h) = (b.x.round() as i32, b.y.round() as i32, b.width.round() as i32, b.height.round() as i32);
            view.set_size_request(w, h);
            layer.put(view, x, y);
            view.show();
            // The layer does not allocate a child put into it after its own
            // allocation, so the child is allocated here, as wry does.
            view.size_allocate(&gtk::Allocation::new(x, y, w, h));
            Ok(())
        })
    }

    pub fn place(view: &webkit2gtk::WebView, b: Bounds) {
        LAYER.with(|l| {
            if let Some(layer) = l.borrow().as_ref() {
                let (x, y, w, h) = (b.x.round() as i32, b.y.round() as i32, b.width.round() as i32, b.height.round() as i32);
                view.set_size_request(w, h);
                layer.move_(view, x, y);
                view.size_allocate(&gtk::Allocation::new(x, y, w, h));
            }
        });
    }
}

#[cfg(all(test, target_os = "linux"))]
mod tests {
    use super::*;

    #[test]
    fn colours_read_off_rgb24_rows_by_stride() {
        // Two pixels wide, one row, a stride with padding; little-endian xRGB.
        let mut data = vec![0u8; 16];
        data[..4].copy_from_slice(&0x00ff8000u32.to_ne_bytes());
        data[4..8].copy_from_slice(&0x00102030u32.to_ne_bytes());
        assert_eq!(grid_of_rgb24(&data, 16, 2, 1), vec![vec!["#ff8000", "#102030"]]);
    }
}
