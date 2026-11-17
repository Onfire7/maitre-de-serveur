//! Mutual-TLS HTTP/2 client to a Node. The server certificate is accepted if
//! and only if its Ed25519 key fingerprint is one we trust; no CA, no chain.
//! Connections come from the Dialer (src/dial.rs), the one way to a Node.

use crate::dial::{Dialer, Proto};
use crate::identity::{fingerprint_of_raw_key, Identity};
use bytes::Bytes;
use http_body_util::{combinators::BoxBody, BodyExt, Full, StreamBody};
use hyper::body::{Frame, Incoming};
use hyper::client::conn::http2::SendRequest;
use hyper::{Request, Response};
use hyper_util::rt::{TokioExecutor, TokioIo};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, SignatureScheme};
use serde::Serialize;
use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use x509_parser::oid_registry::OID_SIG_ED25519;
use x509_parser::prelude::*;

#[derive(Debug)]
struct PinnedKeyVerifier {
    trusted: HashSet<String>,
    provider: Arc<rustls::crypto::CryptoProvider>,
}

impl ServerCertVerifier for PinnedKeyVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        let (_, cert) = X509Certificate::from_der(end_entity.as_ref())
            .map_err(|e| rustls::Error::General(format!("parse peer certificate: {e}")))?;
        let spki = cert.public_key();
        if spki.algorithm.algorithm != OID_SIG_ED25519 {
            return Err(rustls::Error::General("peer key is not ed25519".into()));
        }
        let fp = fingerprint_of_raw_key(&spki.subject_public_key.data);
        if self.trusted.contains(&fp) {
            Ok(ServerCertVerified::assertion())
        } else {
            Err(rustls::Error::General(format!("node {fp} is not in the trust list")))
        }
    }

    fn verify_tls12_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn verify_tls13_signature(
        &self,
        message: &[u8],
        cert: &CertificateDer<'_>,
        dss: &DigitallySignedStruct,
    ) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(message, cert, dss, &self.provider.signature_verification_algorithms)
    }

    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider.signature_verification_algorithms.supported_schemes()
    }
}

/// Accepts any Ed25519 server key and records its raw bytes. Used once per
/// Installer, before the pairing code has confirmed its key (Q66).
#[derive(Debug)]
struct RecordingVerifier {
    seen: Mutex<Option<Vec<u8>>>,
    provider: Arc<rustls::crypto::CryptoProvider>,
}

impl ServerCertVerifier for RecordingVerifier {
    fn verify_server_cert(
        &self,
        end_entity: &CertificateDer<'_>,
        _intermediates: &[CertificateDer<'_>],
        _server_name: &ServerName<'_>,
        _ocsp: &[u8],
        _now: UnixTime,
    ) -> Result<ServerCertVerified, rustls::Error> {
        let (_, cert) = X509Certificate::from_der(end_entity.as_ref())
            .map_err(|e| rustls::Error::General(format!("parse peer certificate: {e}")))?;
        let spki = cert.public_key();
        if spki.algorithm.algorithm != OID_SIG_ED25519 {
            return Err(rustls::Error::General("peer key is not ed25519".into()));
        }
        *self.seen.lock().unwrap() = Some(spki.subject_public_key.data.to_vec());
        Ok(ServerCertVerified::assertion())
    }
    fn verify_tls12_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls12_signature(m, c, d, &self.provider.signature_verification_algorithms)
    }
    fn verify_tls13_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<HandshakeSignatureValid, rustls::Error> {
        rustls::crypto::verify_tls13_signature(m, c, d, &self.provider.signature_verification_algorithms)
    }
    fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
        self.provider.signature_verification_algorithms.supported_schemes()
    }
}

