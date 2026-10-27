//! Downloads and uploads for the file manager (decisions 180, 183, 185).
//! Bytes stream between the disk and the Node over mutual TLS here, never
//! through the webview; progress goes to the Console as `transfer` events.

use bytes::Bytes;
use futures_util::StreamExt;
use serde::Serialize;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
use tauri::{AppHandle, Emitter};
use tokio::io::{AsyncReadExt, AsyncWriteExt};

const CONTENT: &str = "/maitre/files/content";

/// Where on a Node: a Space's Pool or a Drive's root.
#[derive(serde::Deserialize, Clone)]
pub struct Scope {
    #[serde(default)]
    pub space: String,
    #[serde(default)]
    pub drive: String,
}

#[derive(Serialize, Clone)]
struct Progress {
    id: String,
    done: u64,
    total: u64,
    current: String,
}

/// What an upload did with each local file.
#[derive(Serialize, Default)]
pub struct UploadResult {
    pub uploaded: u32,
    /// Names already in the folder; never replaced (decision 185).
    pub skipped: Vec<String>,
    pub failed: Vec<String>,
}

fn emit(app: &AppHandle, id: &str, done: u64, total: u64, current: &str) {
    let _ = app.emit("transfer", Progress { id: id.into(), done, total, current: current.into() });
}

/// Save a file, or a folder as a zip, from the Node to `dest`. Written to
/// `dest.part` first so a broken download never looks finished.
pub async fn download(app: AppHandle, client: reqwest::Client, addr: String, scope: Scope, path: String, folder: bool, dest: String, id: String) -> Result<(), String> {
    let mut q = vec![("space", scope.space), ("drive", scope.drive), ("path", path), ("download", "1".into())];
    if folder {
        q.push(("zip", "1".into()));
    }
    let res = client.get(format!("https://{addr}{CONTENT}")).query(&q).send().await.map_err(|e| e.to_string())?;
    if !res.status().is_success() {
        let code = res.status();
        return Err(format!("{code}: {}", res.text().await.unwrap_or_default().trim()));
    }
    let total = res.content_length().unwrap_or(0);
    let part = PathBuf::from(format!("{dest}.part"));
    let mut out = tokio::fs::File::create(&part).await.map_err(|e| e.to_string())?;
    let mut stream = res.bytes_stream();
    let (mut done, mut last) = (0u64, Instant::now());
    while let Some(chunk) = stream.next().await {
        let chunk = match chunk {
            Ok(c) => c,
            Err(e) => {
                let _ = tokio::fs::remove_file(&part).await;
                return Err(e.to_string());
            }
        };
        out.write_all(&chunk).await.map_err(|e| e.to_string())?;
        done += chunk.len() as u64;
        if last.elapsed() > Duration::from_millis(250) {
            emit(&app, &id, done, total, &dest);
            last = Instant::now();
        }
    }
    out.flush().await.map_err(|e| e.to_string())?;
    drop(out);
    tokio::fs::rename(&part, &dest).await.map_err(|e| e.to_string())?;
    emit(&app, &id, done, done, &dest);
    Ok(())
}

