//! Netboot from the Console (decisions 92 to 95): download the boot bundle
//! once, then run the bundled `maitre netboot` helper elevated so a new
//! machine can boot the Installer from the network. The helper writes JSON
//! events to a file we tail, because elevation on macOS and Windows gives
//! us no live stdout; a stop file tells it to quit.

use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use std::collections::BTreeMap;
use std::fs;
use std::io::{BufRead, BufReader, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Emitter};

/// Where releases put the bundle (`<base><file>`).
pub const RELEASE_BASE: &str = "https://github.com/Onfire7/maitre-de-serveur/releases/latest/download/";
const MANIFEST: &str = "netboot.json";
/// The only files a bundle may contain; manifest names outside this set are
/// rejected, so a hostile manifest cannot pick where we write.
const BOOT_FILES: [&str; 6] = ["shimx64.efi", "grubx64.efi", "mmx64.efi", "vmlinuz", "initramfs.img", "rootfs.squashfs"];
/// Files a bundle may add under `optional` (decision 223): iPXE, which
/// boots legacy BIOS machines. Kept out of `files`, which Desktops before it
/// check strictly; unknown optional names are skipped, not refused.
const OPTIONAL_FILES: [&str; 1] = [BIOS_FILE];
const BIOS_FILE: &str = "undionly.kpxe";

#[derive(Serialize, Deserialize, Clone)]
pub struct Manifest {
    #[serde(default)]
    pub image: String,
    #[serde(default)]
    pub kernel: String,
    /// Version of the image the files were built from, and when (RFC 3339);
    /// bundles before decision 267 carry neither.
    #[serde(default)]
    pub version: String,
    #[serde(default)]
    pub built: String,
    pub files: BTreeMap<String, ManifestFile>,
    #[serde(default)]
    pub optional: BTreeMap<String, ManifestFile>,
}

#[derive(Serialize, Deserialize, Clone)]
pub struct ManifestFile {
    pub size: u64,
    pub sha256: String,
}

#[derive(Serialize, Clone)]
pub struct FilesStatus {
    pub dir: String,
    /// Image the downloaded bundle installs (from its manifest), if any.
    pub image: String,
    pub ready: bool,
    pub have_bytes: u64,
    pub total_bytes: u64,
    pub missing: Vec<String>,
    /// The bundle also boots legacy BIOS machines.
    pub bios: bool,
    /// sha256 of each boot file as its manifest lists it, so the Console
    /// can tell when a Depot has rebuilt them (decision 263).
    pub digests: BTreeMap<String, String>,
    /// Which Installer the files boot and when it was built, from the
    /// manifest, and when the files were downloaded here (decision 267).
    pub version: String,
    pub built: String,
    pub downloaded: String,
}

/// What a Depot's boot files are now (decision 263, 267).
#[derive(Serialize, Clone)]
pub struct DepotBoot {
    pub digests: BTreeMap<String, String>,
    pub version: String,
    pub built: String,
}

#[derive(Serialize, Clone)]
pub struct Progress {
    pub file: String,
    pub done: u64,
    pub total: u64,
    pub finished: bool,
    pub error: String,
}

#[derive(Serialize, Clone, Default)]
pub struct Status {
    pub running: bool,
    pub ip: String,
    pub interface: String,
    pub image: String,
    pub idle_seconds: u64,
    pub clients: Vec<String>,
    /// Machines that asked but these boot files cannot start (decision 223),
    /// as "mac firmware" ("bios", or "" for another architecture).
    pub unsupported: Vec<String>,
    pub last_error: String,
    pub events: Vec<serde_json::Value>,
    /// Started by an earlier run of this app, which left it running
    /// (decision 222); found from its events file and its answering port.
    pub adopted: bool,
}

#[derive(Deserialize)]
pub struct StartOptions {
    pub image: String,
    #[serde(default)]
    pub insecure: String,
    pub idle_seconds: u64,
    #[serde(default)]
    pub serial: bool,
}