/// Connect to `addr` once and return the server's raw Ed25519 public key.
/// Nothing but the handshake happens over this connection. The one
/// connection not made by the Dialer: an Installer is not yet a Node, has
/// no node id to pin, and is never reached through the Relay.
pub async fn peek_server_key(id: &Identity, addr: &str) -> Result<Vec<u8>, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let verifier = Arc::new(RecordingVerifier { seen: Mutex::new(None), provider: provider.clone() });
    let (certs, key) = own_credentials(id)?;
    let mut config = rustls::ClientConfig::builder_with_provider(provider)
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| e.to_string())?
        .dangerous()
        .with_custom_certificate_verifier(verifier.clone())
        .with_client_auth_cert(certs, key)
        .map_err(|e| format!("client auth: {e}"))?;
    config.alpn_protocols = vec![b"h2".to_vec()];
    let client = reqwest::Client::builder()
        .use_preconfigured_tls(config)
        .http2_prior_knowledge()
        .timeout(std::time::Duration::from_secs(5))
        .build()
        .map_err(|e| e.to_string())?;
    // Any request will do; the handshake is what we want. A 404 is fine.
    let _ = client.post(format!("https://{addr}/")).body(Vec::<u8>::new()).send().await.map_err(|e| e.to_string())?;
    verifier.seen.lock().unwrap().clone().ok_or_else(|| "no server key seen".to_string())
}

fn own_credentials(id: &Identity) -> Result<(Vec<CertificateDer<'static>>, PrivateKeyDer<'static>), String> {
    let certs: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut id.cert_pem().as_bytes())
        .collect::<Result<_, _>>()
        .map_err(|e| format!("parse own certificate: {e}"))?;
    let key_pem = id.key_pem();
    let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut key_pem.as_bytes())
        .map_err(|e| format!("parse own key: {e}"))?
        .ok_or("own key PEM contained no key")?;
    Ok((certs, key))
}

/// rustls config presenting `id`, trusting only `fingerprint`, with the given ALPN list.
pub(crate) fn pinned_tls_config(id: &Identity, fingerprint: &str, alpn: &[Vec<u8>]) -> Result<rustls::ClientConfig, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());
    let (certs, key) = own_credentials(id)?;
    let mut config = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| e.to_string())?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(PinnedKeyVerifier {
            trusted: HashSet::from([fingerprint.trim().to_lowercase()]),
            provider,
        }))
        .with_client_auth_cert(certs, key)
        .map_err(|e| format!("client auth: {e}"))?;
    config.alpn_protocols = alpn.to_vec();
    Ok(config)
}

/// A request or response body: bytes, or a stream of them.
pub type Body = BoxBody<Bytes, BoxError>;
pub type BoxError = Box<dyn std::error::Error + Send + Sync>;

/// A body of bytes already in hand.
pub fn full_body(bytes: impl Into<Bytes>) -> Body {
    Full::new(bytes.into()).map_err(|never| match never {}).boxed()
}

/// A body streamed as it is read, for uploads.
pub fn stream_body<S, E>(chunks: S) -> Body
where
    S: futures_util::Stream<Item = Result<Bytes, E>> + Send + Sync + 'static,
    E: Into<BoxError> + 'static,
{
    use futures_util::TryStreamExt;
    StreamBody::new(chunks.map_ok(Frame::data).map_err(Into::into)).boxed()
}

/// What a Connect unary call returned.
#[derive(Serialize)]
pub struct RpcResponse {
    pub status: u16,
    pub content_type: String,
    pub body: Vec<u8>,
}

/// HTTP/2 to Nodes over connections from the Dialer: RPCs and file
/// transfers. One connection per Node and address, dialled again when the
/// Node has closed it.
pub struct NodeClient {
    dialer: Arc<Dialer>,
    conns: Mutex<HashMap<(String, String), SendRequest<Body>>>,
}

/// How long a unary RPC may take, connecting included.
const CALL_TIMEOUT: Duration = Duration::from_secs(15);

impl NodeClient {
    pub fn new(dialer: Arc<Dialer>) -> Self {
        Self { dialer, conns: Mutex::new(HashMap::new()) }
    }

    async fn connect(&self, addr: &str, fingerprint: &str) -> Result<SendRequest<Body>, String> {
        let stream = self.dialer.dial(addr, fingerprint, Proto::H2).await?;
        let (sender, conn) = hyper::client::conn::http2::Builder::new(TokioExecutor::new())
            .handshake(TokioIo::new(stream))
            .await
            .map_err(|e| why(&e))?;
        tokio::spawn(async move {
            let _ = conn.await;
        });
        Ok(sender)
    }

    /// Send `req`, whose URI is a path and query, to the Node `fingerprint`
    /// at `addr`. Fails, saying the address could not be reached, when no
    /// answer came back; an answer of any status is Ok.
    pub async fn send(&self, addr: &str, fingerprint: &str, req: Request<Body>) -> Result<Response<Incoming>, String> {
        self.attempt(addr, fingerprint, req).await.map_err(|f| f.why)
    }