/// Upload local files and folders into `folder` of a scope. Folders keep
/// their layout; hidden files stay behind, as the Node refuses them.
pub async fn upload(app: AppHandle, client: reqwest::Client, addr: String, scope: Scope, folder: String, paths: Vec<String>, id: String) -> Result<UploadResult, String> {
    // (local file, path on the Node, size)
    let mut files: Vec<(PathBuf, String, u64)> = Vec::new();
    for p in &paths {
        let p = PathBuf::from(p);
        let name = p.file_name().map(|n| n.to_string_lossy().to_string()).unwrap_or_default();
        collect(&p, &join(&folder, &name), &mut files);
    }
    let total: u64 = files.iter().map(|f| f.2).sum();
    let done = Arc::new(AtomicU64::new(0));
    let mut result = UploadResult::default();
    for (local, remote, _) in files {
        let current = remote.clone();
        let file = match tokio::fs::File::open(&local).await {
            Ok(f) => f,
            Err(_) => {
                result.failed.push(remote);
                continue;
            }
        };
        let modified = std::fs::metadata(&local)
            .and_then(|m| m.modified())
            .ok()
            .map(|t| chrono_rfc3339(t))
            .unwrap_or_default();
        let (app2, id2, done2) = (app.clone(), id.clone(), done.clone());
        let last = Arc::new(std::sync::Mutex::new(Instant::now()));
        let body = futures_util::stream::unfold(file, |mut f| async move {
            let mut buf = vec![0u8; 256 * 1024];
            match f.read(&mut buf).await {
                Ok(0) => None,
                Ok(n) => {
                    buf.truncate(n);
                    Some((Ok::<Bytes, std::io::Error>(Bytes::from(buf)), f))
                }
                Err(e) => Some((Err(e), f)),
            }
        })
        .map(move |chunk| {
            if let Ok(c) = &chunk {
                let d = done2.fetch_add(c.len() as u64, Ordering::Relaxed) + c.len() as u64;
                let mut l = last.lock().unwrap();
                if l.elapsed() > Duration::from_millis(250) {
                    emit(&app2, &id2, d, total, &current);
                    *l = Instant::now();
                }
            }
            chunk
        });
        let q = [("space", scope.space.as_str()), ("drive", scope.drive.as_str()), ("path", remote.as_str()), ("mkdirs", "1"), ("modified", modified.as_str())];
        match client.put(format!("https://{addr}{CONTENT}")).query(&q).body(reqwest::Body::wrap_stream(body)).send().await {
            Ok(res) if res.status() == reqwest::StatusCode::CONFLICT => result.skipped.push(remote),
            Ok(res) if res.status().is_success() => result.uploaded += 1,
            _ => result.failed.push(remote),
        }
    }
    let d = done.load(Ordering::Relaxed);
    emit(&app, &id, d.max(total), total, "");
    Ok(result)
}

fn collect(local: &Path, remote: &str, out: &mut Vec<(PathBuf, String, u64)>) {
    let hidden = local.file_name().map(|n| n.to_string_lossy().starts_with('.')).unwrap_or(false);
    if hidden {
        return;
    }
    let Ok(meta) = std::fs::metadata(local) else { return };
    if meta.is_dir() {
        let Ok(entries) = std::fs::read_dir(local) else { return };
        let mut entries: Vec<_> = entries.flatten().collect();
        entries.sort_by_key(|e| e.file_name());
        for e in entries {
            collect(&e.path(), &join(remote, &e.file_name().to_string_lossy()), out);
        }
    } else if meta.is_file() {
        out.push((local.to_path_buf(), remote.to_string(), meta.len()));
    }
}

fn join(a: &str, b: &str) -> String {
    if a.is_empty() { b.to_string() } else { format!("{}/{b}", a.trim_end_matches('/')) }
}

/// RFC 3339 in UTC for a file time, without a date crate.
fn chrono_rfc3339(t: std::time::SystemTime) -> String {
    let secs = t.duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0);
    let (days, rem) = (secs.div_euclid(86400), secs.rem_euclid(86400));
    // Civil from days (Howard Hinnant's algorithm).
    let z = days + 719468;
    let era = z.div_euclid(146097);
    let doe = z - era * 146097;
    let yoe = (doe - doe / 1460 + doe / 36524 - doe / 146096) / 365;
    let y = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let d = doy - (153 * mp + 2) / 5 + 1;
    let m = if mp < 10 { mp + 3 } else { mp - 9 };
    let y = if m <= 2 { y + 1 } else { y };
    format!("{y:04}-{m:02}-{d:02}T{:02}:{:02}:{:02}Z", rem / 3600, rem % 3600 / 60, rem % 60)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rfc3339() {
        let t = std::time::UNIX_EPOCH + Duration::from_secs(1_793_000_000);
        assert_eq!(chrono_rfc3339(t), "2026-10-26T07:33:20Z");
    }

    #[test]
    fn collects_folders_and_skips_hidden() {
        let dir = std::env::temp_dir().join(format!("maitre-collect-{}", std::process::id()));
        std::fs::create_dir_all(dir.join("Shows/S1")).unwrap();
        std::fs::write(dir.join("Shows/S1/e1.mkv"), b"abc").unwrap();
        std::fs::write(dir.join("Shows/.DS_Store"), b"x").unwrap();
        let mut out = Vec::new();
        collect(&dir.join("Shows"), "Media/Shows", &mut out);
        std::fs::remove_dir_all(&dir).unwrap();
        assert_eq!(out.len(), 1);
        assert_eq!(out[0].1, "Media/Shows/S1/e1.mkv");
        assert_eq!(out[0].2, 3);
    }
}
