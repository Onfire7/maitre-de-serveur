//! End-to-end check of the Service tunnel (decision 71) against a running
//! Node. Ignored by default; `make tunnel-check` runs it against
//! `maitre dev --fake-service echo`.
//!
//!   MAITRE_TUNNEL_NODE=127.0.0.1:7443 MAITRE_TUNNEL_FP=<node fingerprint>
//!   MAITRE_TUNNEL_IDENTITY=/path/to/device cargo test --test tunnel -- --ignored
use maitre_desktop_lib::identity::Identity;
use maitre_desktop_lib::proxy::Proxies;
use std::path::Path;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

async fn read_headers(s: &mut TcpStream) -> String {
    let mut buf = Vec::new();
    let mut b = [0u8; 1];
    while !buf.ends_with(b"\r\n\r\n") {
        s.read_exact(&mut b).await.expect("headers");
        buf.push(b[0]);
    }
    String::from_utf8_lossy(&buf).into_owned()
}

#[tokio::test]
#[ignore]
async fn tunnel_http_and_websocket() {
    let node = std::env::var("MAITRE_TUNNEL_NODE").expect("MAITRE_TUNNEL_NODE");
    let fp = std::env::var("MAITRE_TUNNEL_FP").expect("MAITRE_TUNNEL_FP");
    let dir = std::env::var("MAITRE_TUNNEL_IDENTITY").expect("MAITRE_TUNNEL_IDENTITY");
    let id = Arc::new(Identity::load_or_generate(Path::new(&dir)).unwrap());
    let proxies = Proxies::default();
    let url = proxies.open(id, node, fp, "echo".into()).await.expect("open proxy");
    let port: u16 = url.trim_start_matches("http://127.0.0.1:").trim_end_matches('/').parse().unwrap();

    // Plain request: path and query must arrive untouched.
    let mut s = TcpStream::connect(("127.0.0.1", port)).await.unwrap();
    s.write_all(b"GET /hello/there?x=1 HTTP/1.1\r\nHost: whatever\r\nConnection: close\r\n\r\n").await.unwrap();
    let mut out = Vec::new();
    s.read_to_end(&mut out).await.unwrap();
    let out = String::from_utf8_lossy(&out);
    assert!(out.starts_with("HTTP/1.1 200"), "status: {out}");
    assert!(out.contains("echo GET /hello/there?x=1 host=echo.local"), "body: {out}");

    // WebSocket: upgrade, send one masked text frame, expect it echoed.
    let mut s = TcpStream::connect(("127.0.0.1", port)).await.unwrap();
    s.write_all(
        b"GET /ws HTTP/1.1\r\nHost: whatever\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n",
    )
    .await
    .unwrap();
    let hdrs = read_headers(&mut s).await;
    assert!(hdrs.starts_with("HTTP/1.1 101"), "upgrade: {hdrs}");
    let payload = b"ping over the tunnel";
    let mask = [0x12u8, 0x34, 0x56, 0x78];
    let mut frame = vec![0x81, 0x80 | payload.len() as u8];
    frame.extend_from_slice(&mask);
    frame.extend(payload.iter().enumerate().map(|(i, b)| b ^ mask[i % 4]));
    s.write_all(&frame).await.unwrap();
    let mut head = [0u8; 2];
    s.read_exact(&mut head).await.unwrap();
    assert_eq!(head[0], 0x81, "text frame");
    let len = (head[1] & 0x7f) as usize;
    let mut body = vec![0u8; len];
    s.read_exact(&mut body).await.unwrap();
    assert_eq!(&body, payload);
}
