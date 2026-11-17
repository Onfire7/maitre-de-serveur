//! End-to-end check of the Service tunnel (decision 71) against a running
//! Node. Ignored by default; `make tunnel-check` runs it against
//! `maitre dev --fake-service echo`.
//!
//!   MAITRE_TUNNEL_NODE=127.0.0.1:7443 MAITRE_TUNNEL_FP=<node fingerprint>
//!   MAITRE_TUNNEL_IDENTITY=/path/to/device cargo test --test tunnel -- --ignored
//!
//! With MAITRE_TUNNEL_ANCHOR=<addr> and MAITRE_TUNNEL_ANCHOR_FP=<fingerprint>
//! the Desktop also has that anchor's Relay (ADR 0012); give
//! MAITRE_TUNNEL_NODE an address that does not answer to check the tunnel
//! through the Relay.
use maitre_desktop_lib::dial::{Anchor, Dialer};
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
    let dialer = Arc::new(Dialer::new(id));
    if let (Ok(addr), Ok(fingerprint)) = (std::env::var("MAITRE_TUNNEL_ANCHOR"), std::env::var("MAITRE_TUNNEL_ANCHOR_FP")) {
        dialer.relay_through(Some(Anchor { addr, fingerprint }));
    }
    let proxies = Proxies::default();
    let (_, port) = proxies
        .open(dialer, node, fp, "echo".into(), "echo".into(), String::new(), None)
        .await
        .expect("open proxy");

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

// --- Terminals (ADR 0011) ---------------------------------------------
// The Console speaks TerminalFrame (api/maitre/v1/terminal.proto) over a
// WebSocket; here the few frames the test needs are written and read by
// hand, so the check needs no Protobuf code of its own.

fn varint(mut v: u64, out: &mut Vec<u8>) {
    while v >= 0x80 {
        out.push((v as u8) | 0x80);
        v >>= 7;
    }
    out.push(v as u8);
}

fn field(n: u64, body: &[u8], out: &mut Vec<u8>) {
    varint(n << 3 | 2, out);
    varint(body.len() as u64, out);
    out.extend_from_slice(body);
}

fn size(cols: u64, rows: u64) -> Vec<u8> {
    let mut s = vec![];
    varint(1 << 3, &mut s);
    varint(cols, &mut s);
    varint(2 << 3, &mut s);
    varint(rows, &mut s);
    s
}

fn start_frame(service: &str, cols: u64, rows: u64) -> Vec<u8> {
    let mut st = vec![];
    field(1, service.as_bytes(), &mut st);
    field(2, &size(cols, rows), &mut st);
    let mut f = vec![];
    field(1, &st, &mut f);
    f
}

fn input_frame(text: &str) -> Vec<u8> {
    let mut i = vec![];
    field(1, text.as_bytes(), &mut i);
    let mut f = vec![];
    field(2, &i, &mut f);
    f
}

fn resize_frame(cols: u64, rows: u64) -> Vec<u8> {
    let mut r = vec![];
    field(1, &size(cols, rows), &mut r);
    let mut f = vec![];
    field(3, &r, &mut f);
    f
}

fn read_varint(b: &[u8], i: &mut usize) -> u64 {
    let (mut v, mut shift) = (0u64, 0);
    loop {
        let x = b[*i];
        *i += 1;
        v |= ((x & 0x7f) as u64) << shift;
        if x < 0x80 {
            return v;
        }
        shift += 7;
    }
}

/// The fields of a message: (number, varint value or bytes).
fn fields(b: &[u8]) -> Vec<(u64, u64, Vec<u8>)> {
    let mut i = 0;
    let mut out = vec![];
    while i < b.len() {
        let key = read_varint(b, &mut i);
        match key & 7 {
            0 => out.push((key >> 3, read_varint(b, &mut i), vec![])),
            2 => {
                let n = read_varint(b, &mut i) as usize;
                out.push((key >> 3, 0, b[i..i + n].to_vec()));
                i += n;
            }
            t => panic!("wire type {t}"),
        }
    }
    out
}

async fn ws_send(s: &mut TcpStream, payload: &[u8]) {
    let mask = rand::random::<[u8; 4]>();
    let mut frame = vec![0x82];
    match payload.len() {
        n if n < 126 => frame.push(0x80 | n as u8),
        n if n < 65536 => {
            frame.push(0x80 | 126);
            frame.extend_from_slice(&(n as u16).to_be_bytes());
        }
        n => {
            frame.push(0x80 | 127);
            frame.extend_from_slice(&(n as u64).to_be_bytes());
        }
    }
    frame.extend_from_slice(&mask);
    frame.extend(payload.iter().enumerate().map(|(i, b)| b ^ mask[i % 4]));
    s.write_all(&frame).await.unwrap();
}

/// The next data message's opcode and payload (the Node's are unmasked).
async fn ws_read(s: &mut TcpStream) -> (u8, Vec<u8>) {
    let mut head = [0u8; 2];
    s.read_exact(&mut head).await.expect("a WebSocket frame");
    let mut len = (head[1] & 0x7f) as u64;
    if len == 126 {
        let mut b = [0u8; 2];
        s.read_exact(&mut b).await.unwrap();
        len = u16::from_be_bytes(b) as u64;
    } else if len == 127 {
        let mut b = [0u8; 8];
        s.read_exact(&mut b).await.unwrap();
        len = u64::from_be_bytes(b);
    }
    let mut body = vec![0u8; len as usize];
    s.read_exact(&mut body).await.unwrap();
    (head[0] & 0x0f, body)
}

