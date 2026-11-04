//! Loopback proxy for the built-in browser (decision 71). Each opened
//! Service gets a port on 127.0.0.1; the webview talks plain HTTP to it and
//! every request is relayed over mutual TLS to the Node's agent with the
//! X-Maitre-Service header, which the agent routes to the Service. HTTP/1.1
//! end to end so WebSocket upgrades (chat-style apps, dashboards) work.
//!
//! A Service's page is a document of its own, which the Console cannot
//! read where it has gone, and so cannot move back and forward (decision
//! 286). So a page proxy serves a small script at /__maitre/frame.js and
//! adds a tag for it to every HTML document it relays (asked for without
//! compression, so the document can be edited). The script sends the
//! page's address to /__maitre/nav on each load and on pushState,
//! replaceState and popstate, which the proxy hands to the Console
//! (decision 294: the page is a native view, so the beacon is the way
//! back; going, reloading and stopping are the view's own). A page whose
//! Content-Security-Policy refuses the script works as before, without
//! the history controls.
//!
//! A Service's port is the same from one launch to the next and whichever
//! way its Node is reached (decision 288): the webview's HTTP cache, cookies
//! and local storage are per origin, and a port chosen afresh each launch
//! left every image to be fetched again and every login to be done again.
//! The port is picked from the Service's name the first time and kept in
//! the Console's saved state; a running proxy follows a change of Node
//! address rather than opening a new origin.

use crate::identity::Identity;
use crate::rpc::pinned_tls_config;
use bytes::Bytes;
use http_body_util::{combinators::BoxBody, BodyExt};
use hyper::body::Incoming;
use hyper::header::{HeaderName, HeaderValue};
use hyper::{Request, Response};
use hyper_util::rt::TokioIo;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex, RwLock};
use tokio::net::{TcpListener, TcpStream};
use tokio_rustls::TlsConnector;

#[derive(Clone)]
struct Target {
    addr: String,
    fingerprint: String,
    /// The Service a page proxy reaches; empty for a Node's file proxy.
    service: String,
    /// A file proxy's secret first path segment (decision 183): without it,
    /// or for anything but GET/HEAD of file contents, the proxy refuses, so
    /// other programs on this computer cannot use the device's identity.
    token: String,
}

/// Where a running proxy relays to; swapped when the Node is reached another way.
type Upstream = Arc<RwLock<(Target, Arc<rustls::ClientConfig>)>>;

struct Live {
    port: u16,
    upstream: Upstream,
}

/// Hears what a page's script reports: the Service, the event and the URL.
pub type Reporter = Arc<dyn Fn(&str, &str, &str) + Send + Sync>;

/// One proxy per Service, on a port kept across launches, and one file
/// proxy per Node.
#[derive(Default)]
pub struct Proxies {
    ports: Mutex<HashMap<String, Live>>,
    files: Mutex<HashMap<String, String>>,
    reporter: Mutex<Option<Reporter>>,
}

/// The lowest and the number of ports a Service's port is picked from.
const PORT_BASE: u16 = 41000;
const PORT_SPAN: u16 = 9000;

/// The ports to try for a Service, in order: the one it had, then ones
/// from its name (FNV-1a, so the same on every launch and machine), so a
/// Service lands on the same port even before the Console remembers it.
pub fn port_candidates(service: &str, remembered: Option<u16>) -> Vec<u16> {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in service.bytes() {
        h ^= b as u64;
        h = h.wrapping_mul(0x100000001b3);
    }
    let first = PORT_BASE + (h % PORT_SPAN as u64) as u16;
    let mut out: Vec<u16> = remembered.into_iter().collect();
    for i in 0..16u16 {
        let p = PORT_BASE + (first - PORT_BASE + i * 37) % PORT_SPAN;
        if !out.contains(&p) {
            out.push(p);
        }
    }
    out
}

impl Proxies {
    pub fn set_reporter(&self, f: impl Fn(&str, &str, &str) + Send + Sync + 'static) {
        *self.reporter.lock().unwrap() = Some(Arc::new(f));
    }

