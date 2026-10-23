//! Mutual-TLS HTTP/2 client to a Node. The server certificate is accepted if
//! and only if its Ed25519 key fingerprint is one we trust; no CA, no chain.

use crate::identity::{fingerprint_of_raw_key, Identity};
use rustls::client::danger::{HandshakeSignatureValid, ServerCertVerified, ServerCertVerifier};
use rustls::pki_types::{CertificateDer, PrivateKeyDer, ServerName, UnixTime};
use rustls::{DigitallySignedStruct, SignatureScheme};
use std::collections::HashSet;
use std::sync::Arc;
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

/// Build a client that presents `id` and trusts only the given Node fingerprints.
pub fn build_client(id: &Identity, trusted: HashSet<String>) -> Result<reqwest::Client, String> {
    let provider = Arc::new(rustls::crypto::ring::default_provider());

    let certs: Vec<CertificateDer<'static>> = rustls_pemfile::certs(&mut id.cert_pem().as_bytes())
        .collect::<Result<_, _>>()
        .map_err(|e| format!("parse own certificate: {e}"))?;
    let key_pem = id.key_pem();
    let key: PrivateKeyDer<'static> = rustls_pemfile::private_key(&mut key_pem.as_bytes())
        .map_err(|e| format!("parse own key: {e}"))?
        .ok_or("own key PEM contained no key")?;

    let mut config = rustls::ClientConfig::builder_with_provider(provider.clone())
        .with_protocol_versions(&[&rustls::version::TLS13])
        .map_err(|e| e.to_string())?
        .dangerous()
        .with_custom_certificate_verifier(Arc::new(PinnedKeyVerifier { trusted, provider }))
        .with_client_auth_cert(certs, key)
        .map_err(|e| format!("client auth: {e}"))?;
    config.alpn_protocols = vec![b"h2".to_vec()];

    reqwest::Client::builder()
        .use_preconfigured_tls(config)
        .http2_prior_knowledge()
        .build()
        .map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    //! Cross-language check against a running `maitre dev`. Skipped unless
    //! MAITRE_TEST_NODE_ADDR and MAITRE_TEST_NODE_FP are set; `make m0-check`
    //! sets up both sides and runs it.
    use super::*;
    use std::collections::HashSet;

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
        let id = Identity::load_or_generate(std::path::Path::new(&id_dir)).unwrap();
        let (Some(addr), Some(fp)) = (env("MAITRE_TEST_NODE_ADDR"), env("MAITRE_TEST_NODE_FP")) else {
            eprintln!("skipped: node not configured");
            return;
        };
        let client = build_client(&id, HashSet::from([fp])).unwrap();
        let res = client
            .post(format!("https://{addr}/maitre.v1.NodeService/GetNodeInfo"))
            .header("content-type", "application/proto")
            .header("connect-protocol-version", "1")
            .body(Vec::<u8>::new())
            .send()
            .await
            .expect("mTLS call to Go dev node");
        assert_eq!(res.status(), 200, "body: {:?}", res.text().await);
    }

    #[tokio::test]
    async fn wrong_fingerprint_is_refused() {
        let (Some(addr), Some(id_dir)) = (env("MAITRE_TEST_NODE_ADDR"), env("MAITRE_TEST_DEVICE_DIR")) else {
            return;
        };
        let id = Identity::load_or_generate(std::path::Path::new(&id_dir)).unwrap();
        let client = build_client(&id, HashSet::from(["00".repeat(32)])).unwrap();
        let err = client
            .post(format!("https://{addr}/maitre.v1.NodeService/GetNodeInfo"))
            .body(Vec::<u8>::new())
            .send()
            .await
            .expect_err("connected to a node with the wrong fingerprint");
        let msg = format!("{err:?}");
        assert!(msg.contains("trust list"), "unexpected error: {msg}");
    }
}
