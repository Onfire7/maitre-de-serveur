//! Loopback proxy for the built-in browser (decision 71). Each opened
//! Page of a Service gets a port on 127.0.0.1; the webview talks plain
//! HTTP to it and every request is relayed over mutual TLS to the Node's
//! agent with the X-Maitre-Service and X-Maitre-Page headers, which the
//! agent routes to that Page of that Service (ADR 0010). HTTP/1.1 end to
//! end so WebSocket upgrades (chat-style apps, dashboards) work.
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
//! A Terminal (ADR 0011) is a WebSocket to the Node's Terminal endpoint on
//! the Service tunnel, through a proxy of its own per Node. Any program on
//! this computer can reach 127.0.0.1, so that proxy relays nothing without
//! a one-time token in the URL, which the Desktop hands only to the
//! Console: `ws://127.0.0.1:<port>/<token>`, good for one upgrade or 30 s.
//! A page proxy never relays a `/__maitre/` path, so a Service's own page
//! cannot open one either.
//!
//! A Page's port is the same from one launch to the next and whichever
//! way its Node is reached (decision 288): the webview's HTTP cache, cookies
//! and local storage are per origin, and a port chosen afresh each launch
//! left every image to be fetched again and every login to be done again.
//! The port is picked from the Page's host name the first time and kept in
//! the Console's saved state; a running proxy follows a change of Node
//! address rather than opening a new origin.

use crate::dial::{Dialer, Proto};
use bytes::Bytes;
use http_body_util::{combinators::BoxBody, BodyExt};
use hyper::body::Incoming;
use hyper::header::{HeaderName, HeaderValue};
use hyper::{Request, Response};
use hyper_util::rt::TokioIo;
use std::collections::HashMap;
use std::net::SocketAddr;
use std::sync::{Arc, Mutex, RwLock};
use std::time::{Duration, Instant};
use tokio::net::TcpListener;

#[derive(Clone)]
struct Target {
    addr: String,
    fingerprint: String,
    /// The Service a page proxy reaches; empty for a Node's file proxy.
    service: String,
    /// Which Page of it (ADR 0010); empty is the Service's first.
    page: String,
    /// A file proxy's secret first path segment (decision 183): without it,
    /// or for anything but GET/HEAD of file contents, the proxy refuses, so
    /// other programs on this computer cannot use the device's identity.
    token: String,
    /// A Terminal proxy's one-time tokens (ADR 0011), each naming the
    /// Service it opens a Terminal into; None for any other proxy.
    terminals: Option<Tokens>,
}

/// How long a Terminal's token is good for, if it is not used first.
pub const TERMINAL_TOKEN_TTL: Duration = Duration::from_secs(30);

/// Where the Node's Terminal endpoint is on the Service tunnel.
const TERMINAL_PATH: &str = "/__maitre/terminal";

/// One-time tokens for Terminal connections, each for one Service.
#[derive(Clone, Default)]
pub struct Tokens(Arc<Mutex<HashMap<String, (String, Instant)>>>);

impl Tokens {
    /// A new token for a Terminal into `service`, minted at `now`.
    pub fn mint_at(&self, service: &str, now: Instant) -> String {
        let token = format!("{:032x}", rand::random::<u128>());
        let mut m = self.0.lock().unwrap();
        m.retain(|_, (_, at)| now.duration_since(*at) < TERMINAL_TOKEN_TTL);
        m.insert(token.clone(), (service.to_string(), now));
        token
    }

    /// The Service a token was minted for, if it is still good at `now`;
    /// either way it is spent.
    pub fn take_at(&self, token: &str, now: Instant) -> Option<String> {
        let (service, at) = self.0.lock().unwrap().remove(token)?;
        (now.duration_since(at) < TERMINAL_TOKEN_TTL).then_some(service)
    }
}

/// Where a running proxy relays to; swapped when the Node is reached another way.
type Upstream = Arc<RwLock<(Target, Arc<Dialer>)>>;

struct Live {
    port: u16,
    upstream: Upstream,
}

/// Hears what a page's script reports: the view's key, the event and the URL.
pub type Reporter = Arc<dyn Fn(&str, &str, &str) + Send + Sync>;