#[derive(Default)]
pub struct Netboot {
    child: Mutex<Option<Child>>,
    status: Arc<Mutex<Status>>,
    /// Whether this run has looked for a helper an earlier run left behind.
    looked: Mutex<bool>,
}

/// Where a helper that said "ready" (and has not said "stopped") serves its
/// boot files, read from its events file.
pub fn left_running(events: &Path) -> Option<String> {
    let text = fs::read_to_string(events).ok()?;
    let mut addr = None;
    for line in text.lines() {
        let Ok(v) = serde_json::from_str::<serde_json::Value>(line) else { continue };
        match v.get("event").and_then(|e| e.as_str()) {
            Some("ready") => {
                let ip = v.get("ip").and_then(|x| x.as_str()).unwrap_or("");
                let port = v.get("http_port").and_then(|x| x.as_u64()).unwrap_or(0);
                addr = (!ip.is_empty() && port > 0).then(|| format!("{ip}:{port}"));
            }
            Some("stopped") => addr = None,
            _ => {}
        }
    }
    addr
}

/// Whether something still accepts connections at addr (the helper's boot file server).
fn answers(addr: &str) -> bool {
    addr.parse::<std::net::SocketAddr>().map(|a| std::net::TcpStream::connect_timeout(&a, Duration::from_millis(300)).is_ok()).unwrap_or(false)
}

/// The helper's port error, in words: who else may hold the ports.
fn explain(message: &str) -> String {
    if message.contains("address already in use") {
        return "Something else on this computer already answers network boot requests (ports 67, 69 or 4011): another network boot from Maître that this app cannot see, a Depot started with NETBOOT=1, or a DHCP or TFTP server such as dnsmasq. Stop it, then turn this on again.".into();
    }
    message.into()
}

impl Netboot {
    pub fn boot_dir(data_dir: &Path) -> PathBuf {
        data_dir.join("netboot")
    }

    pub fn files_status(data_dir: &Path) -> FilesStatus {
        let dir = Self::boot_dir(data_dir);
        let manifest: Option<Manifest> = fs::read(dir.join(MANIFEST)).ok().and_then(|b| serde_json::from_slice(&b).ok());
        let mut st = FilesStatus { dir: dir.to_string_lossy().into_owned(), image: String::new(), ready: false, have_bytes: 0, total_bytes: 0, missing: vec![], bios: false, digests: BTreeMap::new(), version: String::new(), built: String::new(), downloaded: String::new() };
        let Some(m) = manifest else {
            return st;
        };
        st.image = m.image.clone();
        st.digests = digests_of(&m);
        st.version = m.version.clone();
        st.built = m.built.clone();
        st.downloaded = fs::metadata(dir.join(MANIFEST)).and_then(|md| md.modified()).ok().map(rfc3339).unwrap_or_default();
        for (name, f) in &m.files {
            if !BOOT_FILES.contains(&name.as_str()) {
                continue;
            }
            st.total_bytes += f.size;
            match fs::metadata(dir.join(name)) {
                Ok(md) if md.len() == f.size => st.have_bytes += f.size,
                _ => st.missing.push(name.clone()),
            }
        }
        st.ready = st.missing.is_empty() && !m.files.is_empty();
        st.bios = m.optional.get(BIOS_FILE).is_some_and(|f| fs::metadata(dir.join(BIOS_FILE)).is_ok_and(|md| md.len() == f.size));
        st
    }

    /// The boot files a Depot serves now, from its netboot.json: digests by
    /// name (decision 263), and the version and build time of the Installer
    /// they boot (decision 267).
    pub async fn depot_boot(base: String) -> Result<DepotBoot, String> {
        let client = reqwest::Client::builder().timeout(std::time::Duration::from_secs(5)).build().map_err(|e| e.to_string())?;
        let raw = client.get(format!("{base}{MANIFEST}")).send().await.map_err(|e| e.to_string())?.error_for_status().map_err(|e| e.to_string())?.bytes().await.map_err(|e| e.to_string())?;
        let manifest: Manifest = serde_json::from_slice(&raw).map_err(|e| format!("netboot.json: {e}"))?;
        Ok(DepotBoot { digests: digests_of(&manifest), version: manifest.version, built: manifest.built })
    }

