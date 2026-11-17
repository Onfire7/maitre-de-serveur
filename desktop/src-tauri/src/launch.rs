//! Open in your terminal (ADR 0011): run `maitre terminal-bridge` in the
//! person's own terminal app, attached to a Node-held Terminal through the
//! loopback proxy. The proxy's one-time token reaches the bridge in a
//! ticket file only this user can read, which the bridge removes as it
//! reads it, never on a command line other programs can see.
//!
//! Which terminal app: on Linux `xdg-terminal-exec`, else the first of a
//! short list found on PATH; on Windows, Windows Terminal, else conhost; on
//! macOS, whichever app opens a `.command` script (Terminal unless the
//! person chose another). Only the Linux path is exercised by a real run so
//! far; the macOS and Windows paths are untested.

use std::path::{Path, PathBuf};

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Os {
    Linux,
    MacOs,
    Windows,
}

impl Os {
    pub fn current() -> Os {
        if cfg!(target_os = "macos") {
            Os::MacOs
        } else if cfg!(windows) {
            Os::Windows
        } else {
            Os::Linux
        }
    }
}

/// What the Console says when there is no terminal app to open.
pub const NO_TERMINAL: &str = "No terminal app was found on this computer. Install one, such as GNOME Terminal, Konsole or xterm, and try again; the Terminal is still open here.";

/// A terminal app and how to run a command in it. On macOS the command is
/// a script written first (`script`: where, what), which `open` runs in the
/// default terminal app.
#[derive(Debug, PartialEq, Eq)]
pub struct Plan {
    pub program: String,
    pub args: Vec<String>,
    pub script: Option<(PathBuf, String)>,
}

/// Linux terminal apps tried after `xdg-terminal-exec`, each with what goes
/// before the command it should run.
const LINUX: &[(&str, &[&str])] = &[
    ("x-terminal-emulator", &["-e"]),
    ("gnome-terminal", &["--"]),
    ("konsole", &["-e"]),
    ("xfce4-terminal", &["-x"]),
    ("mate-terminal", &["-x"]),
    ("kitty", &[]),
    ("alacritty", &["-e"]),
    ("foot", &[]),
    ("wezterm", &["start", "--"]),
    ("xterm", &["-e"]),
];

/// Which terminal app runs the bridge, and how, given which programs this
/// computer has (`found`: a program name on PATH). `Err` says, in the
/// Console's words, that there is none.
pub fn choose(os: Os, found: &dyn Fn(&str) -> bool, bridge: &Path, ticket: &Path) -> Result<Plan, String> {
    let command = vec![bridge.display().to_string(), "terminal-bridge".into(), "--ticket".into(), ticket.display().to_string()];
    let plan = |program: &str, before: &[&str]| Plan {
        program: program.into(),
        args: before.iter().map(|s| s.to_string()).chain(command.iter().cloned()).collect(),
        script: None,
    };
    match os {
        Os::Linux => {
            if found("xdg-terminal-exec") {
                return Ok(plan("xdg-terminal-exec", &[]));
            }
            LINUX.iter().find(|(p, _)| found(p)).map(|(p, before)| plan(p, before)).ok_or_else(|| NO_TERMINAL.into())
        }
        Os::Windows => {
            if found("wt.exe") {
                Ok(plan("wt.exe", &["new-tab", "--"]))
            } else if found("conhost.exe") {
                Ok(plan("conhost.exe", &[]))
            } else {
                Err(NO_TERMINAL.into())
            }
        }
        Os::MacOs => {
            let script = ticket.with_extension("command");
            let quoted: Vec<String> = command.iter().map(|a| sh_quote(a)).collect();
            let body = format!("#!/bin/sh\nrm -f {}\nexec {}\n", sh_quote(&script.display().to_string()), quoted.join(" "));
            Ok(Plan { program: "open".into(), args: vec![script.display().to_string()], script: Some((script, body)) })
        }
    }
}

fn sh_quote(s: &str) -> String {
    format!("'{}'", s.replace('\'', r"'\''"))
}

/// Whether `program` is an executable on PATH.
pub fn on_path(program: &str) -> bool {
    let Some(path) = std::env::var_os("PATH") else { return false };
    std::env::split_paths(&path).any(|d| is_executable(&d.join(program)))
}