    /// Returns the local URL for a Service and the port it is on, starting a
    /// proxy if needed: on `remembered` when it is free, else on a port from
    /// the Service's name. A proxy already running for the Service is
    /// pointed at `addr` if it was reaching the Node another way.
    pub async fn open(&self, id: Arc<Identity>, addr: String, fingerprint: String, service: String, remembered: Option<u16>) -> Result<(String, u16), String> {
        let target = Target { addr, fingerprint, service: service.clone(), token: String::new() };
        let live = self.ports.lock().unwrap().get(&service).map(|l| (l.port, l.upstream.clone()));
        if let Some((port, upstream)) = live {
            let same = {
                let cur = upstream.read().unwrap();
                cur.0.addr == target.addr && cur.0.fingerprint == target.fingerprint
            };
            if !same {
                let tls = Arc::new(pinned_tls_config(&id, &target.fingerprint, &[b"http/1.1".to_vec()])?);
                *upstream.write().unwrap() = (target, tls);
            }
            return Ok((format!("http://127.0.0.1:{port}/"), port));
        }
        let candidates = port_candidates(&service, remembered);
        self.serve(id, service, target, &candidates).await
    }

    /// Returns the local base URL (ending in the token, no slash) for a
    /// Node's file contents, starting a proxy if needed.
    pub async fn open_files(&self, id: Arc<Identity>, addr: String, fingerprint: String) -> Result<String, String> {
        let key = format!("{fingerprint}@{addr}#files");
        if let Some(base) = self.files.lock().unwrap().get(&key) {
            return Ok(base.clone());
        }
        let token = format!("{:032x}", rand::random::<u128>());
        let target = Target { addr, fingerprint, service: String::new(), token: token.clone() };
        let (root, _) = self.serve(id, key.clone(), target, &[]).await?;
        let base = format!("{root}{token}");
        self.files.lock().unwrap().insert(key, base.clone());
        Ok(base)
    }

    /// Listens on the first free port of `prefer`, or on any free port when
    /// none of them is (another program has them), and relays to `target`.
    async fn serve(&self, id: Arc<Identity>, key: String, target: Target, prefer: &[u16]) -> Result<(String, u16), String> {
        let mut listener = None;
        for &p in prefer {
            if let Ok(l) = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], p))).await {
                listener = Some(l);
                break;
            }
        }
        let listener = match listener {
            Some(l) => l,
            None => TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await.map_err(|e| e.to_string())?,
        };
        let port = listener.local_addr().map_err(|e| e.to_string())?.port();
        let tls = Arc::new(pinned_tls_config(&id, &target.fingerprint, &[b"http/1.1".to_vec()])?);
        let upstream: Upstream = Arc::new(RwLock::new((target, tls)));
        let up = upstream.clone();
        let reporter = self.reporter.lock().unwrap().clone();
        tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else { break };
                let up = up.clone();
                let reporter = reporter.clone();
                tokio::spawn(async move {
                    let svc = hyper::service::service_fn(move |req| {
                        let (target, tls) = up.read().unwrap().clone();
                        relay(req, target, tls, reporter.clone())
                    });
                    let _ = hyper::server::conn::http1::Builder::new()
                        .preserve_header_case(true)
                        .serve_connection(TokioIo::new(stream), svc)
                        .with_upgrades()
                        .await;
                });
            }
        });
        self.ports.lock().unwrap().insert(key, Live { port, upstream });
        Ok((format!("http://127.0.0.1:{port}/"), port))
    }
}

