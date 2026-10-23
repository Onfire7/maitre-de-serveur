mod identity;
mod rpc;

use identity::Identity;
use serde::{Deserialize, Serialize};
use std::collections::HashSet;
use std::fs;
use std::path::PathBuf;
use std::sync::Mutex;
use tauri::{Manager, State};

/// Where the Console points. Milestone 0 only: the user pastes the dev
/// Node's address and fingerprint. Pairing replaces this in Milestone 1.
#[derive(Serialize, Deserialize, Clone, Default)]
pub struct Settings {
    pub node_addr: String,
    pub node_fingerprint: String,
}

#[derive(Serialize)]
pub struct DeviceInfo {
    pub fingerprint: String,
    pub public_key_pem: String,
    pub public_key_path: String,
}

#[derive(Serialize)]
pub struct RpcResponse {
    pub status: u16,
    pub content_type: String,
    pub body: Vec<u8>,
}

struct AppState {
    data_dir: PathBuf,
    identity: Identity,
    settings: Mutex<Settings>,
    client: Mutex<Option<reqwest::Client>>,
}

impl AppState {
    fn settings_path(&self) -> PathBuf {
        self.data_dir.join("console.json")
    }

    fn rebuild_client(&self) -> Result<reqwest::Client, String> {
        let settings = self.settings.lock().unwrap().clone();
        let mut trusted = HashSet::new();
        if !settings.node_fingerprint.is_empty() {
            trusted.insert(settings.node_fingerprint.trim().to_lowercase());
        }
        let client = rpc::build_client(&self.identity, trusted)?;
        *self.client.lock().unwrap() = Some(client.clone());
        Ok(client)
    }
}

#[tauri::command]
fn device_info(state: State<'_, AppState>) -> DeviceInfo {
    DeviceInfo {
        fingerprint: state.identity.fingerprint(),
        public_key_pem: state.identity.public_key_pem(),
        public_key_path: state.identity.public_key_path.display().to_string(),
    }
}

#[tauri::command]
fn get_settings(state: State<'_, AppState>) -> Settings {
    state.settings.lock().unwrap().clone()
}

#[tauri::command]
fn set_settings(state: State<'_, AppState>, settings: Settings) -> Result<(), String> {
    fs::write(
        state.settings_path(),
        serde_json::to_vec_pretty(&settings).map_err(|e| e.to_string())?,
    )
    .map_err(|e| e.to_string())?;
    *state.settings.lock().unwrap() = settings;
    state.rebuild_client()?;
    Ok(())
}

/// Send one Connect-protocol unary request to the configured Node.
/// `procedure` is like "/maitre.v1.NodeService/GetNodeInfo".
#[tauri::command]
async fn rpc(state: State<'_, AppState>, procedure: String, body: Vec<u8>) -> Result<RpcResponse, String> {
    let (client, addr) = {
        let existing = state.client.lock().unwrap().clone();
        let addr = state.settings.lock().unwrap().node_addr.clone();
        (existing, addr)
    };
    let client = match client {
        Some(c) => c,
        None => state.rebuild_client()?,
    };
    if addr.is_empty() {
        return Err("no Node address configured".into());
    }
    let res = client
        .post(format!("https://{addr}{procedure}"))
        .header("content-type", "application/proto")
        .header("connect-protocol-version", "1")
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
            let settings = fs::read(data_dir.join("console.json"))
                .ok()
                .and_then(|b| serde_json::from_slice(&b).ok())
                .unwrap_or_default();
            app.manage(AppState {
                data_dir,
                identity,
                settings: Mutex::new(settings),
                client: Mutex::new(None),
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![device_info, get_settings, set_settings, rpc])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
