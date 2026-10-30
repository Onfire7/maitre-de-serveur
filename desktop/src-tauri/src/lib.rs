mod catalogs;
mod discovery;
pub mod identity;
mod media;
mod netboot;
mod pairing;
pub mod proxy;
pub mod transfer;
mod recovery;
mod rpc;
pub mod sign;

use discovery::{Discovery, Found};
use identity::Identity;
use serde::{Deserialize, Serialize};
use std::collections::{HashMap, HashSet};
use std::fs;
use std::path::PathBuf;
use std::sync::{Arc, Mutex};
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
    /// The Installer's key as PEM: it becomes the Node's key, and the Node
    /// record introducing it to the Fleet carries it (Milestone 3).
    pub public_key_pem: String,
}

struct AppState {
    data_dir: PathBuf,
    identity: Arc<Identity>,
    proxies: proxy::Proxies,
    state: Mutex<ConsoleState>,
    clients: Mutex<HashMap<String, reqwest::Client>>,
    discovery: Discovery,
    netboot: netboot::Netboot,
    media: media::Media,
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
        public_key_pem: identity::public_key_pem_of_raw(&key),
    })
}

/// Start (or reuse) a loopback proxy for a Service's web page and return
/// the local URL the built-in browser should open (decision 71).
#[tauri::command]
async fn service_url(state: State<'_, AppState>, addr: String, fingerprint: String, service: String) -> Result<String, String> {
    state.proxies.open(state.identity.clone(), addr, fingerprint, service).await
}

/// The local base URL for a Node's file contents (decision 183): the file
/// manager's previews read `<base>/maitre/files/content?…` from it.
#[tauri::command]
async fn files_url(state: State<'_, AppState>, addr: String, fingerprint: String) -> Result<String, String> {
    state.proxies.open_files(state.identity.clone(), addr, fingerprint).await
}

/// Save a file (or a folder as a zip) from a Node to `dest` on this computer.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
async fn files_download(app: tauri::AppHandle, state: State<'_, AppState>, addr: String, fingerprint: String, scope: transfer::Scope, path: String, folder: bool, dest: String, id: String) -> Result<(), String> {
    let client = state.client_for(&fingerprint)?;
    transfer::download(app, client, addr, scope, path, folder, dest, id).await
}