/// One proxy per Page, on a port kept across launches, and one file
/// proxy per Node.
#[derive(Default)]
pub struct Proxies {
    ports: Mutex<HashMap<String, Live>>,
    files: Mutex<HashMap<String, String>>,
    terminals: Mutex<HashMap<String, (String, Tokens)>>,
    reporter: Mutex<Option<Reporter>>,
}

/// The lowest and the number of ports a Service's port is picked from.
const PORT_BASE: u16 = 41000;
const PORT_SPAN: u16 = 9000;

/// The ports to try for a Page, in order: the one it had, then ones from
/// its host name (FNV-1a, so the same on every launch and machine), so a
/// Page lands on the same port even before the Console remembers it.
pub fn port_candidates(view: &str, remembered: Option<u16>) -> Vec<u16> {
    let mut h: u64 = 0xcbf29ce484222325;
    for b in view.bytes() {
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

    /// Returns the local URL for one Page of a Service and the port it is
    /// on, starting a proxy if needed: on `remembered` when it is free,
    /// else on a port from `view`, the Page's host name. A proxy already
    /// running for that Page is pointed at `addr` if it was reaching the
    /// Node another way.
    pub async fn open(&self, dialer: Arc<Dialer>, addr: String, fingerprint: String, view: String, service: String, page: String, remembered: Option<u16>) -> Result<(String, u16), String> {
        let target = Target { addr, fingerprint, service, page, token: String::new(), terminals: None };
        let live = self.ports.lock().unwrap().get(&view).map(|l| (l.port, l.upstream.clone()));
        if let Some((port, upstream)) = live {
            let same = {
                let cur = upstream.read().unwrap();
                cur.0.addr == target.addr && cur.0.fingerprint == target.fingerprint
            };
            if !same {
                *upstream.write().unwrap() = (target, dialer);
            }
            return Ok((format!("http://127.0.0.1:{port}/"), port));
        }
        let candidates = port_candidates(&view, remembered);
        self.serve(dialer, view, target, &candidates).await
    }

    /// Returns the local base URL (ending in the token, no slash) for a
    /// Node's file contents, starting a proxy if needed.
    pub async fn open_files(&self, dialer: Arc<Dialer>, addr: String, fingerprint: String) -> Result<String, String> {
        let key = format!("{fingerprint}@{addr}#files");
        if let Some(base) = self.files.lock().unwrap().get(&key) {
            return Ok(base.clone());
        }
        let token = format!("{:032x}", rand::random::<u128>());
        let target = Target { addr, fingerprint, service: String::new(), page: String::new(), token: token.clone(), terminals: None };
        let (root, _) = self.serve(dialer, key.clone(), target, &[]).await?;
        let base = format!("{root}{token}");
        self.files.lock().unwrap().insert(key, base.clone());
        Ok(base)
    }

    /// A URL for one Terminal connection into `service` (ADR 0011):
    /// `ws://127.0.0.1:<port>/<token>`, the token good for one upgrade or
    /// 30 s. The Node's Terminal proxy is started if needed, and follows
    /// the Node to `addr`.
    pub async fn open_terminal(&self, dialer: Arc<Dialer>, addr: String, fingerprint: String, service: String) -> Result<String, String> {
        let key = format!("{fingerprint}#terminal");
        let live = self.terminals.lock().unwrap().get(&key).cloned();
        let (root, tokens) = match live {
            Some((root, tokens)) => {
                if let Some(l) = self.ports.lock().unwrap().get(&key) {
                    let mut up = l.upstream.write().unwrap();
                    if up.0.addr != addr {
                        up.0.addr = addr;
                        up.1 = dialer;
                    }
                }
                (root, tokens)
            }
            None => {
                let tokens = Tokens::default();
                let target = Target { addr, fingerprint, service: String::new(), page: String::new(), token: String::new(), terminals: Some(tokens.clone()) };
                let (root, _) = self.serve(dialer, key.clone(), target, &[]).await?;
                self.terminals.lock().unwrap().insert(key, (root.clone(), tokens.clone()));
                (root, tokens)
            }
        };
        let token = tokens.mint_at(&service, Instant::now());
        Ok(format!("{}{token}", root.replacen("http://", "ws://", 1)))
    }

    /// Listens on the first free port of `prefer`, or on any free port when
    /// none of them is (another program has them), and relays to `target`.
    async fn serve(&self, dialer: Arc<Dialer>, key: String, target: Target, prefer: &[u16]) -> Result<(String, u16), String> {
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
        let upstream: Upstream = Arc::new(RwLock::new((target, dialer)));
        let up = upstream.clone();
        let reporter = self.reporter.lock().unwrap().clone();
        tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else { break };
                let up = up.clone();
                let reporter = reporter.clone();
                tokio::spawn(async move {
                    let svc = hyper::service::service_fn(move |req| {
                        let (target, dialer) = up.read().unwrap().clone();
                        relay(req, target, dialer, reporter.clone())
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
    dialer: Arc<Dialer>,
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
    // A Terminal proxy relays only an upgrade bearing a good token, to the
    // Node's Terminal endpoint for the token's Service; the token is spent
    // before anything is relayed.
    let mut target = target;
    if let Some(tokens) = &target.terminals {
        let token = req.uri().path().trim_start_matches('/');
        let upgrade = req.headers().contains_key(hyper::header::UPGRADE);
        let service = if token.is_empty() { None } else { tokens.take_at(token, Instant::now()) };
        let Some(service) = service.filter(|_| upgrade) else {
            return Ok(text(403, "not allowed".into()));
        };
        *req.uri_mut() = TERMINAL_PATH.parse().unwrap();
        target.service = service;
        target.page.clear();
    }
    let is_terminal = target.terminals.is_some();
    let is_page = !target.service.is_empty() && !is_terminal;
    // The Console's own paths on a page's proxy (frame.js and nav, below)
    // are answered here; no other /__maitre/ path goes to the Node, so a
    // page cannot reach the Terminal endpoint (ADR 0011).
    if is_page && req.uri().path().starts_with("/__maitre/") && req.uri().path() != FRAME_SCRIPT_PATH && req.uri().path() != NAV_PATH {
        return Ok(text(404, "not found".into()));
    }
    if is_page && req.uri().path() == FRAME_SCRIPT_PATH {
        return Ok(Response::builder()
            .status(200)
            .header("content-type", "application/javascript; charset=utf-8")
            .header("cache-control", "no-store")
            .body(http_body_util::Full::new(Bytes::from_static(FRAME_SCRIPT.as_bytes())).map_err(|never| match never {}).boxed())
            .unwrap());
    }
    if is_page && req.uri().path() == NAV_PATH {
        if req.method() == hyper::Method::POST {
            let body = req.into_body().collect().await.map(|b| b.to_bytes()).unwrap_or_default();
            if let (Some(report), Some((event, url))) = (reporter, parse_nav(&body)) {
                report(&view_key(&target), &event, &url);
            }
        }
        return Ok(Response::builder()
            .status(204)
            .header("cache-control", "no-store")
            .body(http_body_util::Empty::new().map_err(|never| match never {}).boxed())
            .unwrap());
    }
    // A document is asked for uncompressed so the frame script can be added to it.
    let document = is_page && wants_document(&req);
    if document {
        req.headers_mut().remove(hyper::header::ACCEPT_ENCODING);
    }
    match connect(&target, &dialer).await {
        Ok(mut sender) => {
            let client_upgrade = hyper::upgrade::on(&mut req);
            let wants_upgrade = req.headers().contains_key(hyper::header::UPGRADE);
            if is_page || is_terminal {
                req.headers_mut().insert(
                    HeaderName::from_static("x-maitre-service"),
                    HeaderValue::from_str(&target.service).unwrap(),
                );
                if !target.page.is_empty() {
                    req.headers_mut().insert(
                        HeaderName::from_static("x-maitre-page"),
                        HeaderValue::from_str(&target.page).unwrap(),
                    );
                }
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
    dialer: &Dialer,
) -> Result<hyper::client::conn::http1::SendRequest<Incoming>, String> {
    let stream = dialer.dial(&target.addr, &target.fingerprint, Proto::Http1).await?;
    let (sender, conn) = hyper::client::conn::http1::handshake(TokioIo::new(stream)).await.map_err(|e| e.to_string())?;
    tokio::spawn(async move {
        let _ = conn.with_upgrades().await;
    });
    Ok(sender)
}

/// The key the Console knows this Page's view by: the Page's host name,
/// the Service's own for its first Page (ADR 0010, services.PageHost).
fn view_key(target: &Target) -> String {
    if target.page.is_empty() {
        target.service.clone()
    } else {
        format!("{}-{}", target.service, target.page)
    }
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
    use crate::dial::fake;
    use crate::identity::Identity;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};
    use tokio::net::TcpStream;

    #[tokio::test]
    async fn a_page_request_reaches_its_node_through_the_dialer() {
        let node = fake::start(|tls| async move {
            let svc = hyper::service::service_fn(|req: Request<Incoming>| async move {
                let service = req.headers().get("x-maitre-service").and_then(|v| v.to_str().ok()).unwrap_or("").to_string();
                let body = format!("{} {} service={service}", req.method(), req.uri());
                Ok::<_, hyper::Error>(Response::new(http_body_util::Full::new(Bytes::from(body))))
            });
            let _ = hyper::server::conn::http1::Builder::new().serve_connection(TokioIo::new(tls), svc).await;
        })
        .await;
        let me = fake::temp_identity("device");
        let proxies = Proxies::default();
        let (_, port) = proxies
            .open(Arc::new(Dialer::new(me.clone())), node.addr.clone(), node.fingerprint.clone(), "echo".into(), "echo".into(), String::new(), None)
            .await
            .unwrap();
        let mut s = TcpStream::connect(("127.0.0.1", port)).await.unwrap();
        s.write_all(b"GET /a?b=1 HTTP/1.1\r\nHost: x\r\nConnection: close\r\n\r\n").await.unwrap();
        let mut out = String::new();
        s.read_to_string(&mut out).await.unwrap();
        assert!(out.starts_with("HTTP/1.1 200"), "{out}");
        assert!(out.ends_with("GET /a?b=1 service=echo"), "{out}");
        assert_eq!(*node.clients.lock().unwrap(), vec![me.fingerprint()]);
    }

    /// A stand-in Node that answers every request with its method, path
    /// and the Service it names, and counts them.
    async fn echo_node() -> fake::FakeNode {
        fake::start(|tls| async move {
            let svc = hyper::service::service_fn(|req: Request<Incoming>| async move {
                let service = req.headers().get("x-maitre-service").and_then(|v| v.to_str().ok()).unwrap_or("").to_string();
                let body = format!("{} {} service={service}", req.method(), req.uri());
                Ok::<_, hyper::Error>(Response::new(http_body_util::Full::new(Bytes::from(body))))
            });
            let _ = hyper::server::conn::http1::Builder::new().serve_connection(TokioIo::new(tls), svc).await;
        })
        .await
    }

    /// A WebSocket upgrade request to the proxy, read to its end.
    async fn get(port: u16, path: &str) -> String {
        let mut s = TcpStream::connect(("127.0.0.1", port)).await.unwrap();
        s.write_all(format!("GET {path} HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade, close\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n").as_bytes()).await.unwrap();
        let mut out = String::new();
        s.read_to_string(&mut out).await.unwrap();
        out
    }

    fn port_and_path(url: &str) -> (u16, String) {
        let rest = url.strip_prefix("ws://127.0.0.1:").expect(url);
        let (port, path) = rest.split_once('/').unwrap();
        (port.parse().unwrap(), format!("/{path}"))
    }

    // ADR 0011: other programs on this computer may reach the loopback
    // proxy, so a Terminal connection carries a one-time token from the
    // Desktop. Without it, or with one already used, the proxy refuses and
    // the Node never hears of it; with it, the request goes to the Node's
    // Terminal endpoint on the Service tunnel.
    #[tokio::test]
    async fn a_terminal_connection_needs_a_token_that_works_once() {
        let node = echo_node().await;
        let me = fake::temp_identity("device");
        let proxies = Proxies::default();
        let dialer = Arc::new(Dialer::new(me.clone()));
        let url = proxies.open_terminal(dialer.clone(), node.addr.clone(), node.fingerprint.clone(), "kuma".into()).await.unwrap();
        let (port, path) = port_and_path(&url);
        for bad in ["/", "/0123456789abcdef0123456789abcdef", "/__maitre/terminal"] {
            let out = get(port, bad).await;
            assert!(out.starts_with("HTTP/1.1 403"), "{bad}: {out}");
        }
        assert!(node.clients.lock().unwrap().is_empty(), "the Node heard of a refused connection");
        let out = get(port, &path).await;
        assert!(out.starts_with("HTTP/1.1 200"), "{out}");
        assert!(out.ends_with("GET /__maitre/terminal service=kuma"), "{out}");
        let out = get(port, &path).await;
        assert!(out.starts_with("HTTP/1.1 403"), "a token used twice: {out}");
        // Each Terminal gets a token of its own, on the same Node's proxy.
        let again = proxies.open_terminal(dialer, node.addr.clone(), node.fingerprint.clone(), "kuma".into()).await.unwrap();
        let (port2, path2) = port_and_path(&again);
        assert_eq!(port2, port);
        assert_ne!(path2, path);
    }

    #[test]
    fn a_terminal_token_lasts_thirty_seconds() {
        let tokens = Tokens::default();
        let t0 = Instant::now();
        let a = tokens.mint_at("kuma", t0);
        let b = tokens.mint_at("memos", t0);
        assert_eq!(tokens.take_at(&a, t0 + Duration::from_secs(29)), Some("kuma".to_string()));
        assert_eq!(tokens.take_at(&b, t0 + Duration::from_secs(31)), None);
    }

    // A Service's own page is on the same kind of proxy; it never reaches
    // the Terminal endpoint, token or not.
    #[tokio::test]
    async fn a_page_proxy_does_not_relay_the_terminal_path() {
        let node = echo_node().await;
        let me = fake::temp_identity("device");
        let proxies = Proxies::default();
        let (_, port) = proxies
            .open(Arc::new(Dialer::new(me)), node.addr.clone(), node.fingerprint.clone(), "kuma".into(), "kuma".into(), String::new(), None)
            .await
            .unwrap();
        let out = get(port, "/__maitre/terminal").await;
        assert!(out.starts_with("HTTP/1.1 404"), "{out}");
        assert!(node.clients.lock().unwrap().is_empty());
    }

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
    fn a_pages_view_key_is_its_host_name() {
        let t = |service: &str, page: &str| view_key(&Target { addr: String::new(), fingerprint: String::new(), service: service.into(), page: page.into(), token: String::new(), terminals: None });
        assert_eq!(t("immich", ""), "immich");
        assert_eq!(t("immich", "admin"), "immich-admin");
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
    async fn a_page_keeps_its_port_and_origin_when_its_node_is_reached_another_way() {
        let dir = std::env::temp_dir().join(format!("maitre-proxy-test-{:x}", rand::random::<u64>()));
        let id = Arc::new(crate::dial::Dialer::new(Arc::new(Identity::load_or_generate(&dir.join("device")).unwrap())));
        let fp = "ab".repeat(32);
        let proxies = Proxies::default();
        let (url, port) = proxies.open(id.clone(), "192.168.1.5:7443".into(), fp.clone(), "jellyfin".into(), "jellyfin".into(), String::new(), None).await.unwrap();
        assert_eq!(url, format!("http://127.0.0.1:{port}/"));
        let (again, p2) = proxies.open(id.clone(), "100.64.0.9:7443".into(), fp.clone(), "jellyfin".into(), "jellyfin".into(), String::new(), None).await.unwrap();
        assert_eq!((again, p2), (url, port));
        let up = proxies.ports.lock().unwrap().get("jellyfin").unwrap().upstream.clone();
        assert_eq!(up.read().unwrap().0.addr, "100.64.0.9:7443");
        // A remembered port that is free is taken; one that is busy is not.
        let busy = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await.unwrap();
        let taken = busy.local_addr().unwrap().port();
        let (_, p3) = proxies.open(id.clone(), "192.168.1.5:7443".into(), fp.clone(), "memos".into(), "memos".into(), String::new(), Some(taken)).await.unwrap();
        assert_ne!(p3, taken);
        assert_eq!(p3, port_candidates("memos", None)[0]);
        // A second Page of a Service is a proxy of its own, keyed by the
        // Page's host name (ADR 0010).
        let (_, admin) = proxies.open(id.clone(), "192.168.1.5:7443".into(), fp.clone(), "immich-admin".into(), "immich".into(), "admin".into(), None).await.unwrap();
        assert_eq!(admin, port_candidates("immich-admin", None)[0]);
        let up = proxies.ports.lock().unwrap().get("immich-admin").unwrap().upstream.clone();
        assert_eq!((up.read().unwrap().0.service.clone(), up.read().unwrap().0.page.clone()), ("immich".to_string(), "admin".to_string()));
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
