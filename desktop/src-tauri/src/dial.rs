//! The one way the Desktop connects to a Node (ADR 0012): `Dialer::dial`
//! returns a mutual-TLS stream to a Node whose key is the pinned node id.
//! RPCs, file transfers and the loopback proxy (Pages, file contents) all
//! dial through it, so how a Node is reached is decided here and nowhere
//! else: directly, or, when that fails, through the anchor's Relay (an
//! HTTP/1.1 Upgrade to the anchor naming the Node, then a second mTLS
//! handshake inside it, pinned to that Node, so the anchor carries only
//! ciphertext).

use crate::identity::Identity;
use crate::rpc::pinned_tls_config;
use std::collections::HashMap;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;
use tokio_rustls::TlsConnector;

/// Where the anchor's Relay asks for a Node (the Node's `relay.Path`).
const RELAY_PATH: &str = "/maitre/relay-node";
/// The Upgrade protocol the Relay speaks.
const RELAY_TOKEN: &str = "maitre-relay";
/// How long a direct connection may take, when there is a Relay to fall
/// back on, before it counts as failed. An address of the home network
/// seen from away is often silently dropped rather than refused.
const DIRECT_TIMEOUT: Duration = Duration::from_secs(3);

/// The Node the Console reads the Fleet through, whose Relay reaches the
/// Nodes this Device cannot reach itself.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Anchor {
    pub addr: String,
    pub fingerprint: String,
}

/// What the connection will speak, offered in TLS ALPN: HTTP/2 for RPCs
/// and file transfers, HTTP/1.1 for the loopback proxy, whose WebSocket
/// upgrades need it.
#[derive(Clone, Copy, PartialEq, Eq, Hash, Debug)]
pub enum Proto {
    H2,
    Http1,
}

impl Proto {
    fn alpn(self) -> Vec<u8> {
        match self {
            Proto::H2 => b"h2".to_vec(),
            Proto::Http1 => b"http/1.1".to_vec(),
        }
    }
}

/// A verified connection to a Node.
pub trait NodeIo: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send {}
impl<T: tokio::io::AsyncRead + tokio::io::AsyncWrite + Unpin + Send> NodeIo for T {}
pub type NodeStream = Box<dyn NodeIo>;

/// Dials Nodes as one identity: this Device's, or the Recovery Phrase's.
/// Keeps one TLS configuration per Node and protocol, so a Node's sessions
/// resume rather than redoing the whole handshake.
pub struct Dialer {
    id: Arc<Identity>,
    configs: Mutex<HashMap<(String, Proto), Arc<rustls::ClientConfig>>>,
    anchor: Mutex<Option<Anchor>>,
}

fn same_node(a: &str, b: &str) -> bool {
    a.trim().eq_ignore_ascii_case(b.trim())
}

impl Dialer {
    pub fn new(id: Arc<Identity>) -> Self {
        Self { id, configs: Mutex::new(HashMap::new()), anchor: Mutex::new(None) }
    }

    /// Relay through this anchor from now on, or through none.
    pub fn relay_through(&self, anchor: Option<Anchor>) {
        *self.anchor.lock().unwrap() = anchor;
    }

    fn config(&self, fingerprint: &str, proto: Proto) -> Result<Arc<rustls::ClientConfig>, String> {
        let key = (fingerprint.trim().to_lowercase(), proto);
        if let Some(c) = self.configs.lock().unwrap().get(&key) {
            return Ok(c.clone());
        }
        let c = Arc::new(pinned_tls_config(&self.id, &key.0, &[proto.alpn()])?);
        self.configs.lock().unwrap().insert(key, c.clone());
        Ok(c)
    }

