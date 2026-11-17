//! Downloads and uploads for the file manager (decisions 180, 183, 185).
//! Bytes stream between the disk and the Node over mutual TLS here, never
//! through the webview, on a connection from the Dialer (src/dial.rs);
//! progress goes to the Console as `transfer` events (src/lib.rs).

use crate::rpc::{full_body, stream_body, NodeClient};
use bytes::Bytes;
use futures_util::StreamExt;
use http_body_util::BodyExt;
use serde::Serialize;
use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::Arc;
use std::time::{Duration, Instant};
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

/// What an upload did with each local file.
#[derive(Serialize, Default)]
pub struct UploadResult {
    pub uploaded: u32,
    /// Names already in the folder; never replaced (decision 185).
    pub skipped: Vec<String>,
    pub failed: Vec<String>,
}

/// Hears how far a transfer is: bytes done, bytes in all, and the file.
pub type Progress = Arc<dyn Fn(u64, u64, &str) + Send + Sync>;

fn content_uri(q: &[(&str, &str)]) -> String {
    let query = form_urlencoded::Serializer::new(String::new()).extend_pairs(q).finish();
    format!("{CONTENT}?{query}")
}

/// Save a file, or a folder as a zip, from the Node to `dest`. Written to
/// `dest.part` first so a broken download never looks finished.
#[allow(clippy::too_many_arguments)]
pub async fn download(client: &NodeClient, addr: &str, fingerprint: &str, scope: Scope, path: String, folder: bool, dest: String, progress: Progress) -> Result<(), String> {
    let mut q = vec![("space", scope.space.as_str()), ("drive", scope.drive.as_str()), ("path", path.as_str()), ("download", "1")];
    if folder {
        q.push(("zip", "1"));
    }
    let req = hyper::Request::get(content_uri(&q)).body(full_body(Bytes::new())).map_err(|e| e.to_string())?;
    let res = client.send(addr, fingerprint, req).await?;
    if !res.status().is_success() {
        let code = res.status();
        let text = res.into_body().collect().await.map(|b| b.to_bytes()).unwrap_or_default();
        return Err(format!("{code}: {}", String::from_utf8_lossy(&text).trim()));
    }
    let total = res
        .headers()
        .get(hyper::header::CONTENT_LENGTH)
        .and_then(|v| v.to_str().ok())
        .and_then(|v| v.parse().ok())
        .unwrap_or(0u64);
    let part = PathBuf::from(format!("{dest}.part"));
    let mut out = tokio::fs::File::create(&part).await.map_err(|e| e.to_string())?;
    let mut body = res.into_body();
    let (mut done, mut last) = (0u64, Instant::now());
    while let Some(frame) = body.frame().await {
        let frame = match frame {
            Ok(f) => f,
            Err(e) => {
                let _ = tokio::fs::remove_file(&part).await;
                return Err(e.to_string());
            }
        };
        let Ok(chunk) = frame.into_data() else { continue };
        out.write_all(&chunk).await.map_err(|e| e.to_string())?;
        done += chunk.len() as u64;
        if last.elapsed() > Duration::from_millis(250) {
            progress(done, total, &dest);
            last = Instant::now();
        }
    }
    out.flush().await.map_err(|e| e.to_string())?;
    drop(out);
    tokio::fs::rename(&part, &dest).await.map_err(|e| e.to_string())?;
    progress(done, done, &dest);
    Ok(())
}

