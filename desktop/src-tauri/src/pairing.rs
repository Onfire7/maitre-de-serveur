//! Pairing code (design question 66). Must match server/internal/pairing.

use sha2::{Digest, Sha256};

/// Six digits, "ddd ddd", from the Installer's and this device's raw keys.
pub fn code(installer: &[u8], device: &[u8]) -> String {
    let mut h = Sha256::new();
    h.update(b"maitre-pairing-v1\0");
    h.update(installer);
    h.update(device);
    let sum = h.finalize();
    let n = u32::from_be_bytes([sum[0], sum[1], sum[2], sum[3]]) % 1_000_000;
    format!("{:03} {:03}", n / 1000, n % 1000)
}

#[cfg(test)]
mod tests {
    #[test]
    fn matches_go_vector() {
        let installer = [0u8; 32];
        let device: Vec<u8> = (0..32u8).collect();
        assert_eq!(super::code(&installer, &device), "036 565");
    }
}

/// Six digits from one device key alone (decision 99): the device asking to
/// join shows it, the approving Console shows the same for that device, and
/// the person compares them.
pub fn device_code(key: &[u8]) -> String {
    let mut h = Sha256::new();
    h.update(b"maitre-device-code-v1\0");
    h.update(key);
    let sum = h.finalize();
    let n = u32::from_be_bytes([sum[0], sum[1], sum[2], sum[3]]) % 1_000_000;
    format!("{:03} {:03}", n / 1000, n % 1000)
}
