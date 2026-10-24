//! Recovery Phrase (design question 67): twelve BIP39 English words encoding
//! 128 bits of entropy. The entropy derives an Ed25519 "recovery key" whose
//! public half is stored in the Fleet as a recovery-only Trusted Device.
//! Must match server/internal/recovery exactly.

use rcgen::{KeyPair, PKCS_ED25519};
use sha2::{Digest, Sha256};

const WORDS: &str = include_str!("../bip39-english.txt");

fn words() -> Vec<&'static str> {
    WORDS.split_whitespace().collect()
}

/// Twelve words from 16 bytes of entropy.
pub fn from_entropy(entropy: &[u8; 16]) -> String {
    let list = words();
    let sum = Sha256::digest(entropy);
    let mut bits = [0u8; 17];
    bits[..16].copy_from_slice(entropy);
    bits[16] = sum[0];
    (0..12)
        .map(|i| {
            let mut idx = 0usize;
            for b in 0..11 {
                let pos = i * 11 + b;
                let bit = (bits[pos / 8] >> (7 - pos % 8)) & 1;
                idx = idx << 1 | bit as usize;
            }
            list[idx]
        })
        .collect::<Vec<_>>()
        .join(" ")
}

/// A fresh phrase from the OS random source.
pub fn generate() -> String {
    let mut entropy = [0u8; 16];
    rand::fill(&mut entropy);
    from_entropy(&entropy)
}

/// Parse a phrase in any case or spacing, checking the checksum.
pub fn parse(text: &str) -> Result<[u8; 16], String> {
    let list = words();
    let fields: Vec<String> = text.split_whitespace().map(|w| w.to_lowercase()).collect();
    if fields.len() != 12 {
        return Err(format!("a recovery phrase has 12 words, got {}", fields.len()));
    }
    let mut bits = [0u8; 17];
    for (i, w) in fields.iter().enumerate() {
        let idx = list
            .iter()
            .position(|x| x == w)
            .ok_or_else(|| format!("\"{w}\" is not a recovery word"))?;
        for b in 0..11 {
            let pos = i * 11 + b;
            if (idx >> (10 - b)) & 1 == 1 {
                bits[pos / 8] |= 1 << (7 - pos % 8);
            }
        }
    }
    let mut entropy = [0u8; 16];
    entropy.copy_from_slice(&bits[..16]);
    let sum = Sha256::digest(entropy);
    if sum[0] >> 4 != bits[16] >> 4 {
        return Err("a word is wrong; check the phrase".into());
    }
    Ok(entropy)
}

/// Derive the recovery key pair. Same domain separation as the Go side.
pub fn key_pair(entropy: &[u8; 16]) -> Result<KeyPair, String> {
    let mut h = Sha256::new();
    h.update(b"maitre-recovery-v1\0");
    h.update(entropy);
    let seed = h.finalize();
    // PKCS#8 v1 wrapper for an Ed25519 private key: fixed 16-byte prefix + seed.
    let mut der = vec![
        0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70, 0x04, 0x22, 0x04, 0x20,
    ];
    der.extend_from_slice(&seed);
    KeyPair::from_pkcs8_der_and_sign_algo(&der.into(), &PKCS_ED25519).map_err(|e| e.to_string())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bip39_vectors() {
        assert_eq!(
            from_entropy(&[0u8; 16]),
            "abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about"
        );
        assert_eq!(
            from_entropy(&[0x7f; 16]),
            "legal winner thank year wave sausage worth useful legal winner thank yellow"
        );
        assert_eq!(parse(" LEGAL winner thank year wave sausage worth useful legal winner thank yellow\n").unwrap(), [0x7f; 16]);
        assert!(parse("abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon").is_err());
    }

    #[test]
    fn round_trip_and_key() {
        let p = generate();
        let e = parse(&p).unwrap();
        assert_eq!(from_entropy(&e), p);
        let k1 = key_pair(&e).unwrap();
        let k2 = key_pair(&e).unwrap();
        assert_eq!(k1.public_key_raw(), k2.public_key_raw());
        assert_eq!(k1.public_key_raw().len(), 32);
    }
}