    /// Download the bundle from `base` (ending in `/`) into the data dir,
    /// verifying every file against the manifest. Emits `netboot-progress`.
    pub async fn download(app: AppHandle, data_dir: PathBuf, base: String) -> Result<FilesStatus, String> {
        let dir = Self::boot_dir(&data_dir);
        fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        let client = reqwest::Client::builder().build().map_err(|e| e.to_string())?;
        let res = client.get(format!("{base}{MANIFEST}")).send().await.map_err(|e| e.to_string())?;
        if res.status() == reqwest::StatusCode::NOT_FOUND && base == RELEASE_BASE {
            return Err("No Maître release has been published yet, so there is nothing to download from GitHub. Developers: tick the Depot option and use your own build.".into());
        }
        let raw = res.error_for_status().map_err(|e| e.to_string())?.bytes().await.map_err(|e| e.to_string())?;
        let manifest: Manifest = serde_json::from_slice(&raw).map_err(|e| format!("netboot.json: {e}"))?;
        if let Some(bad) = manifest.files.keys().find(|n| !BOOT_FILES.contains(&n.as_str())) {
            return Err(format!("netboot.json names an unexpected file {bad:?}; refusing this bundle"));
        }
        if !manifest.files.is_empty() && manifest.files.len() != BOOT_FILES.len() {
            return Err("netboot.json is missing boot files; refusing this bundle".into());
        }
        let optional: Vec<(&String, &ManifestFile)> = manifest.optional.iter().filter(|(n, _)| OPTIONAL_FILES.contains(&n.as_str())).collect();
        if manifest.files.values().chain(optional.iter().map(|(_, f)| *f)).any(|f| f.sha256.len() != 64 || !f.sha256.bytes().all(|b| b.is_ascii_hexdigit())) {
            return Err("netboot.json has a malformed digest; refusing this bundle".into());
        }
        // An optional file this bundle lacks must not linger from an older one.
        for name in OPTIONAL_FILES {
            if !manifest.optional.contains_key(name) {
                let _ = fs::remove_file(dir.join(name));
            }
        }
        let emit = |p: Progress| {
            let _ = app.emit("netboot-progress", p);
        };
        for (name, f) in manifest.files.iter().chain(optional) {
            let path = dir.join(name);
            if let Ok(md) = fs::metadata(&path) {
                if md.len() == f.size && file_sha256(&path)? == f.sha256 {
                    emit(Progress { file: name.clone(), done: f.size, total: f.size, finished: true, error: String::new() });
                    continue;
                }
            }
            let tmp = dir.join(format!("{name}.part"));
            let res = client.get(format!("{base}{name}")).send().await.map_err(|e| e.to_string())?.error_for_status().map_err(|e| format!("{name}: {e}"))?;
            let mut out = fs::File::create(&tmp).map_err(|e| e.to_string())?;
            let mut hasher = Sha256::new();
            let mut done = 0u64;
            let mut stream = res.bytes_stream();
            let mut last = std::time::Instant::now();
            while let Some(chunk) = stream.next().await {
                let chunk = chunk.map_err(|e| e.to_string())?;
                std::io::Write::write_all(&mut out, &chunk).map_err(|e| e.to_string())?;
                hasher.update(&chunk);
                done += chunk.len() as u64;
                if last.elapsed() > Duration::from_millis(200) {
                    emit(Progress { file: name.clone(), done, total: f.size, finished: false, error: String::new() });
                    last = std::time::Instant::now();
                }
            }
            drop(out);
            let sum = hex::encode(hasher.finalize());
            if sum != f.sha256 {
                let _ = fs::remove_file(&tmp);
                let msg = format!("{name}: digest mismatch (got {sum})");
                emit(Progress { file: name.clone(), done, total: f.size, finished: true, error: msg.clone() });
                return Err(msg);
            }
            fs::rename(&tmp, &path).map_err(|e| e.to_string())?;
            emit(Progress { file: name.clone(), done: f.size, total: f.size, finished: true, error: String::new() });
        }
        fs::write(dir.join(MANIFEST), &raw).map_err(|e| e.to_string())?;
        Ok(Self::files_status(&data_dir))
    }