/// Relay one request (and, for upgrades, the resulting byte stream).
async fn relay(
    mut req: Request<Incoming>,
    target: Target,
    tls: Arc<rustls::ClientConfig>,
    reporter: Option<Reporter>,
) -> Result<Response<BoxBody<Bytes, hyper::Error>>, hyper::Error> {
    if !target.token.is_empty() {
        let prefix = format!("/{}/maitre/files/content", target.token);
        let read = req.method() == hyper::Method::GET || req.method() == hyper::Method::HEAD;
        if !read || !req.uri().path().starts_with(&prefix) {
            return Ok(text(403, "not allowed".into()));
        }
        let pq = req.uri().path_and_query().map(|p| p.as_str().to_string()).unwrap_or_default();
        *req.uri_mut() = pq[target.token.len() + 1..].parse().unwrap();
    }
    let page = !target.service.is_empty();
    if page && req.uri().path() == FRAME_SCRIPT_PATH {
        return Ok(Response::builder()
            .status(200)
            .header("content-type", "application/javascript; charset=utf-8")
            .header("cache-control", "no-store")
            .body(http_body_util::Full::new(Bytes::from_static(FRAME_SCRIPT.as_bytes())).map_err(|never| match never {}).boxed())
            .unwrap());
    }
    if page && req.uri().path() == NAV_PATH {
        if req.method() == hyper::Method::POST {
            let body = req.into_body().collect().await.map(|b| b.to_bytes()).unwrap_or_default();
            if let (Some(report), Some((event, url))) = (reporter, parse_nav(&body)) {
                report(&target.service, &event, &url);
            }
        }
        return Ok(Response::builder()
            .status(204)
            .header("cache-control", "no-store")
            .body(http_body_util::Empty::new().map_err(|never| match never {}).boxed())
            .unwrap());
    }
    // A document is asked for uncompressed so the frame script can be added to it.
    let document = page && wants_document(&req);
    if document {
        req.headers_mut().remove(hyper::header::ACCEPT_ENCODING);
    }
    match connect(&target, tls).await {
        Ok(mut sender) => {
            let client_upgrade = hyper::upgrade::on(&mut req);
            let wants_upgrade = req.headers().contains_key(hyper::header::UPGRADE);
            if page {
                req.headers_mut().insert(
                    HeaderName::from_static("x-maitre-service"),
                    HeaderValue::from_str(&target.service).unwrap(),
                );
            }
            // Origin-form URI; the agent routes by header, not host.
            let pq = req.uri().path_and_query().map(|p| p.as_str().to_string()).unwrap_or_else(|| "/".into());
            *req.uri_mut() = pq.parse().unwrap();
            match sender.send_request(req).await {
                Ok(mut res) => {
                    if wants_upgrade && res.status() == hyper::StatusCode::SWITCHING_PROTOCOLS {
                        let server_upgrade = hyper::upgrade::on(&mut res);
                        tokio::spawn(async move {
                            if let (Ok(c), Ok(s)) = (client_upgrade.await, server_upgrade.await) {
                                let mut c = TokioIo::new(c);
                                let mut s = TokioIo::new(s);
                                let _ = tokio::io::copy_bidirectional(&mut c, &mut s).await;
                            }
                        });
                    }
                    if document && is_plain_html(&res) {
                        let (mut parts, body) = res.into_parts();
                        return match body.collect().await {
                            Ok(b) => {
                                let html = inject_frame_script(&b.to_bytes());
                                parts.headers.remove(hyper::header::CONTENT_LENGTH);
                                parts.headers.remove(hyper::header::TRANSFER_ENCODING);
                                parts.headers.insert(hyper::header::CONTENT_LENGTH, HeaderValue::from(html.len()));
                                Ok(Response::from_parts(parts, http_body_util::Full::new(html).map_err(|never| match never {}).boxed()))
                            }
                            Err(e) => Ok(text(502, format!("Maître Desktop could not read the Service's page: {e}"))),
                        };
                    }
                    Ok(res.map(|b| b.boxed()))
                }
                Err(e) => Ok(text(502, format!("Maître Desktop could not reach the Service: {e}"))),
            }
        }
        Err(e) => Ok(text(502, format!("Maître Desktop could not reach the Node: {e}"))),
    }
}

async fn connect(
    target: &Target,
    tls: Arc<rustls::ClientConfig>,
) -> Result<hyper::client::conn::http1::SendRequest<Incoming>, String> {
    let tcp = TcpStream::connect(&target.addr).await.map_err(|e| e.to_string())?;
    let name = rustls::pki_types::ServerName::try_from("maitre").map_err(|e| e.to_string())?;
    let stream = TlsConnector::from(tls).connect(name, tcp).await.map_err(|e| e.to_string())?;
    let (sender, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream)).await.map_err(|e| e.to_string())?;
    tokio::spawn(async move {
        let _ = conn.with_upgrades().await;
    });
    Ok(sender)
}