/// Upload local files and folders into `folder` of a scope. Folders keep
/// their layout; hidden files stay behind, as the Node refuses them.
pub async fn upload(client: &NodeClient, addr: &str, fingerprint: &str, scope: Scope, folder: String, paths: Vec<String>, progress: Progress) -> Result<UploadResult, String> {
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
            .map(chrono_rfc3339)
            .unwrap_or_default();
        let (progress2, done2) = (progress.clone(), done.clone());
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
                    progress2(d, total, &current);
                    *l = Instant::now();
                }
            }
            chunk
        });
        let q = [("space", scope.space.as_str()), ("drive", scope.drive.as_str()), ("path", remote.as_str()), ("mkdirs", "1"), ("modified", modified.as_str())];
        let req = match hyper::Request::put(content_uri(&q)).body(stream_body(body)) {
            Ok(r) => r,
            Err(_) => {
                result.failed.push(remote);
                continue;
            }
        };
        match client.send(addr, fingerprint, req).await {
            Ok(res) if res.status() == hyper::StatusCode::CONFLICT => result.skipped.push(remote),
            Ok(res) if res.status().is_success() => result.uploaded += 1,
            _ => result.failed.push(remote),
        }
    }
    let d = done.load(Ordering::Relaxed);
    progress(d.max(total), total, "");
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
    use crate::dial::{fake, Dialer};
    use crate::rpc::NodeClient;
    use http_body_util::{BodyExt, Full};
    use std::collections::HashMap;
    use std::sync::Mutex;

    /// A Node keeping files in memory: GET returns what the query asked
    /// for, PUT stores the body under its path and refuses a path it has.
    async fn files_node(stored: Arc<Mutex<HashMap<String, String>>>) -> fake::FakeNode {
        fake::start(move |tls| {
            let stored = stored.clone();
            async move {
                let svc = hyper::service::service_fn(move |req: hyper::Request<hyper::body::Incoming>| {
                    let stored = stored.clone();
                    async move {
                        let q: HashMap<String, String> = form_urlencoded::parse(req.uri().query().unwrap_or("").as_bytes()).into_owned().collect();
                        let path = q.get("path").cloned().unwrap_or_default();
                        let res = if req.method() == hyper::Method::GET {
                            let mut keys: Vec<_> = q.iter().map(|(k, v)| format!("{k}={v}")).collect();
                            keys.sort();
                            hyper::Response::new(Full::new(Bytes::from(keys.join("&"))))
                        } else if stored.lock().unwrap().contains_key(&path) {
                            hyper::Response::builder().status(409).body(Full::new(Bytes::new())).unwrap()
                        } else {
                            let body = req.into_body().collect().await?.to_bytes();
                            stored.lock().unwrap().insert(path, String::from_utf8_lossy(&body).into_owned());
                            hyper::Response::new(Full::new(Bytes::new()))
                        };
                        Ok::<_, hyper::Error>(res)
                    }
                });
                let _ = hyper::server::conn::http2::Builder::new(hyper_util::rt::TokioExecutor::new())
                    .serve_connection(hyper_util::rt::TokioIo::new(tls), svc)
                    .await;
            }
        })
        .await
    }

    fn quiet() -> Progress {
        Arc::new(|_, _, _| {})
    }

    #[tokio::test]
    async fn a_download_is_saved_whole_at_its_destination() {
        let node = files_node(Arc::default()).await;
        let client = NodeClient::new(Arc::new(Dialer::new(fake::temp_identity("device"))));
        let dir = std::env::temp_dir().join(format!("maitre-dl-{:x}", rand::random::<u64>()));
        std::fs::create_dir_all(&dir).unwrap();
        let dest = dir.join("Shows.zip").to_string_lossy().into_owned();
        let scope = Scope { space: "media".into(), drive: String::new() };
        let seen = Arc::new(Mutex::new(Vec::new()));
        let s2 = seen.clone();
        let progress: Progress = Arc::new(move |done, total, _| s2.lock().unwrap().push((done, total)));
        download(&client, &node.addr, &node.fingerprint, scope, "Media/Shows & more".into(), true, dest.clone(), progress).await.unwrap();
        let got = std::fs::read_to_string(&dest).unwrap();
        assert_eq!(got, "download=1&drive=&path=Media/Shows & more&space=media&zip=1");
        assert!(!dir.join("Shows.zip.part").exists());
        assert_eq!(seen.lock().unwrap().last(), Some(&(got.len() as u64, got.len() as u64)));
        std::fs::remove_dir_all(&dir).unwrap();
    }

    #[tokio::test]
    async fn an_upload_sends_each_file_and_skips_names_already_there() {
        let stored: Arc<Mutex<HashMap<String, String>>> = Arc::default();
        stored.lock().unwrap().insert("Media/Shows/old.mkv".into(), "kept".into());
        let node = files_node(stored.clone()).await;
        let client = NodeClient::new(Arc::new(Dialer::new(fake::temp_identity("device"))));
        let dir = std::env::temp_dir().join(format!("maitre-ul-{:x}", rand::random::<u64>()));
        std::fs::create_dir_all(dir.join("Shows/S1")).unwrap();
        std::fs::write(dir.join("Shows/S1/e1.mkv"), b"episode one").unwrap();
        std::fs::write(dir.join("Shows/old.mkv"), b"new").unwrap();
        let scope = Scope { space: "media".into(), drive: String::new() };
        let paths = vec![dir.join("Shows").to_string_lossy().into_owned()];
        let r = upload(&client, &node.addr, &node.fingerprint, scope, "Media".into(), paths, quiet()).await.unwrap();
        std::fs::remove_dir_all(&dir).unwrap();
        assert_eq!((r.uploaded, r.skipped.clone(), r.failed.clone()), (1, vec!["Media/Shows/old.mkv".to_string()], vec![]));
        let stored = stored.lock().unwrap();
        assert_eq!(stored.get("Media/Shows/S1/e1.mkv").map(String::as_str), Some("episode one"));
        assert_eq!(stored.get("Media/Shows/old.mkv").map(String::as_str), Some("kept"));
    }

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
