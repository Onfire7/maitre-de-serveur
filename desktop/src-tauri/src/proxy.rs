//! Loopback proxy for the built-in browser (decision 71). Each opened
//! Service gets a port on 127.0.0.1; the webview talks plain HTTP to it and
//! every request is relayed over mutual TLS to the Node's agent with the
//! X-Maitre-Service header, which the agent routes to the Service. HTTP/1.1
//! end to end so WebSocket upgrades (chat-style apps, dashboards) work.

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
    match connect(&target, tls).await {
        Ok(mut sender) => {
            let client_upgrade = hyper::upgrade::on(&mut req);
            let wants_upgrade = req.headers().contains_key(hyper::header::UPGRADE);
            if !target.service.is_empty() {
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