#[cfg(unix)]
fn is_executable(p: &Path) -> bool {
    use std::os::unix::fs::PermissionsExt;
    p.metadata().map(|m| m.is_file() && m.permissions().mode() & 0o111 != 0).unwrap_or(false)
}

#[cfg(not(unix))]
fn is_executable(p: &Path) -> bool {
    p.is_file()
}

/// Writes the bridge's ticket (the proxy's URL with its one-time token, and
/// the Terminal to attach to) in `dir`, readable only by this user, and
/// removes tickets no bridge took within a minute: their token has expired.
pub fn write_ticket(dir: &Path, url: &str, terminal: &str) -> Result<PathBuf, String> {
    create_private_dir(dir).map_err(|e| e.to_string())?;
    if let Ok(entries) = std::fs::read_dir(dir) {
        for e in entries.flatten() {
            let old = e.metadata().and_then(|m| m.modified()).ok().and_then(|t| t.elapsed().ok()).map(|d| d.as_secs() > 60).unwrap_or(false);
            if old {
                let _ = std::fs::remove_file(e.path());
            }
        }
    }
    let name: String = (0..16).map(|_| format!("{:x}", rand::random::<u8>() & 0xf)).collect();
    let path = dir.join(format!("ticket-{name}"));
    let body = serde_json::json!({ "url": url, "terminal": terminal }).to_string();
    write_private(&path, body.as_bytes()).map_err(|e| e.to_string())?;
    Ok(path)
}

#[cfg(unix)]
fn create_private_dir(dir: &Path) -> std::io::Result<()> {
    use std::os::unix::fs::{DirBuilderExt, PermissionsExt};
    std::fs::DirBuilder::new().recursive(true).mode(0o700).create(dir)?;
    std::fs::set_permissions(dir, std::fs::Permissions::from_mode(0o700))
}

#[cfg(not(unix))]
fn create_private_dir(dir: &Path) -> std::io::Result<()> {
    std::fs::create_dir_all(dir)
}

#[cfg(unix)]
fn write_private(path: &Path, body: &[u8]) -> std::io::Result<()> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    std::fs::OpenOptions::new().write(true).create_new(true).mode(0o600).open(path)?.write_all(body)
}

#[cfg(not(unix))]
fn write_private(path: &Path, body: &[u8]) -> std::io::Result<()> {
    std::fs::write(path, body)
}

