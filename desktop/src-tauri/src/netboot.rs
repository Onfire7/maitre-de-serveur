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

#[derive(Serialize, Deserialize, Clone)]
pub struct Manifest {
    #[serde(default)]
    pub image: String,
    #[serde(default)]
    pub kernel: String,
    pub files: BTreeMap<String, ManifestFile>,
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
    pub last_error: String,
    pub events: Vec<serde_json::Value>,
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
}

impl Netboot {
    fn boot_dir(data_dir: &Path) -> PathBuf {
        data_dir.join("netboot")
    }

    pub fn files_status(data_dir: &Path) -> FilesStatus {
        let dir = Self::boot_dir(data_dir);
        let manifest: Option<Manifest> = fs::read(dir.join(MANIFEST)).ok().and_then(|b| serde_json::from_slice(&b).ok());
        let mut st = FilesStatus { dir: dir.to_string_lossy().into_owned(), image: String::new(), ready: false, have_bytes: 0, total_bytes: 0, missing: vec![] };
        let Some(m) = manifest else {
            return st;
        };
        st.image = m.image.clone();
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
        st
    }

    /// Download the bundle from `base` (ending in `/`) into the data dir,
    /// verifying every file against the manifest. Emits `netboot-progress`.
    pub async fn download(app: AppHandle, data_dir: PathBuf, base: String) -> Result<FilesStatus, String> {
        let dir = Self::boot_dir(&data_dir);
        fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        let client = reqwest::Client::builder().build().map_err(|e| e.to_string())?;
        let raw = client.get(format!("{base}{MANIFEST}")).send().await.map_err(|e| e.to_string())?.error_for_status().map_err(|e| e.to_string())?.bytes().await.map_err(|e| e.to_string())?;
        let manifest: Manifest = serde_json::from_slice(&raw).map_err(|e| format!("netboot.json: {e}"))?;
        if let Some(bad) = manifest.files.keys().find(|n| !BOOT_FILES.contains(&n.as_str())) {
            return Err(format!("netboot.json names an unexpected file {bad:?}; refusing this bundle"));
        }
        if !manifest.files.is_empty() && manifest.files.len() != BOOT_FILES.len() {
            return Err("netboot.json is missing boot files; refusing this bundle".into());
        }
        if manifest.files.values().any(|f| f.sha256.len() != 64 || !f.sha256.bytes().all(|b| b.is_ascii_hexdigit())) {
            return Err("netboot.json has a malformed digest; refusing this bundle".into());
        }
        let emit = |p: Progress| {
            let _ = app.emit("netboot-progress", p);
        };
        for (name, f) in &manifest.files {
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
    fn helper_path() -> Result<PathBuf, String> {
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

    pub fn start(&self, app: AppHandle, data_dir: &Path, opts: StartOptions) -> Result<(), String> {
        if self.child.lock().unwrap().as_mut().map(|c| c.try_wait().ok().flatten().is_none()).unwrap_or(false) {
            return Err("netboot is already running".into());
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

    pub fn status(&self) -> Status {
        let mut st = self.status.lock().unwrap().clone();
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
fn elevated(helper: &Path, args: &[String]) -> Result<Child, String> {
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
                            }
                            "client" => {
                                let mac = v.get("mac").and_then(|x| x.as_str()).unwrap_or("").to_string();
                                if !mac.is_empty() && !st.clients.contains(&mac) {
                                    st.clients.push(mac);
                                }
                            }
                            "error" => st.last_error = v.get("message").and_then(|x| x.as_str()).unwrap_or("").into(),
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

fn file_sha256(path: &Path) -> Result<String, String> {
    let mut f = fs::File::open(path).map_err(|e| e.to_string())?;
    let mut h = Sha256::new();
    std::io::copy(&mut f, &mut h).map_err(|e| e.to_string())?;
    Ok(hex::encode(h.finalize()))
}