/// One Terminal frame from the Node: its field number and body.
async fn next_frame(s: &mut TcpStream) -> (u64, Vec<u8>) {
    loop {
        let (op, body) = tokio::time::timeout(std::time::Duration::from_secs(15), ws_read(s)).await.expect("the Node went quiet");
        match op {
            2 => {
                let f = fields(&body);
                return (f[0].0, f[0].2.clone());
            }
            8 => panic!("the Node closed the Terminal: {:?}", String::from_utf8_lossy(&body)),
            _ => continue,
        }
    }
}

async fn output_until(s: &mut TcpStream, seen: &mut String, want: &str) {
    while !seen.contains(want) {
        let (n, body) = next_frame(s).await;
        assert_eq!(n, 5, "waiting for {want:?}, got frame {n} {:?} (output {seen:?})", fields(&body));
        seen.push_str(&String::from_utf8_lossy(&fields(&body)[0].2));
    }
}

async fn upgrade(port: u16, path: &str) -> (TcpStream, String) {
    let mut s = TcpStream::connect(("127.0.0.1", port)).await.unwrap();
    s.write_all(format!("GET {path} HTTP/1.1\r\nHost: 127.0.0.1\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\nOrigin: tauri://localhost\r\n\r\n").as_bytes()).await.unwrap();
    let h = read_headers(&mut s).await;
    (s, h)
}

/// With MAITRE_TUNNEL_TERMINAL set, the dev Node's fake Service has a
/// container (scripts/fake-podman) with a real shell: open a Terminal
/// through the Desktop's Terminal proxy, type, resize, exit; then the token
/// it used is spent and a connection without one is refused.
#[tokio::test]
#[ignore]
async fn terminal_through_the_tunnel() {
    if std::env::var("MAITRE_TUNNEL_TERMINAL").is_err() {
        return;
    }
    let node = std::env::var("MAITRE_TUNNEL_NODE").expect("MAITRE_TUNNEL_NODE");
    let fp = std::env::var("MAITRE_TUNNEL_FP").expect("MAITRE_TUNNEL_FP");
    let dir = std::env::var("MAITRE_TUNNEL_IDENTITY").expect("MAITRE_TUNNEL_IDENTITY");
    let id = Arc::new(Identity::load_or_generate(Path::new(&dir)).unwrap());
    let dialer = Arc::new(Dialer::new(id));
    if let (Ok(addr), Ok(fingerprint)) = (std::env::var("MAITRE_TUNNEL_ANCHOR"), std::env::var("MAITRE_TUNNEL_ANCHOR_FP")) {
        dialer.relay_through(Some(Anchor { addr, fingerprint }));
    }
    let proxies = Proxies::default();
    let url = proxies.open_terminal(dialer, node, fp, "echo".into()).await.expect("terminal url");
    let rest = url.strip_prefix("ws://127.0.0.1:").expect(&url);
    let (port, token) = rest.split_once('/').unwrap();
    let port: u16 = port.parse().unwrap();
    let path = format!("/{token}");

    let (_, refused) = upgrade(port, "/").await;
    assert!(refused.starts_with("HTTP/1.1 403"), "no token: {refused}");

    let (mut s, h) = upgrade(port, &path).await;
    assert!(h.starts_with("HTTP/1.1 101"), "upgrade: {h}");
    ws_send(&mut s, &start_frame("echo", 80, 24)).await;
    let (n, started) = next_frame(&mut s).await;
    assert_eq!(n, 4, "started first: {:?}", fields(&started));
    let started = fields(&started);
    let get = |k: u64| started.iter().find(|f| f.0 == k).map(|f| String::from_utf8_lossy(&f.2).into_owned()).unwrap_or_default();
    assert_eq!((get(2).as_str(), get(3).as_str(), get(4).as_str()), ("echo", "root", "/bin/bash"));
    let mut seen = String::new();
    ws_send(&mut s, &input_frame("echo hi-$((40+2))\n")).await;
    output_until(&mut s, &mut seen, "hi-42").await;
    ws_send(&mut s, &input_frame("stty size\n")).await;
    output_until(&mut s, &mut seen, "24 80").await;
    ws_send(&mut s, &resize_frame(117, 33)).await;
    ws_send(&mut s, &input_frame("stty size\n")).await;
    output_until(&mut s, &mut seen, "33 117").await;
    ws_send(&mut s, &input_frame("exit 3\n")).await;
    let exit = loop {
        let (n, body) = next_frame(&mut s).await;
        if n == 6 {
            break fields(&body);
        }
        assert_eq!(n, 5);
    };
    let reason = exit.iter().find(|f| f.0 == 1).map(|f| f.1).unwrap_or(0);
    let code = exit.iter().find(|f| f.0 == 2).map(|f| f.1).unwrap_or(0);
    assert_eq!((reason, code), (1, 3), "exit {exit:?}");

    let (_, again) = upgrade(port, &path).await;
    assert!(again.starts_with("HTTP/1.1 403"), "a token used twice: {again}");
}