    async fn attempt(&self, addr: &str, fingerprint: &str, req: Request<Body>) -> Result<Response<Incoming>, Failure> {
        let unreachable = |why: String| Failure { sent: false, why: format!("{addr} could not be reached: {why}") };
        let dropped = |why: String| Failure { sent: true, why: format!("{addr} dropped the call before answering: {why}") };
        let key = (fingerprint.trim().to_lowercase(), addr.to_string());
        let (mut parts, body) = req.into_parts();
        let pq = parts.uri.path_and_query().map(|p| p.as_str()).unwrap_or("/");
        // HTTP/2 needs a scheme and authority; the Node routes by path alone.
        parts.uri = format!("https://{addr}{pq}").parse().map_err(|e: hyper::http::uri::InvalidUri| Failure { sent: false, why: e.to_string() })?;
        let mut req = Request::from_parts(parts, body);
        let pooled = self.conns.lock().unwrap().get(&key).filter(|s| s.is_ready()).cloned();
        if let Some(mut sender) = pooled {
            match sender.try_send_request(req).await {
                Ok(res) => return Ok(res),
                // The Node closed the connection before taking the request: dial again.
                Err(mut e) => match e.take_message() {
                    Some(r) => req = r,
                    None => {
                        self.conns.lock().unwrap().remove(&key);
                        return Err(dropped(why(e.error())));
                    }
                },
            }
        }
        self.conns.lock().unwrap().remove(&key);
        let mut sender = self.connect(addr, fingerprint).await.map_err(unreachable)?;
        self.conns.lock().unwrap().insert(key.clone(), sender.clone());
        match sender.try_send_request(req).await {
            Ok(res) => Ok(res),
            Err(mut e) => {
                self.conns.lock().unwrap().remove(&key);
                let why = why(e.error());
                Err(if e.take_message().is_some() { unreachable(why) } else { dropped(why) })
            }
        }
    }

    /// One Connect-protocol unary call: `procedure` is like
    /// "/maitre.v1.NodeService/GetNodeInfo". A call that only reads (see
    /// `idempotent`) is sent once more when the connection drops before
    /// the answer, as connections relayed through an anchor that failed
    /// over do (ADR 0012); any other call is not, since the Node may have
    /// carried it out.
    pub async fn call(&self, addr: &str, fingerprint: &str, procedure: &str, body: Vec<u8>) -> Result<RpcResponse, String> {
        match self.call_once(addr, fingerprint, procedure, body.clone()).await {
            Err(f) if f.sent && idempotent(procedure) => self.call_once(addr, fingerprint, procedure, body).await.map_err(|f| f.why),
            r => r.map_err(|f| f.why),
        }
    }

    async fn call_once(&self, addr: &str, fingerprint: &str, procedure: &str, body: Vec<u8>) -> Result<RpcResponse, Failure> {
        let req = Request::post(procedure)
            .header("content-type", "application/proto")
            .header("connect-protocol-version", "1")
            .body(full_body(body))
            .map_err(|e| Failure { sent: false, why: e.to_string() })?;
        let call = async {
            let res = self.attempt(addr, fingerprint, req).await?;
            let status = res.status().as_u16();
            let content_type = res.headers().get("content-type").and_then(|v| v.to_str().ok()).unwrap_or("").to_string();
            let body = res
                .into_body()
                .collect()
                .await
                .map_err(|e| Failure { sent: true, why: format!("{addr} dropped the call before answering: {}", why(&e)) })?
                .to_bytes()
                .to_vec();
            Ok(RpcResponse { status, content_type, body })
        };
        match tokio::time::timeout(CALL_TIMEOUT, call).await {
            Ok(r) => r,
            Err(_) => Err(Failure { sent: false, why: format!("{addr} could not be reached: did not answer in {} s", CALL_TIMEOUT.as_secs()) }),
        }
    }
}

/// Why a request failed, and whether it had reached the Node first.
struct Failure {
    sent: bool,
    why: String,
}