/// Open in your terminal: write the ticket, choose the terminal app and run
/// the bridge in it. Returns once the app is started; `Err` is for the
/// Console (NO_TERMINAL when there is none).
pub fn open(tickets: &Path, bridge: &Path, url: &str, terminal: &str) -> Result<(), String> {
    let ticket = write_ticket(tickets, url, terminal)?;
    let os = Os::current();
    let found = |p: &str| on_path(p) || (os == Os::Windows && p == "conhost.exe");
    let plan = match choose(os, &found, bridge, &ticket) {
        Ok(p) => p,
        Err(e) => {
            let _ = std::fs::remove_file(&ticket);
            return Err(e);
        }
    };
    if let Some((path, body)) = &plan.script {
        write_private(path, body.as_bytes()).map_err(|e| e.to_string())?;
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            std::fs::set_permissions(path, std::fs::Permissions::from_mode(0o700)).map_err(|e| e.to_string())?;
        }
    }
    let mut cmd = std::process::Command::new(&plan.program);
    cmd.args(&plan.args).stdin(std::process::Stdio::null()).stdout(std::process::Stdio::null()).stderr(std::process::Stdio::null());
    match cmd.spawn() {
        // Reap it in the background: terminal apps often return at once.
        Ok(mut child) => {
            std::thread::spawn(move || child.wait());
            Ok(())
        }
        Err(e) => {
            let _ = std::fs::remove_file(&ticket);
            Err(format!("{} could not be started: {e}", plan.program))
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn has(names: &'static [&'static str]) -> impl Fn(&str) -> bool {
        move |p| names.contains(&p)
    }

    fn line(p: &Plan) -> String {
        format!("{} {}", p.program, p.args.join(" "))
    }

    const BRIDGE: &str = "/opt/maitre/maitre";
    const TICKET: &str = "/home/me/.local/share/maitre/tickets/ticket-ab";

    #[test]
    fn linux_prefers_xdg_terminal_exec() {
        let p = choose(Os::Linux, &has(&["xdg-terminal-exec", "gnome-terminal", "xterm"]), Path::new(BRIDGE), Path::new(TICKET)).unwrap();
        assert_eq!(line(&p), format!("xdg-terminal-exec {BRIDGE} terminal-bridge --ticket {TICKET}"));
        assert_eq!(p.script, None);
    }

    #[test]
    fn linux_falls_back_to_the_first_common_terminal_found() {
        let p = choose(Os::Linux, &has(&["xterm", "gnome-terminal"]), Path::new(BRIDGE), Path::new(TICKET)).unwrap();
        assert_eq!(line(&p), format!("gnome-terminal -- {BRIDGE} terminal-bridge --ticket {TICKET}"));
        let p = choose(Os::Linux, &has(&["xterm", "konsole"]), Path::new(BRIDGE), Path::new(TICKET)).unwrap();
        assert_eq!(line(&p), format!("konsole -e {BRIDGE} terminal-bridge --ticket {TICKET}"));
        let p = choose(Os::Linux, &has(&["xterm"]), Path::new(BRIDGE), Path::new(TICKET)).unwrap();
        assert_eq!(line(&p), format!("xterm -e {BRIDGE} terminal-bridge --ticket {TICKET}"));
    }

    #[test]
    fn no_terminal_app_is_said_plainly() {
        let e = choose(Os::Linux, &has(&[]), Path::new(BRIDGE), Path::new(TICKET)).unwrap_err();
        assert!(e.starts_with("No terminal app was found on this computer."), "{e}");
        let e = choose(Os::Windows, &has(&[]), Path::new(BRIDGE), Path::new(TICKET)).unwrap_err();
        assert_eq!(e, NO_TERMINAL);
    }

    #[test]
    fn windows_prefers_windows_terminal_then_conhost() {
        let (bridge, ticket) = (r"C:\Program Files\Maitre\maitre.exe", r"C:\Users\me\AppData\maitre\tickets\ticket-ab");
        let p = choose(Os::Windows, &has(&["wt.exe", "conhost.exe"]), Path::new(bridge), Path::new(ticket)).unwrap();
        assert_eq!(p.program, "wt.exe");
        assert_eq!(p.args, vec!["new-tab", "--", bridge, "terminal-bridge", "--ticket", ticket]);
        let p = choose(Os::Windows, &has(&["conhost.exe"]), Path::new(bridge), Path::new(ticket)).unwrap();
        assert_eq!(p.program, "conhost.exe");
        assert_eq!(p.args, vec![bridge, "terminal-bridge", "--ticket", ticket]);
    }

    #[test]
    fn macos_opens_a_script_in_the_default_terminal_app() {
        let bridge = "/Applications/Maître Desktop.app/Contents/MacOS/maitre";
        let p = choose(Os::MacOs, &has(&[]), Path::new(bridge), Path::new(TICKET)).unwrap();
        let script = format!("{TICKET}.command");
        assert_eq!(line(&p), format!("open {script}"));
        let (at, body) = p.script.unwrap();
        assert_eq!(at, PathBuf::from(&script));
        assert_eq!(
            body,
            format!("#!/bin/sh\nrm -f '{script}'\nexec '/Applications/Maître Desktop.app/Contents/MacOS/maitre' 'terminal-bridge' '--ticket' '{TICKET}'\n")
        );
    }

    #[test]
    fn a_ticket_is_private_and_names_the_terminal() {
        let dir = std::env::temp_dir().join(format!("maitre-tickets-{}", std::process::id()));
        let p = write_ticket(&dir, "ws://127.0.0.1:4000/abc", "0123456789abcdef").unwrap();
        let v: serde_json::Value = serde_json::from_slice(&std::fs::read(&p).unwrap()).unwrap();
        assert_eq!(v, serde_json::json!({"url": "ws://127.0.0.1:4000/abc", "terminal": "0123456789abcdef"}));
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            assert_eq!(std::fs::metadata(&p).unwrap().permissions().mode() & 0o777, 0o600);
            assert_eq!(std::fs::metadata(&dir).unwrap().permissions().mode() & 0o777, 0o700);
        }
        std::fs::remove_dir_all(&dir).unwrap();
    }
}