    /// The bundled helper: next to our executable in a bundle (Tauri copies
    /// externalBin there, also for `tauri dev`), or MAITRE_BIN in development.
    pub fn helper_path() -> Result<PathBuf, String> {
        if let Ok(p) = std::env::var("MAITRE_BIN") {
            if Path::new(&p).exists() {
                return Ok(PathBuf::from(p));
            }
        }
        let exe = std::env::current_exe().map_err(|e| e.to_string())?;
        let dir = exe.parent().ok_or("no executable directory")?;
        for name in ["maitre", "maitre.exe"] {
            let p = dir.join(name);
            if p.exists() {
                return Ok(p);
            }
        }
        Err("the maitre helper is not bundled with this build".into())
    }

    /// Pick up a helper an earlier run of this app left running, once per run:
    /// its events file says it is ready and its boot file server answers.
    /// Turn off then stops it like one started here (both use the stop file).
    pub fn adopt(&self, app: &AppHandle, data_dir: &Path) -> bool {
        let events = Self::boot_dir(data_dir).join("events.jsonl");
        let Some(addr) = left_running(&events) else { return false };
        if !answers(&addr) {
            return false;
        }
        *self.status.lock().unwrap() = Status { adopted: true, ..Default::default() };
        let status = self.status.clone();
        let app = app.clone();
        // Replays the file from the start: ready, clients, then whatever comes.
        std::thread::spawn(move || tail_events(app, events, status));
        true
    }

    pub fn start(&self, app: AppHandle, data_dir: &Path, opts: StartOptions) -> Result<(), String> {
        if self.child.lock().unwrap().as_mut().map(|c| c.try_wait().ok().flatten().is_none()).unwrap_or(false) {
            return Err("netboot is already running".into());
        }
        if self.status.lock().unwrap().running || self.adopt(&app, data_dir) {
            return Err("Network boot is already running on this computer, from before this app was restarted. Turn it off first.".into());
        }
        let dir = Self::boot_dir(data_dir);
        let helper = Self::helper_path()?;
        let events = dir.join("events.jsonl");
        let stop = dir.join("stop");
        let _ = fs::remove_file(&events);
        let _ = fs::remove_file(&stop);
        let mut args: Vec<String> = vec![
            "netboot".into(),
            "--dir".into(), dir.to_string_lossy().into_owned(),
            "--image".into(), opts.image.clone(),
            "--idle".into(), format!("{}s", opts.idle_seconds),
            "--events-file".into(), events.to_string_lossy().into_owned(),
            "--stop-file".into(), stop.to_string_lossy().into_owned(),
        ];
        if !opts.insecure.is_empty() {
            args.push("--insecure".into());
            args.push(opts.insecure.clone());
        }
        if opts.serial {
            args.push("--serial".into());
        }
        let child = elevated(&helper, &args)?;
        *self.status.lock().unwrap() = Status { image: opts.image, idle_seconds: opts.idle_seconds, ..Default::default() };
        *self.child.lock().unwrap() = Some(child);
        let status = self.status.clone();
        std::thread::spawn(move || tail_events(app, events, status));
        Ok(())
    }

    pub fn stop(&self, data_dir: &Path) {
        let _ = fs::write(Self::boot_dir(data_dir).join("stop"), b"");
        // The helper notices within a second; give it a moment, then reap.
        if let Some(mut c) = self.child.lock().unwrap().take() {
            std::thread::spawn(move || {
                for _ in 0..30 {
                    if c.try_wait().ok().flatten().is_some() {
                        return;
                    }
                    std::thread::sleep(Duration::from_millis(100));
                }
                let _ = c.kill();
            });
        }
        self.status.lock().unwrap().running = false;
    }