/// Whether the procedure only reads, so sending it twice is harmless: the
/// Get and List calls, and the reads named otherwise. The protos declare no
/// idempotency level, so it is by name; a new read that fits neither is
/// simply not retried.
fn idempotent(procedure: &str) -> bool {
    const READS: &[&str] = &["Usage", "DriveContents", "PlanDrain", "PreviewSetup", "CheckService", "DebugReadFile"];
    let method = procedure.rsplit('/').next().unwrap_or_default();
    let prefixed = |p: &str| method.strip_prefix(p).and_then(|rest| rest.chars().next()).is_some_and(|c| c.is_ascii_uppercase());
    prefixed("Get") || prefixed("List") || READS.contains(&method)
}

/// An error with its causes: hyper's own words ("connection error") hide
/// the refused connection or reset underneath, which is what the user needs.
fn why(e: &(dyn std::error::Error + 'static)) -> String {
    let mut out = vec![e.to_string()];
    let mut cur = e.source();
    while let Some(c) = cur {
        let s = c.to_string();
        if !s.is_empty() && !out.contains(&s) {
            out.push(s);
        }
        cur = c.source();
    }
    out.join(": ")
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::dial::{fake, Dialer};
    use http_body_util::BodyExt;
    use hyper_util::rt::{TokioExecutor, TokioIo};

    /// A Node answering every request with what it was asked, over HTTP/2.
    /// With `close_after_one`, it closes each connection after its first
    /// answer, as a Node that restarted would.
    async fn h2_node(close_after_one: bool) -> fake::FakeNode {
        fake::start(move |tls| async move {
            let done = Arc::new(tokio::sync::Notify::new());
            let answered = done.clone();
            let svc = hyper::service::service_fn(move |req: hyper::Request<hyper::body::Incoming>| {
                let answered = answered.clone();
                async move {
                    let head = format!("{} {} {}", req.method(), req.uri().path_and_query().map(|p| p.as_str()).unwrap_or(""), req.headers().get("content-type").and_then(|v| v.to_str().ok()).unwrap_or(""));
                    let body = req.into_body().collect().await?.to_bytes();
                    answered.notify_one();
                    Ok::<_, hyper::Error>(
                        hyper::Response::builder()
                            .header("content-type", "application/proto")
                            .body(http_body_util::Full::new(Bytes::from(format!("{head} {}", String::from_utf8_lossy(&body)))))
                            .unwrap(),
                    )
                }
            });
            let conn = hyper::server::conn::http2::Builder::new(TokioExecutor::new()).serve_connection(TokioIo::new(tls), svc);
            tokio::pin!(conn);
            if close_after_one {
                tokio::select! {
                    _ = conn.as_mut() => return,
                    _ = done.notified() => {}
                }
                // Let the answer go out, then drop the connection.
                let _ = tokio::time::timeout(std::time::Duration::from_millis(20), conn.as_mut()).await;
                return;
            }
            let _ = conn.await;
        })
        .await
    }

    fn client() -> (NodeClient, Arc<Identity>) {
        let me = fake::temp_identity("device");
        (NodeClient::new(Arc::new(Dialer::new(me.clone()))), me)
    }

    #[tokio::test]
    async fn rpcs_reach_their_node_over_one_connection() {
        let node = h2_node(false).await;
        let (client, me) = client();
        let res = client.call(&node.addr, &node.fingerprint, "/maitre.v1.NodeService/GetNodeInfo", b"hi".to_vec()).await.unwrap();
        assert_eq!(res.status, 200);
        assert_eq!(res.content_type, "application/proto");
        assert_eq!(String::from_utf8(res.body).unwrap(), "POST /maitre.v1.NodeService/GetNodeInfo application/proto hi");
        let again = client.call(&node.addr, &node.fingerprint, "/maitre.v1.NodeService/ListSpaces", vec![]).await.unwrap();
        assert_eq!(String::from_utf8(again.body).unwrap(), "POST /maitre.v1.NodeService/ListSpaces application/proto ");
        assert_eq!(*node.clients.lock().unwrap(), vec![me.fingerprint()]);
        assert_eq!(node.connections.load(std::sync::atomic::Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn a_connection_the_node_closed_is_dialled_again() {
        let node = h2_node(true).await;
        let (client, _) = client();
        for _ in 0..3 {
            let res = client.call(&node.addr, &node.fingerprint, "/p", vec![]).await.expect("call");
            assert_eq!(res.status, 200);
            tokio::time::sleep(std::time::Duration::from_millis(50)).await;
        }
        assert_eq!(node.connections.load(std::sync::atomic::Ordering::SeqCst), 3);
    }

    #[tokio::test]
    async fn a_request_streams_its_body_and_query_to_the_node() {
        let node = h2_node(false).await;
        let (client, _) = client();
        let chunks = futures_util::stream::iter(vec![Ok::<_, std::io::Error>(Bytes::from("a")), Ok(Bytes::from("b"))]);
        let req = hyper::Request::put("/maitre/files/content?path=a%20b").body(stream_body(chunks)).unwrap();
        let res = client.send(&node.addr, &node.fingerprint, req).await.unwrap();
        let body = res.into_body().collect().await.unwrap().to_bytes();
        assert_eq!(body, "PUT /maitre/files/content?path=a%20b  ab");
    }

    #[tokio::test]
    async fn an_rpc_to_an_address_that_does_not_answer_says_which() {
        let addr = fake::dead_addr().await;
        let (client, _) = client();
        let err = client.call(&addr, &"00".repeat(32), "/p", vec![]).await.err().expect("no answer");
        assert!(err.starts_with(&format!("{addr} could not be reached: ")), "{err}");
    }

    #[tokio::test]
    async fn an_rpc_to_a_node_with_another_key_is_refused() {
        let node = h2_node(false).await;
        let (client, _) = client();
        let err = client.call(&node.addr, &"00".repeat(32), "/p", vec![]).await.err().expect("refused");
        assert!(err.contains("trust list"), "{err}");
    }

    /// A Node that holds each request `hold` before answering it, noting
    /// the procedures asked for.
    async fn slow_h2_node(hold: std::time::Duration, asked: Arc<Mutex<Vec<String>>>) -> fake::FakeNode {
        fake::start(move |tls| {
            let asked = asked.clone();
            async move {
                let svc = hyper::service::service_fn(move |req: hyper::Request<hyper::body::Incoming>| {
                    let asked = asked.clone();
                    async move {
                        asked.lock().unwrap().push(req.uri().path().to_string());
                        tokio::time::sleep(hold).await;
                        Ok::<_, hyper::Error>(hyper::Response::new(http_body_util::Full::new(Bytes::from("ok"))))
                    }
                });
                let _ = hyper::server::conn::http2::Builder::new(TokioExecutor::new()).serve_connection(TokioIo::new(tls), svc).await;
            }
        })
        .await
    }

    /// A Node reached only through `anchor`'s Relay, and another anchor
    /// that relays to it too, to fail over to.
    async fn relayed_node(asked: Arc<Mutex<Vec<String>>>) -> (fake::FakeNode, fake::FakeNode, fake::FakeNode, String) {
        let node = slow_h2_node(std::time::Duration::from_millis(600), asked).await;
        let anchor = fake::anchor_to(node.addr.clone(), Arc::new(Mutex::new(Vec::new()))).await;
        let next = fake::anchor_to(node.addr.clone(), Arc::new(Mutex::new(Vec::new()))).await;
        (node, anchor, next, fake::dead_addr().await)
    }

    fn anchor(n: &fake::FakeNode) -> Option<crate::dial::Anchor> {
        Some(crate::dial::Anchor { addr: n.addr.clone(), fingerprint: n.fingerprint.clone() })
    }

    #[tokio::test]
    async fn after_the_anchor_fails_over_an_idempotent_rpc_in_flight_is_retried_once_through_the_new_anchor() {
        let asked = Arc::new(Mutex::new(Vec::new()));
        let (node, old, new, dead) = relayed_node(asked.clone()).await;
        let dialer = Arc::new(Dialer::new(fake::temp_identity("device")));
        dialer.relay_through(anchor(&old));
        let client = NodeClient::new(dialer.clone());
        let call = client.call(&dead, &node.fingerprint, "/maitre.v1.NodeService/GetNodeInfo", vec![]);
        let fail_over = async {
            tokio::time::sleep(std::time::Duration::from_millis(300)).await;
            dialer.relay_through(anchor(&new));
        };
        let (res, _) = tokio::join!(call, fail_over);
        assert_eq!(res.expect("retried").status, 200);
        assert_eq!(*asked.lock().unwrap(), vec!["/maitre.v1.NodeService/GetNodeInfo"; 2]);
        assert_eq!(new.connections.load(std::sync::atomic::Ordering::SeqCst), 1);
    }

    #[tokio::test]
    async fn after_the_anchor_fails_over_an_rpc_that_is_not_idempotent_is_not_retried() {
        let asked = Arc::new(Mutex::new(Vec::new()));
        let (node, old, new, dead) = relayed_node(asked.clone()).await;
        let dialer = Arc::new(Dialer::new(fake::temp_identity("device")));
        dialer.relay_through(anchor(&old));
        let client = NodeClient::new(dialer.clone());
        let call = client.call(&dead, &node.fingerprint, "/maitre.v1.NodeService/Reboot", vec![]);
        let fail_over = async {
            tokio::time::sleep(std::time::Duration::from_millis(300)).await;
            dialer.relay_through(anchor(&new));
        };
        let (res, _) = tokio::join!(call, fail_over);
        let err = res.err().expect("not retried");
        // Not "could not be reached", which would have the Console try the
        // Node's other addresses: the call may have been carried out.
        assert!(err.starts_with(&format!("{dead} dropped the call before answering: ")), "{err}");
        assert_eq!(*asked.lock().unwrap(), vec!["/maitre.v1.NodeService/Reboot"]);
        assert_eq!(new.connections.load(std::sync::atomic::Ordering::SeqCst), 0);
    }

    #[test]
    fn reads_are_idempotent_and_changes_are_not() {
        for p in ["/maitre.v1.NodeService/GetNodeInfo", "/maitre.v1.StorageService/ListDrives", "/maitre.v1.FilesService/ListDir", "/maitre.v1.FilesService/Usage", "/maitre.v1.StorageService/DriveContents", "/maitre.v1.StorageService/PlanDrain", "/maitre.v1.ServiceService/PreviewSetup"] {
            assert!(idempotent(p), "{p}");
        }
        for p in ["/maitre.v1.NodeService/Reboot", "/maitre.v1.NodeService/PutRecord", "/maitre.v1.FilesService/Delete", "/maitre.v1.ServiceService/InstallService", "/maitre.v1.NodeService/DebugExec", "/maitre.v1.NodeService/Restart", "/x/Get", "/x/Listen"] {
            assert!(!idempotent(p), "{p}");
        }
    }

    // Cross-language check against a running `maitre dev`. Skipped unless
    // MAITRE_TEST_NODE_ADDR and MAITRE_TEST_NODE_FP are set; `make m0-check`
    // sets up both sides and runs it.
    fn env(name: &str) -> Option<String> {
        std::env::var(name).ok().filter(|v| !v.is_empty())
    }

    #[tokio::test]
    async fn trusted_device_reaches_go_dev_node() {
        // Creating the identity first lets a setup pass generate the key
        // (with only MAITRE_TEST_DEVICE_DIR set) before the Node is started.
        let Some(id_dir) = env("MAITRE_TEST_DEVICE_DIR") else {
            eprintln!("skipped: MAITRE_TEST_DEVICE_DIR not set");
            return;
        };
        let id = Arc::new(Identity::load_or_generate(std::path::Path::new(&id_dir)).unwrap());
        let (Some(addr), Some(fp)) = (env("MAITRE_TEST_NODE_ADDR"), env("MAITRE_TEST_NODE_FP")) else {
            eprintln!("skipped: node not configured");
            return;
        };
        let client = NodeClient::new(Arc::new(Dialer::new(id)));
        let res = client.call(&addr, &fp, "/maitre.v1.NodeService/GetNodeInfo", vec![]).await.expect("mTLS call to Go dev node");
        assert_eq!(res.status, 200, "body: {:?}", String::from_utf8_lossy(&res.body));
    }

    #[tokio::test]
    async fn wrong_fingerprint_is_refused() {
        let (Some(addr), Some(id_dir)) = (env("MAITRE_TEST_NODE_ADDR"), env("MAITRE_TEST_DEVICE_DIR")) else {
            return;
        };
        let id = Arc::new(Identity::load_or_generate(std::path::Path::new(&id_dir)).unwrap());
        let client = NodeClient::new(Arc::new(Dialer::new(id)));
        let err = client.call(&addr, &"00".repeat(32), "/maitre.v1.NodeService/GetNodeInfo", vec![]).await.err().expect("connected to a node with the wrong fingerprint");
        assert!(err.contains("trust list"), "unexpected error: {err}");
    }
}
