//! End-to-end check of the Relay (ADR 0012) against two running Nodes of
//! one Fleet: the Desktop cannot reach the target itself, only the anchor,
//! and uses the target through the anchor's Relay. Ignored by default;
//! `make relay-check` runs it against two agents on loopback.
//!
//!   MAITRE_RELAY_ANCHOR=<addr> MAITRE_RELAY_ANCHOR_FP=<fingerprint>
//!   MAITRE_RELAY_NODE=<an address of the target the Desktop cannot reach>
//!   MAITRE_RELAY_NODE_FP=<fingerprint> MAITRE_RELAY_NODE_NAME=<its name>
//!   MAITRE_RELAY_IDENTITY=/path/to/device cargo test --test relay -- --ignored
use maitre_desktop_lib::dial::{Anchor, Dialer};
use maitre_desktop_lib::identity::Identity;
use maitre_desktop_lib::proxy::Proxies;
use maitre_desktop_lib::rpc::NodeClient;
use std::path::Path;
use std::sync::Arc;
use tokio::io::{AsyncReadExt, AsyncWriteExt};
use tokio::net::TcpStream;

fn env(name: &str) -> String {
    std::env::var(name).unwrap_or_else(|_| panic!("{name}"))
}

/// A protobuf message of one string field, number 1.
fn string_field(s: &str) -> Vec<u8> {
    let mut out = vec![0x0a, s.len() as u8];
    out.extend_from_slice(s.as_bytes());
    out
}

#[tokio::test]
#[ignore]
async fn a_node_reached_only_through_the_anchor_is_fully_usable() {
    let (node, fp, name) = (env("MAITRE_RELAY_NODE"), env("MAITRE_RELAY_NODE_FP"), env("MAITRE_RELAY_NODE_NAME"));
    let id = Arc::new(Identity::load_or_generate(Path::new(&env("MAITRE_RELAY_IDENTITY"))).unwrap());
    let dialer = Arc::new(Dialer::new(id));
    let nodes = NodeClient::new(dialer.clone());

    // Without the Relay the target is not reached at all.
    let err = nodes.call(&node, &fp, "/maitre.v1.NodeService/GetNodeInfo", vec![]).await.err().expect("reached directly");
    assert!(err.starts_with(&format!("{node} could not be reached")), "{err}");

    dialer.relay_through(Some(Anchor { addr: env("MAITRE_RELAY_ANCHOR"), fingerprint: env("MAITRE_RELAY_ANCHOR_FP") }));

    // It answers as itself.
    let res = nodes.call(&node, &fp, "/maitre.v1.NodeService/GetNodeInfo", vec![]).await.expect("GetNodeInfo through the Relay");
    assert_eq!(res.status, 200, "{}", String::from_utf8_lossy(&res.body));
    assert!(res.body.windows(name.len()).any(|w| w == name.as_bytes()), "not {name}: {:?}", String::from_utf8_lossy(&res.body));

    // An action: renaming the Node.
    let renamed = format!("{name}-relayed");
    let res = nodes.call(&node, &fp, "/maitre.v1.NodeService/RenameNode", string_field(&renamed)).await.expect("RenameNode");
    assert_eq!(res.status, 200, "{}", String::from_utf8_lossy(&res.body));
    assert!(res.body.windows(renamed.len()).any(|w| w == renamed.as_bytes()), "{:?}", String::from_utf8_lossy(&res.body));

    // Its logs: the agent's journal. A machine without journalctl has
    // none to give, and the Node says so itself.
    let res = nodes.call(&node, &fp, "/maitre.v1.NodeService/GetNodeLogs", vec![]).await.expect("GetNodeLogs");
    let text = String::from_utf8_lossy(&res.body);
    if std::env::var("MAITRE_RELAY_NO_JOURNAL").is_ok() {
        assert!(res.status == 200 || text.contains("journalctl"), "{} {text}", res.status);
    } else {
        assert_eq!(res.status, 200, "{text}");
    }

    // The loopback proxy for file contents reaches the target's handler
    // over HTTP/1.1 through the Relay.
    let base = Proxies::default().open_files(dialer.clone(), node.clone(), fp.clone()).await.expect("open_files");
    let space = std::env::var("MAITRE_RELAY_SPACE").unwrap_or_else(|_| "nospace".into());
    let path = std::env::var("MAITRE_RELAY_FILE").unwrap_or_else(|_| "hello.txt".into());
    let url = format!("{base}/maitre/files/content?space={space}&path={path}");
    let rest = url.strip_prefix("http://").unwrap();
    let (host, target) = rest.split_once('/').unwrap();
    let mut s = TcpStream::connect(host).await.unwrap();
    s.write_all(format!("GET /{target} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n\r\n").as_bytes()).await.unwrap();
    let mut out = Vec::new();
    s.read_to_end(&mut out).await.unwrap();
    let out = String::from_utf8_lossy(&out);
    match std::env::var("MAITRE_RELAY_FILE_CONTENTS") {
        Ok(want) => assert!(out.starts_with("HTTP/1.1 200") && out.ends_with(&want), "{out}"),
        // No Pool here: the answer is the target's own, naming the Space.
        Err(_) => assert!(out.contains(&format!("Space {space} has no Pool on this Node")), "{out}"),
    }
}

#[tokio::test]
#[ignore]
async fn the_anchor_refuses_a_node_that_is_not_of_the_fleet() {
    let id = Arc::new(Identity::load_or_generate(Path::new(&env("MAITRE_RELAY_IDENTITY"))).unwrap());
    let dialer = Arc::new(Dialer::new(id));
    dialer.relay_through(Some(Anchor { addr: env("MAITRE_RELAY_ANCHOR"), fingerprint: env("MAITRE_RELAY_ANCHOR_FP") }));
    let nodes = NodeClient::new(dialer);
    let node = env("MAITRE_RELAY_NODE");
    let err = nodes.call(&node, &"ab".repeat(32), "/maitre.v1.NodeService/GetNodeInfo", vec![]).await.err().expect("relayed");
    assert!(err.contains("the anchor refused the Relay: 404 Not Found: no Node abababab in this Fleet"), "{err}");
}