    pub fn status(&self, app: &AppHandle, data_dir: &Path) -> Status {
        let first = !std::mem::replace(&mut *self.looked.lock().unwrap(), true);
        if first && self.child.lock().unwrap().is_none() && self.adopt(app, data_dir) {
            // Give the replay a moment so the first answer already shows it.
            std::thread::sleep(Duration::from_millis(300));
        }
        let mut st = self.status.lock().unwrap().clone();
        // Nothing to reap for an adopted helper: it is gone when its port is.
        if st.adopted && st.running {
            let addr = left_running(&Self::boot_dir(data_dir).join("events.jsonl"));
            if !addr.as_deref().map(answers).unwrap_or(false) {
                self.status.lock().unwrap().running = false;
                st.running = false;
            }
        }
        if let Some(c) = self.child.lock().unwrap().as_mut() {
            if let Ok(Some(code)) = c.try_wait() {
                if st.running && st.last_error.is_empty() && !code.success() {
                    st.last_error = format!("the helper exited with {code}");
                }
                st.running = false;
            }
        }
        st
    }
}

/// Run the helper with administrator rights, the way each OS does it.
pub fn elevated(helper: &Path, args: &[String]) -> Result<Child, String> {
    #[cfg(target_os = "linux")]
    {
        let mut cmd = Command::new("pkexec");
        cmd.arg(helper).args(args);
        cmd.stdout(Stdio::null()).stderr(Stdio::piped()).stdin(Stdio::null());
        return cmd.spawn().map_err(|e| format!("pkexec: {e} (is polkit installed?)"));
    }
    #[cfg(target_os = "macos")]
    {
        // Untested (decision 95): osascript prompts, the helper detaches.
        let quoted: Vec<String> = std::iter::once(helper.to_string_lossy().into_owned()).chain(args.iter().cloned()).map(|a| format!("'{}'", a.replace('\'', "'\\''"))).collect();
        let script = format!("do shell script \"{} >/dev/null 2>&1 &\" with administrator privileges", quoted.join(" ").replace('"', "\\\""));
        let mut cmd = Command::new("osascript");
        cmd.arg("-e").arg(script).stdout(Stdio::null()).stderr(Stdio::piped()).stdin(Stdio::null());
        return cmd.spawn().map_err(|e| format!("osascript: {e}"));
    }
    #[cfg(target_os = "windows")]
    {
        // Untested (decision 95): UAC prompt through PowerShell.
        let list: Vec<String> = args.iter().map(|a| format!("'{}'", a.replace('\'', "''"))).collect();
        let ps = format!("Start-Process -FilePath '{}' -ArgumentList @({}) -Verb RunAs -WindowStyle Hidden", helper.to_string_lossy(), list.join(","));
        let mut cmd = Command::new("powershell");
        cmd.args(["-NoProfile", "-Command", &ps]).stdout(Stdio::null()).stderr(Stdio::piped()).stdin(Stdio::null());
        return cmd.spawn().map_err(|e| format!("powershell: {e}"));
    }
    #[allow(unreachable_code)]
    Err("netboot is not supported on this platform".into())
}