    /// Connect to the Node `fingerprint` at `addr`, speaking `proto`, or,
    /// when `addr` cannot be reached that way, through the anchor's Relay.
    /// Fails if neither reaches it or the key there is not `fingerprint`.
    pub async fn dial(&self, addr: &str, fingerprint: &str, proto: Proto) -> Result<NodeStream, String> {
        let anchor = self.anchor.lock().unwrap().clone().filter(|a| !same_node(&a.fingerprint, fingerprint));
        let Some(anchor) = anchor else {
            return self.direct(addr, fingerprint, proto).await;
        };
        let direct = match tokio::time::timeout(DIRECT_TIMEOUT, self.direct(addr, fingerprint, proto)).await {
            Ok(Ok(s)) => return Ok(s),
            Ok(Err(e)) => e,
            Err(_) => format!("no answer in {} s", DIRECT_TIMEOUT.as_secs()),
        };
        self.relayed(&anchor, fingerprint, proto).await.map_err(|e| format!("{direct}; through the anchor: {e}"))
    }

    async fn direct(&self, addr: &str, fingerprint: &str, proto: Proto) -> Result<NodeStream, String> {
        let config = self.config(fingerprint, proto)?;
        let tcp = TcpStream::connect(addr).await.map_err(|e| e.to_string())?;
        let _ = tcp.set_nodelay(true);
        let name = rustls::pki_types::ServerName::try_from("maitre").map_err(|e| e.to_string())?;
        let tls = TlsConnector::from(config).connect(name, tcp).await.map_err(|e| e.to_string())?;
        Ok(Box::new(tls))
    }

    /// Ask the anchor to relay to the Node `fingerprint`, then shake hands
    /// with that Node inside the relayed stream.
    async fn relayed(&self, anchor: &Anchor, fingerprint: &str, proto: Proto) -> Result<NodeStream, String> {
        let config = self.config(fingerprint, proto)?;
        let mut outer = self
            .direct(&anchor.addr, &anchor.fingerprint, Proto::Http1)
            .await
            .map_err(|e| format!("{} could not be reached: {e}", anchor.addr))?;
        let node: String = form_urlencoded::byte_serialize(fingerprint.trim().to_lowercase().as_bytes()).collect();
        let ask = format!("GET {RELAY_PATH}?node={node} HTTP/1.1\r\nHost: maitre\r\nConnection: Upgrade\r\nUpgrade: {RELAY_TOKEN}\r\n\r\n");
        outer.write_all(ask.as_bytes()).await.map_err(|e| e.to_string())?;
        outer.flush().await.map_err(|e| e.to_string())?;
        let head = read_head(&mut outer).await?;
        let mut lines = head.split("\r\n");
        let status = lines.next().unwrap_or_default().splitn(2, ' ').nth(1).unwrap_or_default().to_string();
        if !status.starts_with("101") {
            let len = lines
                .filter_map(|l| l.split_once(':'))
                .find(|(k, _)| k.trim().eq_ignore_ascii_case("content-length"))
                .and_then(|(_, v)| v.trim().parse::<usize>().ok())
                .unwrap_or(0)
                .min(1024);
            let mut body = vec![0u8; len];
            let _ = tokio::time::timeout(Duration::from_secs(5), outer.read_exact(&mut body)).await;
            let why = String::from_utf8_lossy(&body).trim().to_string();
            return Err(if why.is_empty() { format!("the anchor refused the Relay: {status}") } else { format!("the anchor refused the Relay: {status}: {why}") });
        }
        let name = rustls::pki_types::ServerName::try_from("maitre").map_err(|e| e.to_string())?;
        let tls = TlsConnector::from(config).connect(name, outer).await.map_err(|e| e.to_string())?;
        Ok(Box::new(tls))
    }
}

/// Read an HTTP/1.1 response head, byte by byte so nothing after it (the
/// relayed Node's first bytes) is taken.
async fn read_head(s: &mut NodeStream) -> Result<String, String> {
    let mut head = Vec::new();
    let mut b = [0u8; 1];
    while !head.ends_with(b"\r\n\r\n") {
        if head.len() > 16 * 1024 {
            return Err("the anchor's answer is too long".into());
        }
        s.read_exact(&mut b).await.map_err(|e| format!("the anchor did not answer the Relay: {e}"))?;
        head.push(b[0]);
    }
    Ok(String::from_utf8_lossy(&head).into_owned())
}

