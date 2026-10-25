//! The Trusted Device identity: an Ed25519 key pair stored as PEM in the
//! same layout the Go agent uses (`identity.key`, `identity.pub`), so a
//! developer can copy `identity.pub` straight into a dev Node's trust dir.

use rcgen::{CertificateParams, KeyPair, PKCS_ED25519};
use sha2::{Digest, Sha256};
use std::fs;
use std::path::{Path, PathBuf};

pub struct Identity {
    key_pair: KeyPair,
    /// Self-signed certificate, PEM. Regenerated on every load; peers check
    /// the key, never the certificate, so this is fine.
    cert_pem: String,
    pub public_key_path: PathBuf,
}

impl Identity {
    pub fn load_or_generate(dir: &Path) -> Result<Self, String> {
        fs::create_dir_all(dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
        let key_path = dir.join("identity.key");
        let pub_path = dir.join("identity.pub");

        let key_pair = match fs::read_to_string(&key_path) {
            Ok(pem) => KeyPair::from_pem(&pem).map_err(|e| format!("parse identity.key: {e}"))?,
            Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
                let kp = KeyPair::generate_for(&PKCS_ED25519).map_err(|e| e.to_string())?;
                write_private(&key_path, kp.serialize_pem().as_bytes())?;
                fs::write(&pub_path, kp.public_key_pem()).map_err(|e| e.to_string())?;
                kp
            }
            Err(e) => return Err(format!("read identity.key: {e}")),
        };

        let mut params = CertificateParams::new(vec!["maitre".to_string()])
            .map_err(|e| e.to_string())?;
        params.not_before = rcgen::date_time_ymd(2020, 1, 1);
        params.not_after = rcgen::date_time_ymd(2120, 1, 1);
        let cert = params.self_signed(&key_pair).map_err(|e| e.to_string())?;

        Ok(Self {
            cert_pem: cert.pem(),
            key_pair,
            public_key_path: pub_path,
        })
    }

    /// Lowercase hex SHA-256 of the raw 32-byte public key; matches Go.
    pub fn fingerprint(&self) -> String {
        fingerprint_of_raw_key(self.key_pair.public_key_raw())
    }

    pub fn public_key_raw(&self) -> &[u8] {
        self.key_pair.public_key_raw()
    }

    pub fn public_key_pem(&self) -> String {
        self.key_pair.public_key_pem()
    }

    pub fn cert_pem(&self) -> &str {
        &self.cert_pem
    }

    pub fn key_pem(&self) -> String {
        self.key_pair.serialize_pem()
    }
}

pub fn fingerprint_of_raw_key(raw: &[u8]) -> String {
    hex::encode(Sha256::digest(raw))
}

/// PEM (SubjectPublicKeyInfo) for a raw Ed25519 public key: the form Node
/// and Trusted Device records carry.
pub fn public_key_pem_of_raw(raw: &[u8]) -> String {
    use base64::Engine;
    // SPKI prefix for Ed25519 (RFC 8410): SEQUENCE { SEQUENCE { OID 1.3.101.112 }, BIT STRING }
    const PREFIX: [u8; 12] = [0x30, 0x2a, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x03, 0x21, 0x00];
    let mut der = PREFIX.to_vec();
    der.extend_from_slice(raw);
    let b64 = base64::engine::general_purpose::STANDARD.encode(der);
    let mut out = String::from("-----BEGIN PUBLIC KEY-----\n");
    for chunk in b64.as_bytes().chunks(64) {
        out.push_str(std::str::from_utf8(chunk).unwrap());
        out.push('\n');
    }
    out.push_str("-----END PUBLIC KEY-----\n");
    out
}

#[cfg(unix)]
fn write_private(path: &Path, data: &[u8]) -> Result<(), String> {
    use std::os::unix::fs::OpenOptionsExt;
    use std::io::Write;
    let mut f = fs::OpenOptions::new()
        .write(true)
        .create_new(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| format!("write {}: {e}", path.display()))?;
    f.write_all(data).map_err(|e| e.to_string())
}

#[cfg(not(unix))]
fn write_private(path: &Path, data: &[u8]) -> Result<(), String> {
    fs::write(path, data).map_err(|e| format!("write {}: {e}", path.display()))
}
