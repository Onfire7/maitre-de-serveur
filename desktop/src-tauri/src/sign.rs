//! Record signatures (decision 189). A Trusted Device signs the content of
//! every record it writes, so a Node can tell a change a person made from
//! one a peer made up. Must match server/internal/recsig exactly.

use ring::signature::{Ed25519KeyPair, KeyPair};

const DOMAIN: &[u8] = b"maitre record v1\0";

/// The bytes a record signature covers: the domain string, then kind, id
/// and body each as a 4-byte big-endian length and the bytes, then deleted
/// as one byte, then the signing time as 8-byte big-endian milliseconds.
/// A deleted record's body is empty, whatever was passed.
pub fn canonical(kind: &str, id: &str, body: &[u8], deleted: bool, signed_unix_ms: i64) -> Vec<u8> {
    let body: &[u8] = if deleted { &[] } else { body };
    let mut out = Vec::with_capacity(DOMAIN.len() + 12 + kind.len() + id.len() + body.len() + 9);
    out.extend_from_slice(DOMAIN);
    for part in [kind.as_bytes(), id.as_bytes(), body] {
        out.extend_from_slice(&(part.len() as u32).to_be_bytes());
        out.extend_from_slice(part);
    }
    out.push(deleted as u8);
    out.extend_from_slice(&signed_unix_ms.to_be_bytes());
    out
}

/// The signer's raw 32-byte public key and the signature.
pub struct Signature {
    pub signer_key: Vec<u8>,
    pub signature: Vec<u8>,
}

pub fn sign(key: &Ed25519KeyPair, kind: &str, id: &str, body: &[u8], deleted: bool, signed_unix_ms: i64) -> Signature {
    let sig = key.sign(&canonical(kind, id, body, deleted, signed_unix_ms));
    Signature { signer_key: key.public_key().as_ref().to_vec(), signature: sig.as_ref().to_vec() }
}

/// A ring signing key from an rcgen key pair's PKCS#8 DER.
pub fn key_of_pkcs8(der: &[u8]) -> Result<Ed25519KeyPair, String> {
    Ed25519KeyPair::from_pkcs8_maybe_unchecked(der).map_err(|e| format!("signing key: {e}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    const MS: i64 = 1761600000123;

    fn test_key() -> Ed25519KeyPair {
        let seed: Vec<u8> = (0u8..32).collect();
        Ed25519KeyPair::from_seed_unchecked(&seed).unwrap()
    }

    #[test]
    fn public_key_of_test_seed() {
        assert_eq!(hex::encode(test_key().public_key().as_ref()), "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8");
    }

    #[test]
    fn vector_space() {
        let body = hex::decode("0a056d65646961").unwrap();
        assert_eq!(hex::encode(canonical("space", "media", &body, false, MS)), "6d6169747265207265636f726420763100000000057370616365000000056d65646961000000070a056d65646961000000019a278ad07b");
        let s = sign(&test_key(), "space", "media", &body, false, MS);
        assert_eq!(hex::encode(&s.signature), "86387e9eb59456809df9926fc263989223ccc3d15e056c3435210f85dec09dbaa2c984342d86f7ddf6db518b313ac12c7821122554525dbb503069383195470a");
        assert_eq!(hex::encode(&s.signer_key), "03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8");
    }

    #[test]
    fn vector_tombstone_ignores_body() {
        let want = "6d6169747265207265636f726420763100000000056772616e7400000002673100000000010000019a278ad07b";
        assert_eq!(hex::encode(canonical("grant", "g1", &[], true, MS)), want);
        assert_eq!(hex::encode(canonical("grant", "g1", b"ignored", true, MS)), want);
        let s = sign(&test_key(), "grant", "g1", b"ignored", true, MS);
        assert_eq!(hex::encode(&s.signature), "394fa2ee58a263c193f7c57a6243b893ecfdda618d15b83dc4430c797f99cf9f3cf61eb4a07a6b592d1a3b735f07e40c78b494b9b82a05bde5eb718aab150601");
    }

    #[test]
    fn rcgen_keys_sign() {
        // The device identity and the Recovery Phrase key are rcgen key
        // pairs; both must load into ring with the same public key.
        let kp = rcgen::KeyPair::generate_for(&rcgen::PKCS_ED25519).unwrap();
        let k = key_of_pkcs8(&kp.serialize_der()).unwrap();
        assert_eq!(k.public_key().as_ref(), kp.public_key_raw());
        let rec = crate::recovery::key_pair(&[7u8; 16]).unwrap();
        let k = key_of_pkcs8(&rec.serialize_der()).unwrap();
        assert_eq!(k.public_key().as_ref(), rec.public_key_raw());
    }
}
