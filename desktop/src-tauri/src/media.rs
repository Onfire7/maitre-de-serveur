//! "Prepare an installer USB" (Milestone 7): download the raw installer
//! image, list removable disks through the bundled helper, write the image
//! elevated with progress, the same way netboot runs its helper.

use futures_util::StreamExt;
use serde::{Deserialize, Serialize};
use std::fs;
use std::io::{BufRead, BufReader, Seek, SeekFrom};
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::{Arc, Mutex};
use std::time::Duration;
use tauri::{AppHandle, Emitter};

pub const RELEASE_IMAGE: &str = "https://github.com/Onfire7/maitre-de-serveur/releases/latest/download/maitre-installer.img.xz";
const FILE: &str = "maitre-installer.img.xz";

#[derive(Serialize, Deserialize, Clone)]
pub struct Disk {
    pub device: String,
    pub model: String,
    pub size_bytes: u64,
    pub removable: bool,
    pub transport: String,
    pub in_use: bool,
}

#[derive(Serialize, Clone)]
pub struct ImageStatus {
    pub path: String,
    pub ready: bool,
    pub size_bytes: u64,
}

#[derive(Serialize, Clone, Default)]
pub struct WriteStatus {
    pub running: bool,
    pub done: u64,
    pub total: u64,
    pub finished: bool,
    pub error: String,
}

#[derive(Default)]
pub struct Media {
    status: Arc<Mutex<WriteStatus>>,
}

impl Media {
    fn image_path(data_dir: &Path) -> PathBuf {
        data_dir.join("media").join(FILE)
    }

    pub fn image_status(data_dir: &Path) -> ImageStatus {
        let p = Self::image_path(data_dir);
        let size = fs::metadata(&p).map(|m| m.len()).unwrap_or(0);
        ImageStatus { path: p.to_string_lossy().into_owned(), ready: size > 0, size_bytes: size }
    }

    /// Download the image from `url` (empty: the latest release), emitting `media-progress`.
    pub async fn download(app: AppHandle, data_dir: PathBuf, url: String) -> Result<ImageStatus, String> {
        let url = if url.is_empty() { RELEASE_IMAGE.to_string() } else { url };
        let dir = data_dir.join("media");
        fs::create_dir_all(&dir).map_err(|e| e.to_string())?;
        let client = reqwest::Client::builder().build().map_err(|e| e.to_string())?;
        let res = client.get(&url).send().await.map_err(|e| e.to_string())?;
        if res.status() == reqwest::StatusCode::NOT_FOUND {
            return Err("No Maître release has been published yet, so there is no installer image to download.".into());
        }
        let res = res.error_for_status().map_err(|e| e.to_string())?;
        let total = res.content_length().unwrap_or(0);
        let tmp = dir.join(format!("{FILE}.part"));
        let mut out = fs::File::create(&tmp).map_err(|e| e.to_string())?;
        let mut done = 0u64;
        let mut stream = res.bytes_stream();
        let mut last = std::time::Instant::now();
        while let Some(chunk) = stream.next().await {
            let chunk = chunk.map_err(|e| e.to_string())?;
            std::io::Write::write_all(&mut out, &chunk).map_err(|e| e.to_string())?;
            done += chunk.len() as u64;
            if last.elapsed() > Duration::from_millis(200) {
                let _ = app.emit("media-progress", WriteStatus { running: true, done, total, finished: false, error: String::new() });
                last = std::time::Instant::now();
            }
        }
        drop(out);
        fs::rename(&tmp, Self::image_path(&data_dir)).map_err(|e| e.to_string())?;
        Ok(Self::image_status(&data_dir))
    }

    /// Removable disks, through the helper (no elevation needed to list).
    pub fn list(helper: &Path) -> Result<Vec<Disk>, String> {
        let out = Command::new(helper).args(["media", "list"]).output().map_err(|e| e.to_string())?;
        if !out.status.success() {
            return Err(String::from_utf8_lossy(&out.stderr).into_owned());
        }
        serde_json::from_slice(&out.stdout).map_err(|e| e.to_string())
    }

    /// Write the image to `device`, elevated; progress arrives as `media-progress`.
    pub fn write(&self, app: AppHandle, data_dir: &Path, helper: &Path, device: String, elevate: impl Fn(&Path, &[String]) -> Result<std::process::Child, String>) -> Result<(), String> {
        if self.status.lock().unwrap().running {
            return Err("a write is already running".into());
        }
        let image = Self::image_path(data_dir);
        if !image.exists() {
            return Err("download the installer image first".into());
        }
        let events = data_dir.join("media").join("events.jsonl");
        let _ = fs::remove_file(&events);
        let args: Vec<String> = vec![
            "media".into(), "write".into(),
            "--image".into(), image.to_string_lossy().into_owned(),
            "--device".into(), device,
            "--events-file".into(), events.to_string_lossy().into_owned(),
        ];
        let mut child = elevate(helper, &args)?;
        *self.status.lock().unwrap() = WriteStatus { running: true, ..Default::default() };
        let status = self.status.clone();
        std::thread::spawn(move || {
            let mut offset = 0u64;
            let started = std::time::Instant::now();
            loop {
                if let Ok(mut f) = fs::File::open(&events) {
                    let _ = f.seek(SeekFrom::Start(offset));
                    let mut reader = BufReader::new(f);
                    let mut line = String::new();
                    while let Ok(n) = reader.read_line(&mut line) {
                        if n == 0 {
                            break;
                        }
                        offset += n as u64;
                        if let Ok(v) = serde_json::from_str::<serde_json::Value>(line.trim()) {
                            let kind = v.get("event").and_then(|e| e.as_str()).unwrap_or("");
                            let mut st = status.lock().unwrap();
                            match kind {
                                "progress" | "start" => {
                                    st.done = v.get("done").and_then(|x| x.as_u64()).unwrap_or(st.done);
                                    st.total = v.get("total").and_then(|x| x.as_u64()).unwrap_or(st.total);
                                }
                                "done" => {
                                    st.done = v.get("done").and_then(|x| x.as_u64()).unwrap_or(st.done);
                                    st.running = false;
                                    st.finished = true;
                                }
                                "error" => {
                                    st.error = v.get("message").and_then(|x| x.as_str()).unwrap_or("").into();
                                    st.running = false;
                                }
                                _ => {}
                            }
                            let _ = app.emit("media-progress", st.clone());
                            if !st.running {
                                return;
                            }
                        }
                        line.clear();
                    }
                }
                if let Ok(Some(code)) = child.try_wait() {
                    let mut st = status.lock().unwrap();
                    if st.running {
                        st.running = false;
                        if !code.success() && st.error.is_empty() {
                            st.error = format!("the helper exited with {code} (was the password prompt cancelled?)");
                        }
                        let _ = app.emit("media-progress", st.clone());
                    }
                    return;
                }
                if started.elapsed() > Duration::from_secs(1800) {
                    let mut st = status.lock().unwrap();
                    st.running = false;
                    st.error = "timed out".into();
                    let _ = app.emit("media-progress", st.clone());
                    return;
                }
                std::thread::sleep(Duration::from_millis(400));
            }
        });
        Ok(())
    }

    pub fn status(&self) -> WriteStatus {
        self.status.lock().unwrap().clone()
    }
}
