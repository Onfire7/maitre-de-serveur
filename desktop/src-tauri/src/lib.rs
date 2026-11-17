mod catalogs;
pub mod dial;
mod discovery;
pub mod identity;
mod media;
mod netboot;
mod pairing;
pub mod proxy;
pub mod transfer;
pub mod views;
#[cfg(target_os = "linux")]
mod webkit;
mod recovery;
pub mod rpc;
pub mod sign;

use discovery::{Discovery, Found};
use identity::Identity;
use serde::{Deserialize, Serialize};
use std::collections::HashMap;
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
    /// Each Service's loopback port (decision 288): kept so its page's
    /// origin, and with it the cache and logins, is the same every launch.
    #[serde(default, skip_serializing_if = "HashMap::is_empty")]
    pub ports: HashMap<String, u16>,
}

#[derive(Serialize)]
pub struct DeviceInfo {
    pub fingerprint: String,
    pub public_key_pem: String,
    pub public_key_path: String,
    pub name: String,
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
    /// The one way to a Node (ADR 0012): the RPC client, file transfers
    /// and the loopback proxy all connect through it.
    dialer: Arc<dial::Dialer>,
    nodes: rpc::NodeClient,
    discovery: Discovery,
    netboot: netboot::Netboot,
    media: media::Media,
    views: views::Views,
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

/// Start (or reuse) a loopback proxy for one Page of a Service and return
/// the local URL the built-in browser should open (decision 71). `view` is
/// the Page's host name, which keys its port, its view and its frame; the
/// relay names the Service and the Page in its headers (ADR 0010).
#[tauri::command]
async fn service_url(state: State<'_, AppState>, addr: String, fingerprint: String, view: String, service: String, page: String) -> Result<String, String> {
    let remembered = state.state.lock().unwrap().ports.get(&view).copied();
    let (url, port) = state.proxies.open(state.dialer.clone(), addr, fingerprint, view.clone(), service, page, remembered).await?;
    if remembered != Some(port) {
        let mut st = state.state.lock().unwrap().clone();
        st.ports.insert(view, port);
        state.save(st)?;
    }
    Ok(url)
}

/// The local base URL for a Node's file contents (decision 183): the file
/// manager's previews read `<base>/maitre/files/content?…` from it.
#[tauri::command]
async fn files_url(state: State<'_, AppState>, addr: String, fingerprint: String) -> Result<String, String> {
    state.proxies.open_files(state.dialer.clone(), addr, fingerprint).await
}

/// A Service's Page in a view of its own (decision 294, src/views.rs):
/// opened at `url` where the Console says, placed again as the Console's
/// layout moves, shown or hidden, driven and asked for its colours.
/// Opening and closing wait on the main thread, so they run off it: a
/// sync command runs on the main thread and would wait for itself.
#[tauri::command]
async fn view_open(app: tauri::AppHandle, state: State<'_, AppState>, view: String, url: String, bounds: views::Bounds, shown: bool) -> Result<(), String> {
    state.views.open(&app, &view, &url, bounds, shown)
}

#[tauri::command]
fn view_place(state: State<'_, AppState>, view: String, bounds: views::Bounds) -> Result<(), String> {
    state.views.place(&view, bounds)
}

#[tauri::command]
fn view_show(state: State<'_, AppState>, view: String, shown: bool) -> Result<(), String> {
    state.views.show(&view, shown)
}

#[tauri::command]
async fn view_close(state: State<'_, AppState>, view: String) -> Result<(), String> {
    state.views.close(&view)
}

#[tauri::command]
fn view_navigate(state: State<'_, AppState>, view: String, url: String) -> Result<(), String> {
    state.views.navigate(&view, &url)
}

#[tauri::command]
fn view_reload(state: State<'_, AppState>, view: String) -> Result<(), String> {
    state.views.reload(&view)
}

#[tauri::command]
fn view_stop(state: State<'_, AppState>, view: String) -> Result<(), String> {
    state.views.stop(&view)
}

/// Dev tool: what a view (the main one for an empty `view`) shows, as a PNG.
#[tauri::command]
async fn view_snapshot(app: tauri::AppHandle, state: State<'_, AppState>, view: String, path: String) -> Result<(), String> {
    views::snapshot_to(&app, &state.views, &view, path).await
}

/// Dev tool: a script's result in a view, as JSON.
#[tauri::command]
async fn view_eval(state: State<'_, AppState>, view: String, js: String) -> Result<String, String> {
    views::eval_in(&state.views, &view, js).await
}

/// Dev tool: end a view's web process, to see the Console take a crash.
#[tauri::command]
fn view_crash(state: State<'_, AppState>, view: String) -> Result<(), String> {
    views::crash(&state.views, &view)
}

#[tauri::command]
async fn view_sample(state: State<'_, AppState>, view: String, cols: i32, rows: i32) -> Result<Vec<Vec<String>>, String> {
    state.views.sample(&view, cols, rows).await
}

#[derive(Serialize, Clone)]
struct TransferProgress {
    id: String,
    done: u64,
    total: u64,
    current: String,
}

/// A transfer's progress, sent to the Console as `transfer` events.
fn transfer_events(app: tauri::AppHandle, id: String) -> transfer::Progress {
    use tauri::Emitter;
    Arc::new(move |done, total, current| {
        let _ = app.emit("transfer", TransferProgress { id: id.clone(), done, total, current: current.into() });
    })
}

/// Save a file (or a folder as a zip) from a Node to `dest` on this computer.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
async fn files_download(app: tauri::AppHandle, state: State<'_, AppState>, addr: String, fingerprint: String, scope: transfer::Scope, path: String, folder: bool, dest: String, id: String) -> Result<(), String> {
    transfer::download(&state.nodes, &addr, &fingerprint, scope, path, folder, dest, transfer_events(app, id)).await
}

/// Upload local files and folders into `folder` of a scope on a Node.
#[tauri::command]
#[allow(clippy::too_many_arguments)]
async fn files_upload(app: tauri::AppHandle, state: State<'_, AppState>, addr: String, fingerprint: String, scope: transfer::Scope, folder: String, paths: Vec<String>, id: String) -> Result<transfer::UploadResult, String> {
    transfer::upload(&state.nodes, &addr, &fingerprint, scope, folder, paths, transfer_events(app, id)).await
}

/// Send one Connect-protocol unary request to `addr`, trusting only
/// `fingerprint`. `procedure` is like "/maitre.v1.NodeService/GetNodeInfo".
/// An error means the Node could not be reached, which the Console's
/// transport takes as the cue to try the Node's other addresses.
#[tauri::command]
async fn rpc(
    state: State<'_, AppState>,
    addr: String,
    fingerprint: String,
    procedure: String,
    body: Vec<u8>,
) -> Result<rpc::RpcResponse, String> {
    if addr.is_empty() || fingerprint.is_empty() {
        return Err("rpc needs an address and a fingerprint".into());
    }
    state.nodes.call(&addr, &fingerprint, &procedure, body).await
}

#[tauri::command]
fn netboot_files(state: State<'_, AppState>) -> netboot::FilesStatus {
    netboot::Netboot::files_status(&state.data_dir)
}

/// base: URL ending in "/" where netboot.json and the files live
/// (a Depot's /boot/ or the GitHub release); empty means the release.
#[tauri::command]
async fn netboot_depot_boot(base: String) -> Result<netboot::DepotBoot, String> {
    netboot::Netboot::depot_boot(base).await
}

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

/// The Node the Console now reads the Fleet through, at the address that
/// answered (decision 159): Nodes this Device cannot reach itself are
/// reached through its Relay (ADR 0012). None forgets it.
#[tauri::command]
fn relay_through(state: State<'_, AppState>, anchor: Option<RelayAnchor>) {
    state.dialer.relay_through(anchor.map(|a| dial::Anchor { addr: a.addr, fingerprint: a.fingerprint }));
}

/// How this Device last reached the Node `fingerprint`: "direct",
/// "relayed" (through the anchor's Relay), or null when no way is
/// remembered (none yet, it failed, or the anchor changed).
#[tauri::command]
fn node_way(state: State<'_, AppState>, fingerprint: String) -> Option<dial::Way> {
    state.dialer.way(&fingerprint)
}

#[derive(Deserialize)]
struct RelayAnchor {
    addr: String,
    fingerprint: String,
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
    let nodes = rpc::NodeClient::new(Arc::new(dial::Dialer::new(Arc::new(rec_id))));
    let res = nodes.call(&addr, &fingerprint, "/maitre.v1.NodeService/PutRecord", body).await?;
    if !(200..300).contains(&res.status) {
        let text = String::from_utf8_lossy(&res.body);
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
            // WebKitGTK 2.52 crashes on a page's first View Transition when
            // the DMA-BUF renderer is off (see webkit.rs): turn the feature off.
            // A Service's page is a frame on a loopback origin of its own,
            // so to WebKitGTK its cookies are third-party, which its default
            // policy refuses: a login that lives in a cookie (Mealie,
            // Tandoor, Paperless) never held (decision 292). The Console
            // frames only Services of the user's own Fleet, so every
            // cookie is accepted.
            // The Service views need a layer above the main view to sit
            // on (decision 294, views.rs).
            #[cfg(target_os = "linux")]
            if let Some(w) = app.get_webview_window("main") {
                let _ = w.with_webview(|w| {
                    if let Err(why) = webkit::disable_view_transitions(&w.inner()) {
                        eprintln!("webkit: WebKitGTK {} left as it is: {why}", webkit::version());
                    }
                    if let Err(why) = webkit::accept_every_cookie(&w.inner()) {
                        eprintln!("webkit: cookies of a Service's frame may not hold: {why}");
                    }
                    if let Err(why) = views::placement::prepare(&w.inner()) {
                        eprintln!("views: Service pages cannot be placed: {why}");
                    }
                });
            }
            // What a page's own script reports through the proxy's beacon
            // reaches the Console as a view event.
            let handle = app.handle().clone();
            let proxies = proxy::Proxies::default();
            proxies.set_reporter(move |view, event, url| views::report(&handle, view, event, url));
            let identity = Arc::new(identity);
            let dialer = Arc::new(dial::Dialer::new(identity.clone()));
            app.manage(AppState {
                data_dir,
                identity,
                proxies,
                state: Mutex::new(state),
                nodes: rpc::NodeClient::new(dialer.clone()),
                dialer,
                discovery: Discovery::start(),
                netboot: netboot::Netboot::default(),
                media: media::Media::default(),
                views: views::Views::default(),
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
            view_open,
            view_place,
            view_show,
            view_close,
            view_navigate,
            view_reload,
            view_stop,
            view_sample,
            view_snapshot,
            view_eval,
            view_crash,
            files_url,
            files_download,
            files_upload,
            rpc,
            relay_through,
            node_way,
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
            netboot_depot_boot,
            netboot_start,
            netboot_stop,
            netboot_status
        ])
        .run(tauri::generate_context!())
        .expect("error while running tauri application");
}
