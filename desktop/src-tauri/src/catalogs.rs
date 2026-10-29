//! User Catalogs (plan C9): the Console reads a Catalog itself, through the
//! bundled maitre helper. A folder Catalog is read where it is; a git Catalog
//! is cloned into this Console's data folder and kept at its branch's head.
//! The helper runs only `catalog-index --lenient`, `catalog sync` and
//! `catalog add`, with the paths given here.

use serde::{Deserialize, Serialize};
use std::path::{Path, PathBuf};
use std::process::Command;

#[derive(Deserialize)]
pub struct Source {
    pub id: String,
    #[serde(default)]
    pub folder: String,
    #[serde(default)]
    pub git_url: String,
    #[serde(default)]
    pub git_branch: String,
}

#[derive(Serialize)]
pub struct Loaded {
    /// The Catalog's index, with `problems` for entries that do not load.
    pub index: serde_json::Value,
    /// The commit a git Catalog is at; empty for a folder.
    pub commit: String,
    /// Where the Catalog was read from on this computer.
    pub dir: String,
}

fn valid_id(id: &str) -> bool {
    !id.is_empty() && id.len() <= 40 && id.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
}

fn run(helper: &Path, args: &[&str]) -> Result<String, String> {
    let out = Command::new(helper).args(args).output().map_err(|e| e.to_string())?;
    if !out.status.success() {
        let err = String::from_utf8_lossy(&out.stderr).trim().trim_start_matches("maitre: ").to_string();
        return Err(if err.is_empty() { format!("maitre {} failed", args[0]) } else { err });
    }
    Ok(String::from_utf8_lossy(&out.stdout).into_owned())
}

/// A Catalog folder holds its entries in `entries/`, or is that folder itself.
fn entries_dir(dir: &Path) -> PathBuf {
    let e = dir.join("entries");
    if e.is_dir() { e } else { dir.to_path_buf() }
}

/// Read a Catalog: a folder as it is, a git repository after syncing it.
pub fn load(helper: &Path, data_dir: &Path, src: &Source) -> Result<Loaded, String> {
    if !valid_id(&src.id) {
        return Err(format!("catalog id {:?}", src.id));
    }
    let (dir, commit) = if !src.git_url.is_empty() {
        let dir = data_dir.join("catalogs").join(&src.id);
        let dir_s = dir.to_string_lossy().into_owned();
        let mut args = vec!["catalog", "sync"];
        if !src.git_branch.is_empty() {
            args.extend(["--branch", src.git_branch.as_str()]);
        }
        args.extend(["--", src.git_url.as_str(), dir_s.as_str()]);
        let commit = run(helper, &args)?.trim().to_string();
        (dir, commit)
    } else if !src.folder.is_empty() {
        let dir = PathBuf::from(&src.folder);
        if !dir.is_dir() {
            return Err(format!("{} is not a folder on this computer", src.folder));
        }
        (dir, String::new())
    } else {
        return Err("a Catalog needs a folder or a git repository".into());
    };
    let entries = entries_dir(&dir);
    let raw = run(helper, &["catalog-index", "--lenient", "--", &entries.to_string_lossy()])?;
    let index = serde_json::from_str(&raw).map_err(|e| e.to_string())?;
    Ok(Loaded { index, commit, dir: dir.to_string_lossy().into_owned() })
}

/// Convert a Compose file into a folder Catalog (plan C10): the report and
/// entry as JSON, whether or not a bundle was written.
pub fn add(helper: &Path, folder: &str, compose: &str) -> Result<serde_json::Value, String> {
    if !Path::new(folder).is_dir() {
        return Err(format!("{folder} is not a folder on this computer"));
    }
    let out = Command::new(helper).args(["catalog", "add", "--json", "--", folder, compose]).output().map_err(|e| e.to_string())?;
    // A conversion with things to fix exits non-zero but still prints its report.
    let stdout = String::from_utf8_lossy(&out.stdout);
    if let Ok(v) = serde_json::from_str::<serde_json::Value>(&stdout) {
        return Ok(v);
    }
    Err(String::from_utf8_lossy(&out.stderr).trim().trim_start_matches("maitre: ").to_string())
}

#[cfg(all(test, unix))]
mod tests {
    use super::*;
    use std::os::unix::fs::PermissionsExt;

    /// A stand-in helper that logs its arguments and answers as maitre would.
    fn helper(dir: &Path) -> PathBuf {
        let p = dir.join("maitre");
        let log = dir.join("args.log");
        std::fs::write(&p, format!("#!/bin/sh\necho \"$@\" >> {}\ncase \"$1\" in\n  catalog-index) echo '{{\"format\":1,\"entries\":[]}}' ;;\n  catalog) echo 0123abcd ;;\nesac\n", log.display())).unwrap();
        std::fs::set_permissions(&p, std::fs::Permissions::from_mode(0o755)).unwrap();
        p
    }

    fn tmp(name: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("maitre-catalogs-{name}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[test]
    fn git_catalog_is_synced_into_the_data_folder_then_indexed() {
        let d = tmp("git");
        let h = helper(&d);
        let src = Source { id: "extras".into(), folder: String::new(), git_url: "https://example.invalid/c.git".into(), git_branch: "main".into() };
        let loaded = load(&h, &d, &src).unwrap();
        assert_eq!(loaded.commit, "0123abcd");
        let log = std::fs::read_to_string(d.join("args.log")).unwrap();
        let clone = d.join("catalogs").join("extras");
        assert!(log.contains(&format!("catalog sync --branch main -- https://example.invalid/c.git {}", clone.display())), "{log}");
        assert!(log.contains(&format!("catalog-index --lenient -- {}", clone.display())), "{log}");
    }

    #[test]
    fn folder_catalog_reads_its_entries_folder() {
        let d = tmp("folder");
        let h = helper(&d);
        let cat = d.join("mine");
        std::fs::create_dir_all(cat.join("entries")).unwrap();
        load(&h, &d, &Source { id: "mine".into(), folder: cat.to_string_lossy().into(), git_url: String::new(), git_branch: String::new() }).unwrap();
        let log = std::fs::read_to_string(d.join("args.log")).unwrap();
        assert!(log.contains(&format!("catalog-index --lenient -- {}", cat.join("entries").display())), "{log}");
    }

    #[test]
    fn ids_are_folder_names() {
        let d = tmp("ids");
        let h = helper(&d);
        for id in ["", "../x", "A", "a/b"] {
            assert!(load(&h, &d, &Source { id: id.into(), folder: d.to_string_lossy().into(), git_url: String::new(), git_branch: String::new() }).is_err(), "{id}");
        }
    }
}