fn text(status: u16, body: String) -> Response<BoxBody<Bytes, hyper::Error>> {
    Response::builder()
        .status(status)
        .header("content-type", "text/plain; charset=utf-8")
        .body(http_body_util::Full::new(Bytes::from(body)).map_err(|never| match never {}).boxed())
        .unwrap()
}

const FRAME_SCRIPT_PATH: &str = "/__maitre/frame.js";
const FRAME_SCRIPT_TAG: &[u8] = b"<script src=\"/__maitre/frame.js\"></script>";
/// Where the script reports to: a POST whose body is `{"event","url"}`.
const NAV_PATH: &str = "/__maitre/nav";

/// Runs inside a Service's page (decision 286). It reports the page's
/// address to the Console on load and on every History change, through a
/// beacon to the proxy: the page is a view of its own (decision 294), with
/// no parent window to post to. A frame inside the page says nothing.
const FRAME_SCRIPT: &str = r#"(function () {
  if (window.parent !== window) return;
  var send = function (event) {
    try { navigator.sendBeacon("/__maitre/nav", JSON.stringify({ event: event, url: location.href })); } catch (e) {}
  };
  var wrap = function (name, event) {
    var orig = history[name];
    if (typeof orig !== "function") return;
    history[name] = function () { var r = orig.apply(this, arguments); send(event); return r; };
  };
  wrap("pushState", "push");
  wrap("replaceState", "replace");
  window.addEventListener("popstate", function () { send("pop"); });
  window.addEventListener("pagehide", function () { send("unload"); });
  send("load");
})();
"#;

/// The event and URL of a beacon's body; None for anything else.
fn parse_nav(body: &[u8]) -> Option<(String, String)> {
    let v: serde_json::Value = serde_json::from_slice(body).ok()?;
    let event = v.get("event")?.as_str()?;
    if !["load", "push", "replace", "pop", "unload"].contains(&event) {
        return None;
    }
    let url = v.get("url")?.as_str()?;
    Some((event.to_string(), url.to_string()))
}

/// A request for a document, by its Accept header: a page, not a script,
/// a style, an image or an API call.
fn wants_document<B>(req: &Request<B>) -> bool {
    req.method() == hyper::Method::GET
        && req
            .headers()
            .get(hyper::header::ACCEPT)
            .and_then(|v| v.to_str().ok())
            .map(|a| a.trim_start().to_ascii_lowercase().starts_with("text/html"))
            .unwrap_or(false)
}

/// An HTML document that came uncompressed, so it can be edited.
fn is_plain_html<B>(res: &Response<B>) -> bool {
    let header = |h: hyper::header::HeaderName| res.headers().get(h).and_then(|v| v.to_str().ok()).map(|s| s.to_ascii_lowercase());
    let html = header(hyper::header::CONTENT_TYPE).map(|t| t.trim_start().starts_with("text/html")).unwrap_or(false);
    let encoded = header(hyper::header::CONTENT_ENCODING).map(|e| !e.is_empty() && e != "identity").unwrap_or(false);
    html && !encoded && res.status().is_success()
}

/// The script tag goes right after `<head>` when there is one (before the
/// page's own scripts, so a History change made at start is reported), else
/// at the front.
fn inject_frame_script(html: &[u8]) -> Bytes {
    let lower: Vec<u8> = html.iter().map(|b| b.to_ascii_lowercase()).collect();
    let at = find(&lower, b"<head").and_then(|i| lower[i..].iter().position(|&b| b == b'>').map(|j| i + j + 1)).unwrap_or(0);
    let mut out = Vec::with_capacity(html.len() + FRAME_SCRIPT_TAG.len());
    out.extend_from_slice(&html[..at]);
    out.extend_from_slice(FRAME_SCRIPT_TAG);
    out.extend_from_slice(&html[at..]);
    Bytes::from(out)
}