/// Upload local files and folders into `folder` of a scope on a Node.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
async fn files_upload(app: tauri::AppHandle, state: State<'_, AppState>, addr: String, fingerprint: String, scope: transfer::Scope, folder: String, paths: Vec<String>, id: String) -> Result<transfer::UploadResult, String> {
    let client = state.client_for(&fingerprint)?;
    transfer::upload(app, client, addr, scope, folder, paths, id).await
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

#[tauri::command]
fn netboot_files(state: State<'_, AppState>) -> netboot::FilesStatus {
    netboot::Netboot::files_status(&state.data_dir)
}

/// base: URL ending in "/" where netboot.json and the files live
/// (a Depot's /boot/ or the GitHub release); empty means the release.
#[tauri::command]
async fn netboot_download(app: tauri::AppHandle, state: State<'_, AppState>, base: String) -> Result<netboot::FilesStatus, String> {
    let base = if base.is_empty() { netboot::RELEASE_BASE.to_string() } else { base };
    netboot::Netboot::download(app, state.data_dir.clone(), base).await
}

#[tauri::command]
fn netboot_start(app: tauri::AppHandle, state: State<'_, AppState>, opts: netboot::StartOptions) -> Result<(), String> {
    state.netboot.start(app, &state.data_dir, opts)
}

#[tauri::command]
fn netboot_stop(state: State<'_, AppState>) {
    state.netboot.stop(&state.data_dir)
}

#[tauri::command]
fn netboot_status(app: tauri::AppHandle, state: State<'_, AppState>) -> netboot::Status {
    state.netboot.status(&app, &state.data_dir)
}

/// Six-digit code for a device key (decision 99), for the asking Console's own
/// screen and the approving Console's list.
#[tauri::command]
fn device_code(state: State<'_, AppState>, key_pem: Option<String>) -> Result<String, String> {
    match key_pem {
        Some(pem) if !pem.is_empty() => {
            let raw = raw_key_of_pem(&pem)?;
            Ok(pairing::device_code(&raw))
        }
        _ => Ok(pairing::device_code(state.identity.public_key_raw())),
    }
}

fn raw_key_of_pem(pem: &str) -> Result<Vec<u8>, String> {
    use base64::Engine;
    let b64: String = pem.lines().filter(|l| !l.starts_with("-----")).collect();
    let der = base64::engine::general_purpose::STANDARD.decode(b64.trim()).map_err(|e| e.to_string())?;
    if der.len() != 44 {
        return Err("not an Ed25519 public key".into());
    }
    Ok(der[12..].to_vec())
}

/// Announce this Console as a device asking to join a Fleet (decision 99).
#[tauri::command]
fn announce_join(state: State<'_, AppState>) -> Result<String, String> {
    let name = device_name();
    state.discovery.announce_device(&state.identity.fingerprint(), &name, state.identity.public_key_raw())
}

#[tauri::command]
fn withdraw_join(state: State<'_, AppState>, fullname: String) {
    state.discovery.withdraw(&fullname)
}

/// The Recovery Phrase path (decision 100): with the phrase's key, ask a Node
/// to add this Console's own key as a Trusted Device. Returns the recovery
/// public key PEM for the Console's Fleet state.
#[tauri::command]
async fn recover_with_phrase(_state: State<'_, AppState>, phrase: String, addr: String, fingerprint: String, body: Vec<u8>) -> Result<String, String> {
    let entropy = recovery::parse(&phrase)?;
    let key = recovery::key_pair(&entropy)?;
    let pem = key.public_key_pem();
    let rec_id = Identity::from_key_pair(key)?;
    let client = rpc::build_client(&rec_id, HashSet::from([fingerprint.trim().to_lowercase()]))?;
    let res = client
        .post(format!("https://{addr}/maitre.v1.NodeService/PutRecord"))
        .header("content-type", "application/proto")
        .header("connect-protocol-version", "1")
        .timeout(std::time::Duration::from_secs(15))
        .body(body)
        .send()
        .await
        .map_err(|e| e.to_string())?;
    if !res.status().is_success() {
        let text = res.text().await.unwrap_or_default();
        return Err(format!("the Node refused the phrase: {text}"));
    }
    Ok(pem)
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
pub struct SignedRecord {
    pub signer_key: Vec<u8>,
    pub signature: Vec<u8>,
    pub signed_unix_ms: i64,
}

/// Sign a record's content (decision 189) with this device's key, or with
/// the Recovery Phrase's key when a phrase is given. The webview passes the
/// fields; the bytes signed are always built here.
#[tauri::command]
fn sign_record(state: State<'_, AppState>, kind: String, id: String, body: Vec<u8>, deleted: bool, signed_unix_ms: i64, phrase: Option<String>) -> Result<SignedRecord, String> {
    let der = match phrase.as_deref().map(str::trim).filter(|p| !p.is_empty()) {
        Some(p) => recovery::key_pair(&recovery::parse(p)?)?.serialize_der(),
        None => state.identity.pkcs8_der(),
    };
    let key = sign::key_of_pkcs8(&der)?;
    let s = sign::sign(&key, &kind, &id, &body, deleted, signed_unix_ms);
    Ok(SignedRecord { signer_key: s.signer_key, signature: s.signature, signed_unix_ms })
}

/// source: "depot" for a stick built from a Depot's netboot files, else the release image.
#[tauri::command]
fn media_image(state: State<'_, AppState>, source: Option<String>) -> media::ImageStatus {
    media::Media::image_status(&state.data_dir, media::is_depot(&source))
}

/// Build a stick image from the netboot files already downloaded (decision 221).
#[tauri::command]
async fn media_build(state: State<'_, AppState>, image: String, insecure: String) -> Result<media::ImageStatus, String> {
    let helper = netboot::Netboot::helper_path()?;
    let data_dir = state.data_dir.clone();
    tauri::async_runtime::spawn_blocking(move || media::Media::build(&helper, &data_dir, &netboot::Netboot::boot_dir(&data_dir), &image, &insecure)).await.map_err(|e| e.to_string())?
}

#[tauri::command]
async fn media_download(app: tauri::AppHandle, state: State<'_, AppState>, url: String) -> Result<media::ImageStatus, String> {
    media::Media::download(app, state.data_dir.clone(), url).await
}

#[tauri::command]
fn media_disks() -> Result<Vec<media::Disk>, String> {
    media::Media::list(&netboot::Netboot::helper_path()?)
}

#[tauri::command]
fn media_write(app: tauri::AppHandle, state: State<'_, AppState>, device: String, source: Option<String>) -> Result<(), String> {
    let helper = netboot::Netboot::helper_path()?;
    state.media.write(app, &state.data_dir, &helper, device, media::is_depot(&source), netboot::elevated)
}

#[tauri::command]
fn media_status(state: State<'_, AppState>) -> media::WriteStatus {
    state.media.status()
}

#[tauri::command]
async fn catalog_load(state: State<'_, AppState>, source: catalogs::Source) -> Result<catalogs::Loaded, String> {
    let helper = netboot::Netboot::helper_path()?;
    let data_dir = state.data_dir.clone();
    tauri::async_runtime::spawn_blocking(move || catalogs::load(&helper, &data_dir, &source)).await.map_err(|e| e.to_string())?
}

#[tauri::command]
async fn catalog_add(folder: String, compose: String) -> Result<serde_json::Value, String> {
    let helper = netboot::Netboot::helper_path()?;
    tauri::async_runtime::spawn_blocking(move || catalogs::add(&helper, &folder, &compose)).await.map_err(|e| e.to_string())?
}

#[cfg_attr(mobile, tauri::mobile_entry_point)]
pub fn run() {
    tauri::Builder::default()
        .plugin(tauri_plugin_opener::init())
        .plugin(tauri_plugin_dialog::init())
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
                identity: Arc::new(identity),
                proxies: proxy::Proxies::default(),
                state: Mutex::new(state),
                clients: Mutex::new(HashMap::new()),
                discovery: Discovery::start(),
                netboot: netboot::Netboot::default(),
                media: media::Media::default(),
            });
            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            catalog_load,
            catalog_add,
            device_info,
            get_state,
            set_state,
            create_fleet,
            discover,
            peek_installer,
            service_url,
            files_url,
            files_download,
            files_upload,
            rpc,
            device_code,
            announce_join,
            withdraw_join,
            recover_with_phrase,
            sign_record,
            media_image,
            media_build,
            media_download,
            media_disks,
            media_write,
            media_status,
            netboot_files,
            netboot_download,
            netboot_start,
            netboot_stop,
            netboot_status
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