#[cfg(test)]
pub(crate) mod fake {
    //! A stand-in Node for tests: a TLS listener presenting a Node key that
    //! asks for the client's certificate and records whose it was.
    use crate::identity::{fingerprint_of_raw_key, Identity};
    use rustls::pki_types::{CertificateDer, PrivateKeyDer, UnixTime};
    use rustls::server::danger::{ClientCertVerified, ClientCertVerifier};
    use rustls::{DigitallySignedStruct, DistinguishedName, SignatureScheme};
    use std::future::Future;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::{Arc, Mutex};
    use tokio::net::{TcpListener, TcpStream};
    use tokio_rustls::server::TlsStream;

    pub fn temp_identity(name: &str) -> Arc<Identity> {
        let dir = std::env::temp_dir().join(format!("maitre-{name}-{:x}", rand::random::<u64>()));
        Arc::new(Identity::load_or_generate(&dir).unwrap())
    }

    #[derive(Debug)]
    struct AnyClient {
        seen: Arc<Mutex<Vec<String>>>,
        provider: Arc<rustls::crypto::CryptoProvider>,
    }

    impl ClientCertVerifier for AnyClient {
        fn root_hint_subjects(&self) -> &[DistinguishedName] {
            &[]
        }
        fn verify_client_cert(&self, end: &CertificateDer<'_>, _: &[CertificateDer<'_>], _: UnixTime) -> Result<ClientCertVerified, rustls::Error> {
            use x509_parser::prelude::*;
            let (_, cert) = X509Certificate::from_der(end.as_ref()).map_err(|e| rustls::Error::General(e.to_string()))?;
            self.seen.lock().unwrap().push(fingerprint_of_raw_key(&cert.public_key().subject_public_key.data));
            Ok(ClientCertVerified::assertion())
        }
        fn verify_tls12_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
            rustls::crypto::verify_tls12_signature(m, c, d, &self.provider.signature_verification_algorithms)
        }
        fn verify_tls13_signature(&self, m: &[u8], c: &CertificateDer<'_>, d: &DigitallySignedStruct) -> Result<rustls::client::danger::HandshakeSignatureValid, rustls::Error> {
            rustls::crypto::verify_tls13_signature(m, c, d, &self.provider.signature_verification_algorithms)
        }
        fn supported_verify_schemes(&self) -> Vec<SignatureScheme> {
            self.provider.signature_verification_algorithms.supported_schemes()
        }
    }

    pub struct FakeNode {
        pub addr: String,
        pub fingerprint: String,
        /// Fingerprints of the client keys presented, one per full
        /// handshake (a resumed session presents none).
        pub clients: Arc<Mutex<Vec<String>>>,
        /// How many TLS connections were accepted.
        pub connections: Arc<AtomicUsize>,
    }

    /// Start a Node that hands each accepted TLS connection to `serve`.
    pub async fn start<F, Fut>(serve: F) -> FakeNode
    where
        F: Fn(TlsStream<TcpStream>) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let node = temp_identity("fake-node");
        let provider = Arc::new(rustls::crypto::ring::default_provider());
        let clients = Arc::new(Mutex::new(Vec::new()));
        let certs: Vec<CertificateDer<'static>> =
            rustls_pemfile::certs(&mut node.cert_pem().as_bytes()).collect::<Result<_, _>>().unwrap();
        let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut node.key_pem().as_bytes()).unwrap().unwrap();
        let mut config = rustls::ServerConfig::builder_with_provider(provider.clone())
            .with_protocol_versions(&[&rustls::version::TLS13])
            .unwrap()
            .with_client_cert_verifier(Arc::new(AnyClient { seen: clients.clone(), provider }))
            .with_single_cert(certs, key)
            .unwrap();
        config.alpn_protocols = vec![b"h2".to_vec(), b"http/1.1".to_vec()];
        let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(config));
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap().to_string();
        let serve = Arc::new(serve);
        let connections = Arc::new(AtomicUsize::new(0));
        let counted = connections.clone();
        tokio::spawn(async move {
            while let Ok((tcp, _)) = listener.accept().await {
                let (acceptor, serve, counted) = (acceptor.clone(), serve.clone(), counted.clone());
                tokio::spawn(async move {
                    if let Ok(tls) = acceptor.accept(tcp).await {
                        counted.fetch_add(1, Ordering::SeqCst);
                        serve(tls).await;
                    }
                });
            }
        });
        FakeNode { addr, fingerprint: node.fingerprint(), clients, connections }
    }

    /// An address nothing listens on.
    pub async fn dead_addr() -> String {
        let l = TcpListener::bind("127.0.0.1:0").await.unwrap();
        l.local_addr().unwrap().to_string()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    /// A Node that writes the protocol it agreed, then echoes.
    async fn echo_node() -> fake::FakeNode {
        fake::start(|mut tls| async move {
            let alpn = tls.get_ref().1.alpn_protocol().map(|p| String::from_utf8_lossy(p).into_owned()).unwrap_or_default();
            let _ = tls.write_all(format!("{alpn}\n").as_bytes()).await;
            let mut buf = [0u8; 64];
            while let Ok(n) = tls.read(&mut buf).await {
                if n == 0 || tls.write_all(&buf[..n]).await.is_err() {
                    break;
                }
                let _ = tls.flush().await;
            }
        })
        .await
    }

    async fn read_line(s: &mut NodeStream) -> String {
        let mut out = Vec::new();
        let mut b = [0u8; 1];
        while s.read_exact(&mut b).await.is_ok() && b[0] != b'\n' {
            out.push(b[0]);
        }
        String::from_utf8(out).unwrap()
    }

    #[tokio::test]
    async fn a_node_with_the_pinned_key_is_reached_as_this_device() {
        let node = echo_node().await;
        let me = fake::temp_identity("device");
        let dialer = Dialer::new(me.clone());
        let mut s = dialer.dial(&node.addr, &node.fingerprint, Proto::H2).await.expect("dial");
        assert_eq!(read_line(&mut s).await, "h2");
        s.write_all(b"hello\n").await.unwrap();
        assert_eq!(read_line(&mut s).await, "hello");
        assert_eq!(*node.clients.lock().unwrap(), vec![me.fingerprint()]);

        let mut s = dialer.dial(&node.addr, &node.fingerprint.to_uppercase(), Proto::Http1).await.expect("dial http/1.1");
        assert_eq!(read_line(&mut s).await, "http/1.1");
    }

    #[tokio::test]
    async fn a_node_with_another_key_is_refused() {
        let node = echo_node().await;
        let dialer = Dialer::new(fake::temp_identity("device"));
        let err = dialer.dial(&node.addr, &"00".repeat(32), Proto::H2).await.err().expect("refused");
        assert!(err.contains("trust list"), "{err}");
    }

    /// An anchor that answers the Relay's Upgrade by splicing the
    /// connection to `to` (whatever Node is there), counting requests.
    async fn anchor_to(to: String, asked: Arc<Mutex<Vec<String>>>) -> fake::FakeNode {
        fake::start(move |mut tls| {
            let (to, asked) = (to.clone(), asked.clone());
            async move {
                let mut head = Vec::new();
                let mut b = [0u8; 1];
                while !head.ends_with(b"\r\n\r\n") && tls.read_exact(&mut b).await.is_ok() {
                    head.push(b[0]);
                }
                asked.lock().unwrap().push(String::from_utf8_lossy(&head).into_owned());
                let Ok(mut up) = tokio::net::TcpStream::connect(&to).await else { return };
                let _ = tls.write_all(b"HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: maitre-relay\r\n\r\n").await;
                let _ = tokio::io::copy_bidirectional(&mut tls, &mut up).await;
            }
        })
        .await
    }

    #[tokio::test]
    async fn a_node_the_device_cannot_reach_is_reached_through_the_anchor() {
        let target = echo_node().await;
        let asked = Arc::new(Mutex::new(Vec::new()));
        let anchor = anchor_to(target.addr.clone(), asked.clone()).await;
        let me = fake::temp_identity("device");
        let dialer = Dialer::new(me.clone());
        dialer.relay_through(Some(Anchor { addr: anchor.addr.clone(), fingerprint: anchor.fingerprint.clone() }));
        let dead = fake::dead_addr().await;
        let mut s = dialer.dial(&dead, &target.fingerprint, Proto::H2).await.expect("relayed");
        assert_eq!(read_line(&mut s).await, "h2");
        s.write_all(b"hello\n").await.unwrap();
        assert_eq!(read_line(&mut s).await, "hello");
        // The anchor was asked over HTTP/1.1 for the target by its node id,
        // and the target saw this Device, not the anchor.
        let req = asked.lock().unwrap()[0].clone();
        assert!(req.starts_with(&format!("GET /maitre/relay-node?node={} HTTP/1.1\r\n", target.fingerprint)), "{req}");
        assert!(req.contains("Upgrade: maitre-relay\r\n"), "{req}");
        assert_eq!(*anchor.clients.lock().unwrap(), vec![me.fingerprint()]);
        assert_eq!(*target.clients.lock().unwrap(), vec![me.fingerprint()]);
    }

    #[tokio::test]
    async fn a_relay_that_hands_back_another_node_fails_the_handshake() {
        let target = echo_node().await;
        let impostor = echo_node().await;
        let anchor = anchor_to(impostor.addr.clone(), Arc::new(Mutex::new(Vec::new()))).await;
        let dialer = Dialer::new(fake::temp_identity("device"));
        dialer.relay_through(Some(Anchor { addr: anchor.addr.clone(), fingerprint: anchor.fingerprint.clone() }));
        let dead = fake::dead_addr().await;
        let err = dialer.dial(&dead, &target.fingerprint, Proto::H2).await.err().expect("refused");
        assert!(err.contains("trust list"), "{err}");
    }

    #[tokio::test]
    async fn an_anchor_that_refuses_the_relay_says_so() {
        let anchor = fake::start(|mut tls| async move {
            let mut b = [0u8; 512];
            let _ = tls.read(&mut b).await;
            let body = "404 page not found\n";
            let _ = tls.write_all(format!("HTTP/1.1 404 Not Found\r\nContent-Length: {}\r\n\r\n{body}", body.len()).as_bytes()).await;
            let _ = tls.shutdown().await;
        })
        .await;
        let dialer = Dialer::new(fake::temp_identity("device"));
        dialer.relay_through(Some(Anchor { addr: anchor.addr.clone(), fingerprint: anchor.fingerprint.clone() }));
        let dead = fake::dead_addr().await;
        let err = dialer.dial(&dead, &"00".repeat(32), Proto::H2).await.err().expect("refused");
        assert!(err.contains("the anchor refused the Relay: 404 Not Found: 404 page not found"), "{err}");
    }

    #[tokio::test]
    async fn without_an_anchor_or_to_the_anchor_itself_there_is_no_relay() {
        let anchor = fake::start(|_| async {}).await;
        let dialer = Dialer::new(fake::temp_identity("device"));
        let dead = fake::dead_addr().await;
        assert!(dialer.dial(&dead, &anchor.fingerprint, Proto::H2).await.is_err());
        dialer.relay_through(Some(Anchor { addr: anchor.addr.clone(), fingerprint: anchor.fingerprint.clone() }));
        assert!(dialer.dial(&dead, &anchor.fingerprint, Proto::H2).await.is_err());
        assert_eq!(anchor.connections.load(std::sync::atomic::Ordering::SeqCst), 0);
    }

    #[tokio::test]
    async fn an_address_that_does_not_answer_fails() {
        let dialer = Dialer::new(fake::temp_identity("device"));
        let addr = fake::dead_addr().await;
        let err = dialer.dial(&addr, &"00".repeat(32), Proto::H2).await.err().expect("no answer");
        assert!(!err.is_empty());
    }
}