fn find(hay: &[u8], needle: &[u8]) -> Option<usize> {
    hay.windows(needle.len()).position(|w| w == needle)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn script_goes_after_head_whatever_its_case() {
        let out = inject_frame_script(b"<!doctype html><HTML><Head lang=\"en\"><title>x</title></Head></HTML>");
        assert_eq!(&out[..], &b"<!doctype html><HTML><Head lang=\"en\"><script src=\"/__maitre/frame.js\"></script><title>x</title></Head></HTML>"[..]);
    }

    #[test]
    fn script_leads_a_document_without_head() {
        let out = inject_frame_script(b"<p>hi</p>");
        assert_eq!(&out[..], &b"<script src=\"/__maitre/frame.js\"></script><p>hi</p>"[..]);
    }

    #[test]
    fn a_beacon_names_one_of_the_page_events() {
        assert_eq!(parse_nav(br#"{"event":"push","url":"http://127.0.0.1:4100/web/movies"}"#), Some(("push".into(), "http://127.0.0.1:4100/web/movies".into())));
        assert_eq!(parse_nav(br#"{"event":"eval","url":"x"}"#), None);
        assert_eq!(parse_nav(b"not json"), None);
    }

    #[test]
    fn a_service_port_comes_from_its_name_and_the_remembered_one_leads() {
        let a = port_candidates("jellyfin", None);
        assert_eq!(a, port_candidates("jellyfin", None));
        assert!(a.iter().all(|&p| (PORT_BASE..PORT_BASE + PORT_SPAN).contains(&p)));
        assert_ne!(a[0], port_candidates("memos", None)[0]);
        let b = port_candidates("jellyfin", Some(43210));
        assert_eq!(b[0], 43210);
        assert_eq!(&b[1..], &a[..]);
    }

    #[tokio::test]
    async fn a_service_keeps_its_port_and_origin_when_its_node_is_reached_another_way() {
        let dir = std::env::temp_dir().join(format!("maitre-proxy-test-{:x}", rand::random::<u64>()));
        let id = Arc::new(Identity::load_or_generate(&dir.join("device")).unwrap());
        let fp = "ab".repeat(32);
        let proxies = Proxies::default();
        let (url, port) = proxies.open(id.clone(), "192.168.1.5:7443".into(), fp.clone(), "jellyfin".into(), None).await.unwrap();
        assert_eq!(url, format!("http://127.0.0.1:{port}/"));
        let (again, p2) = proxies.open(id.clone(), "100.64.0.9:7443".into(), fp.clone(), "jellyfin".into(), None).await.unwrap();
        assert_eq!((again, p2), (url, port));
        let up = proxies.ports.lock().unwrap().get("jellyfin").unwrap().upstream.clone();
        assert_eq!(up.read().unwrap().0.addr, "100.64.0.9:7443");
        // A remembered port that is free is taken; one that is busy is not.
        let busy = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await.unwrap();
        let taken = busy.local_addr().unwrap().port();
        let (_, p3) = proxies.open(id.clone(), "192.168.1.5:7443".into(), fp.clone(), "memos".into(), Some(taken)).await.unwrap();
        assert_ne!(p3, taken);
        assert_eq!(p3, port_candidates("memos", None)[0]);
        let _ = std::fs::remove_dir_all(dir);
    }

    #[test]
    fn only_documents_asked_for_as_html_are_edited() {
        let doc = Request::builder().header("accept", "text/html,application/xhtml+xml").body(()).unwrap();
        assert!(wants_document(&doc));
        let api = Request::builder().header("accept", "application/json").body(()).unwrap();
        assert!(!wants_document(&api));
        let post = Request::builder().method("POST").header("accept", "text/html").body(()).unwrap();
        assert!(!wants_document(&post));
        let plain = Response::builder().header("content-type", "text/html; charset=utf-8").body(()).unwrap();
        assert!(is_plain_html(&plain));
        let gz = Response::builder().header("content-type", "text/html").header("content-encoding", "gzip").body(()).unwrap();
        assert!(!is_plain_html(&gz));
        let json = Response::builder().header("content-type", "application/json").body(()).unwrap();
        assert!(!is_plain_html(&json));
    }
}
