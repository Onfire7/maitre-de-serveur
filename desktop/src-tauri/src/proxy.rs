//! Loopback proxy for the built-in browser (decision 71). Each opened
//! Service gets a port on 127.0.0.1; the webview talks plain HTTP to it and
//! every request is relayed over mutual TLS to the Node's agent with the
//! X-Maitre-Service header, which the agent routes to the Service. HTTP/1.1
//! end to end so WebSocket upgrades (chat-style apps, dashboards) work.
//!
//! A Service's page is cross-origin to the Console, which can neither read
//! where the page has gone nor move it back and forward (decision 286). So
//! a page proxy serves a small script at /__maitre/frame.js and adds a tag
//! for it to every HTML document it relays (asked for without compression,
//! so the document can be edited). The script tells the Console the frame's
//! address on each load and on pushState, replaceState and popstate, and
//! takes the Console's go, reload and stop. A page whose Content-Security-
//! Policy refuses the script works as before, without the controls.

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
use std::sync::{Arc, Mutex};
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

/// One proxy per (node, service), and one file proxy per Node; ports are
/// reused for the app's lifetime.
#[derive(Default)]
pub struct Proxies {
    ports: Mutex<HashMap<String, u16>>,
    files: Mutex<HashMap<String, String>>,
}

impl Proxies {
    /// Returns the local URL for a Service, starting a proxy if needed.
    pub async fn open(&self, id: Arc<Identity>, addr: String, fingerprint: String, service: String) -> Result<String, String> {
        let key = format!("{fingerprint}@{addr}/{service}");
        if let Some(p) = self.ports.lock().unwrap().get(&key) {
            return Ok(format!("http://127.0.0.1:{p}/"));
        }
        let target = Target { addr, fingerprint, service, token: String::new() };
        self.serve(id, key, target).await
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
        let root = self.serve(id, key.clone(), target).await?;
        let base = format!("{root}{token}");
        self.files.lock().unwrap().insert(key, base.clone());
        Ok(base)
    }

    async fn serve(&self, id: Arc<Identity>, key: String, target: Target) -> Result<String, String> {
        let listener = TcpListener::bind(SocketAddr::from(([127, 0, 0, 1], 0))).await.map_err(|e| e.to_string())?;
        let port = listener.local_addr().map_err(|e| e.to_string())?.port();
        let tls = Arc::new(pinned_tls_config(&id, &target.fingerprint, &[b"http/1.1".to_vec()])?);
        tokio::spawn(async move {
            loop {
                let Ok((stream, _)) = listener.accept().await else { break };
                let target = target.clone();
                let tls = tls.clone();
                tokio::spawn(async move {
                    let svc = hyper::service::service_fn(move |req| relay(req, target.clone(), tls.clone()));
                    let _ = hyper::server::conn::http1::Builder::new()
                        .preserve_header_case(true)
                        .serve_connection(TokioIo::new(stream), svc)
                        .with_upgrades()
                        .await;
                });
            }
        });
        self.ports.lock().unwrap().insert(key, port);
        Ok(format!("http://127.0.0.1:{port}/"))
    }
}

/// Relay one request (and, for upgrades, the resulting byte stream).
async fn relay(
    mut req: Request<Incoming>,
    target: Target,
    tls: Arc<rustls::ClientConfig>,
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

/// Runs inside a Service's page (decision 286). It reports the page's
/// address to the Console on load and on every History change, and does
/// what the Console asks: `go` (replace the address, so the window's own
/// history is left alone), `reload`, `stop`.
const FRAME_SCRIPT: &str = r#"(function () {
  if (window.parent === window) return;
  var send = function (event) {
    try { window.parent.postMessage({ maitre: "frame", event: event, url: location.href }, "*"); } catch (e) {}
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
  window.addEventListener("message", function (e) {
    var d = e.data;
    if (!d || d.maitre !== "frame-cmd" || e.source !== window.parent) return;
    if (d.cmd === "go" && typeof d.url === "string") location.replace(d.url);
    else if (d.cmd === "reload") location.reload();
    else if (d.cmd === "stop") window.stop();
  });
  send("load");
})();
"#;

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