/// Follow the helper's events file and mirror it into the status and to
/// the webview as `netboot-event`.
fn tail_events(app: AppHandle, path: PathBuf, status: Arc<Mutex<Status>>) {
    let mut offset = 0u64;
    let started = std::time::Instant::now();
    loop {
        if let Ok(mut f) = fs::File::open(&path) {
            let _ = f.seek(SeekFrom::Start(offset));
            let mut reader = BufReader::new(f);
            let mut line = String::new();
            while let Ok(n) = reader.read_line(&mut line) {
                if n == 0 {
                    break;
                }
                offset += n as u64;
                if let Ok(v) = serde_json::from_str::<serde_json::Value>(line.trim()) {
                    let kind = v.get("event").and_then(|e| e.as_str()).unwrap_or("").to_string();
                    {
                        let mut st = status.lock().unwrap();
                        match kind.as_str() {
                            "ready" => {
                                st.running = true;
                                st.ip = v.get("ip").and_then(|x| x.as_str()).unwrap_or("").into();
                                st.interface = v.get("interface").and_then(|x| x.as_str()).unwrap_or("").into();
                                // What an adopted helper serves; the same as asked for one started here.
                                if let Some(image) = v.get("image").and_then(|x| x.as_str()) {
                                    st.image = image.into();
                                }
                                if let Some(idle) = v.get("idle_seconds").and_then(|x| x.as_u64()) {
                                    st.idle_seconds = idle;
                                }
                            }
                            "client" => {
                                let mac = v.get("mac").and_then(|x| x.as_str()).unwrap_or("").to_string();
                                if !mac.is_empty() && !st.clients.contains(&mac) {
                                    st.clients.push(mac);
                                }
                            }
                            "unsupported" => {
                                let mac = v.get("mac").and_then(|x| x.as_str()).unwrap_or("");
                                let fw = v.get("firmware").and_then(|x| x.as_str()).unwrap_or("");
                                let entry = format!("{mac} {fw}");
                                if !mac.is_empty() && !st.unsupported.contains(&entry) {
                                    st.unsupported.push(entry);
                                }
                            }
                            "error" => st.last_error = explain(v.get("message").and_then(|x| x.as_str()).unwrap_or("")),
                            "stopped" => st.running = false,
                            _ => {}
                        }
                        st.events.push(v.clone());
                        if st.events.len() > 200 {
                            st.events.remove(0);
                        }
                    }
                    let _ = app.emit("netboot-event", v);
                    if kind == "stopped" {
                        return;
                    }
                }
                line.clear();
            }
        } else if started.elapsed() > Duration::from_secs(60) {
            // Elevation refused or the helper never started.
            let mut st = status.lock().unwrap();
            if !st.running {
                st.last_error = "the netboot helper did not start (was the password prompt cancelled?)".into();
                let _ = app.emit("netboot-event", serde_json::json!({"event": "stopped", "reason": "not started"}));
                return;
            }
        }
        std::thread::sleep(Duration::from_millis(500));
    }
}

/// Each required boot file's digest, as the manifest lists it.
/// A file time as RFC 3339 in UTC, whole seconds.
fn rfc3339(t: std::time::SystemTime) -> String {
    let secs = t.duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
    // Civil date from days since the epoch (Howard Hinnant's algorithm).
    let (days, rem) = (secs.div_euclid(86_400), secs.rem_euclid(86_400));
    let z = days + 719_468;
    let era = z.div_euclid(146_097);
    let doe = z.rem_euclid(146_097);
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = yoe + era * 400 + if m <= 2 { 1 } else { 0 };
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", rem / 3600, rem % 3600 / 60, rem % 60)
}

fn digests_of(m: &Manifest) -> BTreeMap<String, String> {
    m.files.iter().filter(|(n, _)| BOOT_FILES.contains(&n.as_str())).map(|(n, f)| (n.clone(), f.sha256.clone())).collect()
}

