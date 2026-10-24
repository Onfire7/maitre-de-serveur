mod discovery;
mod identity;
mod pairing;
mod recovery;
mod rpc;

use discovery::{Discovery, Found};
use identity::Identity;
use serde::{Deserialize, Serialize};
use std::collections::{HashMap, HashSet};
use std::fs;
use std::path::PathBuf;
use std::sync::Mutex;
use tauri::{Manager, State};

/// The Fleet this Console administers. Created on first run (Milestone 1);
/// joining an existing Fleet from a second device arrives in Milestone 3.
#[derive(Serialize, Deserialize, Clone)]
pub struct FleetState {
    pub fleet_id: String,
    pub name: String,
    pub created_unix_ms: i64,
    /// Public half of the Recovery Phrase key, PEM. Sent to every Node as a
    /// recovery-only Trusted Device. The phrase itself is never stored.
    pub recovery_public_key_pem: String,
}

/// A Node this Console knows how to reach.
#[derive(Serialize, Deserialize, Clone)]
pub struct KnownNode {
    pub node_id: String,
    pub name: String,
    /// host:port candidates; mDNS keeps them fresh.
    pub addresses: Vec<String>,
}

#[derive(Serialize, Deserialize, Clone, Default)]
pub struct ConsoleState {
    pub fleet: Option<FleetState>,
    #[serde(default)]
    pub nodes: Vec<KnownNode>,
}

#[derive(Serialize)]
pub struct DeviceInfo {
    pub fingerprint: String,
    pub public_key_pem: String,
    pub public_key_path: String,
    pub name: String,
}

#[derive(Serialize)]
pub struct RpcResponse {
    pub status: u16,
    pub content_type: String,
    pub body: Vec<u8>,
}

#[derive(Serialize)]
pub struct Peek {
    pub fingerprint: String,
    pub pairing_code: String,
}

struct AppState {
    data_dir: PathBuf,
    identity: Identity,
    state: Mutex<ConsoleState>,
    clients: Mutex<HashMap<String, reqwest::Client>>,
    discovery: Discovery,
}

impl AppState {
    fn state_path(&self) -> PathBuf {
        self.data_dir.join("console.json")
    }

    fn save(&self, state: ConsoleState) -> Result<(), String> {
        fs::write(self.state_path(), serde_json::to_vec_pretty(&state).map_err(|e| e.to_string())?)
            .map_err(|e| e.to_string())?;
        *self.state.lock().unwrap() = state;
        Ok(())
    }

    /// One client per pinned server fingerprint.
    fn client_for(&self, fingerprint: &str) -> Result<reqwest::Client, String> {
        let fp = fingerprint.trim().to_lowercase();
        if let Some(c) = self.clients.lock().unwrap().get(&fp) {
            return Ok(c.clone());
        }
        let client = rpc::build_client(&self.identity, HashSet::from([fp.clone()]))?;
        self.clients.lock().unwrap().insert(fp, client.clone());
        Ok(client)
    }
}

#[tauri::command]
fn device_info(state: State<'_, AppState>) -> DeviceInfo {
    DeviceInfo {
        fingerprint: state.identity.fingerprint(),
        public_key_pem: state.identity.public_key_pem(),
        public_key_path: state.identity.public_key_path.display().to_string(),
        name: device_name(),
    }
}

fn device_name() -> String {
    std::env::var("HOSTNAME")
        .ok()
        .or_else(|| fs::read_to_string("/etc/hostname").ok())
        .map(|s| s.trim().to_string())
        .filter(|s| !s.is_empty())
        .unwrap_or_else(|| "This computer".into())
}

#[tauri::command]
fn get_state(state: State<'_, AppState>) -> ConsoleState {
    state.state.lock().unwrap().clone()
}

#[tauri::command]
fn set_state(state: State<'_, AppState>, new_state: ConsoleState) -> Result<(), String> {
    state.save(new_state)
}

/// Create the Fleet. Returns the Recovery Phrase, shown exactly once.
#[tauri::command]
fn create_fleet(state: State<'_, AppState>, name: String) -> Result<String, String> {
    if state.state.lock().unwrap().fleet.is_some() {
        return Err("this Console already has a Fleet".into());
    }
    let phrase = recovery::generate();
    let entropy = recovery::parse(&phrase)?;
    let key = recovery::key_pair(&entropy)?;
    let mut raw = [0u8; 16];
    rand::fill(&mut raw);
    let mut st = state.state.lock().unwrap().clone();
    st.fleet = Some(FleetState {
        fleet_id: hex::encode(raw),
        name: name.trim().to_string(),
        created_unix_ms: std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0),
        recovery_public_key_pem: key.public_key_pem(),
    });
    state.save(st)?;
    Ok(phrase)
}

#[tauri::command]
fn discover(state: State<'_, AppState>) -> Vec<Found> {
    state.discovery.snapshot()
}

/// First contact with an Installer: learn its key and compute the pairing
/// code we expect it to show. The UI then pins `fingerprint` for every
/// further call and checks the Installer reports the same code.
#[tauri::command]
async fn peek_installer(state: State<'_, AppState>, addr: String) -> Result<Peek, String> {
    let key = rpc::peek_server_key(&state.identity, &addr).await?;
    Ok(Peek {
        fingerprint: identity::fingerprint_of_raw_key(&key),
        pairing_code: pairing::code(&key, state.identity.public_key_raw()),
    })
}

/// Send one Connect-protocol unary request to `addr`, trusting only `fingerprint`.
/// `procedure` is like "/maitre.v1.NodeService/GetNodeInfo".
#[tauri::command]
async fn rpc(
    state: State<'_, AppState>,
    addr: String,
    fingerprint: String,
    procedure: String,
    body: Vec<u8>,
) -> Result<RpcResponse, String> {
    if addr.is_empty() || fingerprint.is_empty() {
        return Err("rpc needs an address and a fingerprint".into());
    }
    let client = state.client_for(&fingerprint)?;
    let res = client
        .post(format!("https://{addr}{procedure}"))
        .header("content-type", "application/proto")
        .header("connect-protocol-version", "1")
        .timeout(std::time::Duration::from_secs(15))
        .body(body)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    let status = res.status().as_u16();
    let content_type = res
        .headers()
        .get("content-type")
        .and_then(|v| v.to_str().ok())
        .unwrap_or("")
        .to_string();
    let body = res.bytes().await.map_err(|e| e.to_string())?.to_vec();
    Ok(RpcResponse { status, content_type, body })
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .setup(|app| {
            let data_dir = app.path().app_data_dir()?;
            fs::create_dir_all(&data_dir)?;
            let identity = Identity::load_or_generate(&data_dir.join("device"))
                .map_err(std::io::Error::other)?;
            let state = fs::read(data_dir.join("console.json"))
                .ok()
                .and_then(|b| serde_json::from_slice(&b).ok())
                .unwrap_or_default();
            app.manage(AppState {
                data_dir,
                identity,
                state: Mutex::new(state),
                clients: Mutex::new(HashMap::new()),
                discovery: Discovery::start(),
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            device_info,
            get_state,
            set_state,
            create_fleet,
            discover,
            peek_installer,
            rpc
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