fn file_sha256(path: &Path) -> Result<String, String> {
    let mut f = fs::File::open(path).map_err(|e| e.to_string())?;
    let mut h = Sha256::new();
    std::io::copy(&mut f, &mut h).map_err(|e| e.to_string())?;
    Ok(hex::encode(h.finalize()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_helper_is_left_running_until_it_says_stopped() {
        let dir = std::env::temp_dir().join(format!("maitre-nb-{}", std::process::id()));
        fs::create_dir_all(&dir).unwrap();
        let events = dir.join("events.jsonl");
        assert_eq!(left_running(&events), None, "no file");
        fs::write(&events, "{\"event\":\"ready\",\"ip\":\"192.168.4.54\",\"http_port\":38123}\n{\"event\":\"client\",\"mac\":\"aa\"}\n").unwrap();
        assert_eq!(left_running(&events).as_deref(), Some("192.168.4.54:38123"));
        fs::write(&events, "{\"event\":\"ready\",\"ip\":\"192.168.4.54\",\"http_port\":38123}\n{\"event\":\"stopped\",\"reason\":\"requested\"}\n").unwrap();
        assert_eq!(left_running(&events), None);
        fs::write(&events, "{\"event\":\"error\",\"message\":\"tftp on udp/69: bind: address already in use\"}\n").unwrap();
        assert_eq!(left_running(&events), None, "one that failed to start is not running");
        let _ = fs::remove_dir_all(&dir);
    }

    #[test]
    fn a_bundle_with_ipxe_boots_bios_machines() {
        let data = std::env::temp_dir().join(format!("maitre-nb-bios-{}", std::process::id()));
        let dir = Netboot::boot_dir(&data);
        fs::create_dir_all(&dir).unwrap();
        let entry = |n: usize| format!("{{\"size\":{n},\"sha256\":\"{}\"}}", "0".repeat(64));
        let files: Vec<String> = BOOT_FILES.iter().map(|f| format!("\"{f}\":{}", entry(f.len()))).collect();
        for f in BOOT_FILES {
            fs::write(dir.join(f), f).unwrap();
        }
        // An older Depot's manifest: no optional files.
        fs::write(dir.join(MANIFEST), format!("{{\"image\":\"x/y:z\",\"files\":{{{}}}}}", files.join(","))).unwrap();
        let st = Netboot::files_status(&data);
        assert!(st.ready && !st.bios);
        assert_eq!(st.digests.len(), BOOT_FILES.len());
        assert_eq!(st.digests["vmlinuz"], "0".repeat(64));
        assert!(st.version.is_empty() && st.built.is_empty() && !st.downloaded.is_empty(), "an older bundle: {} {} {}", st.version, st.built, st.downloaded);
        fs::write(dir.join(MANIFEST), format!("{{\"image\":\"x/y:z\",\"version\":\"abc1234\",\"built\":\"2026-10-31T19:26:12Z\",\"files\":{{{}}}}}", files.join(","))).unwrap();
        let st = Netboot::files_status(&data);
        assert_eq!((st.version.as_str(), st.built.as_str()), ("abc1234", "2026-10-31T19:26:12Z"));
        fs::write(dir.join(MANIFEST), format!("{{\"image\":\"x/y:z\",\"files\":{{{}}},\"optional\":{{\"undionly.kpxe\":{},\"future.bin\":{}}}}}", files.join(","), entry(4), entry(1))).unwrap();
        assert!(!Netboot::files_status(&data).bios, "listed but not downloaded");
        fs::write(dir.join(BIOS_FILE), "ipxe").unwrap();
        let st = Netboot::files_status(&data);
        assert!(st.ready && st.bios);
        assert_eq!(st.total_bytes, BOOT_FILES.iter().map(|f| f.len() as u64).sum::<u64>(), "optional files are not counted as required");
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn file_times_are_rfc3339() {
        let t = std::time::UNIX_EPOCH + std::time::Duration::from_secs(1_793_474_772);
        assert_eq!(rfc3339(t), "2026-10-31T19:26:12Z");
        assert_eq!(rfc3339(std::time::UNIX_EPOCH), "1970-01-01T00:00:00Z");
        assert_eq!(rfc3339(std::time::UNIX_EPOCH + std::time::Duration::from_secs(951_782_400)), "2000-02-29T00:00:00Z");
    }

    #[test]
    fn a_taken_port_is_explained() {
        assert!(explain("tftp on udp/69: listen udp4 192.168.4.54:69: bind: address already in use (run with sudo?)").contains("Something else on this computer"));
        assert_eq!(explain("no LAN address"), "no LAN address");
    }
}
