//! Finds Installers and Nodes on the local network with mDNS/DNS-SD. The
//! browser runs for the life of the app; the UI asks for a snapshot.

use mdns_sd::{ServiceDaemon, ServiceEvent};
use serde::Serialize;
use std::collections::BTreeMap;
use std::net::IpAddr;
use std::sync::{Arc, Mutex};

pub const TYPE_INSTALLER: &str = "_maitre-installer._tcp.local.";
pub const TYPE_NODE: &str = "_maitre-node._tcp.local.";

#[derive(Serialize, Clone, Debug)]
pub struct Found {
    /// "installer" or "node".
    pub kind: String,
    /// Key fingerprint from the TXT record; what the Console pins.
    pub id: String,
    pub name: String,
    pub machine: String,
    pub fleet_id: String,
    pub version: String,
    /// host:port candidates, IPv4 first.
    pub addresses: Vec<String>,
}

#[derive(Clone)]
pub struct Discovery {
    found: Arc<Mutex<BTreeMap<String, Found>>>,
    daemon: Option<ServiceDaemon>,
}

impl Discovery {
    pub fn start() -> Self {
        let found: Arc<Mutex<BTreeMap<String, Found>>> = Arc::default();
        let daemon = match ServiceDaemon::new() {
            Ok(d) => d,
            Err(e) => {
                eprintln!("mdns: {e}; discovery disabled");
                return Self { found, daemon: None };
            }
        };
        for ty in [TYPE_INSTALLER, TYPE_NODE] {
            let rx = match daemon.browse(ty) {
                Ok(rx) => rx,
                Err(e) => {
                    eprintln!("mdns browse {ty}: {e}");
                    continue;
                }
            };
            let found = found.clone();
            std::thread::spawn(move || {
                while let Ok(ev) = rx.recv() {
                    match ev {
                        ServiceEvent::ServiceResolved(r) => {
                            let txt = |k: &str| r.txt_properties.get_property_val_str(k).unwrap_or("").to_string();
                            let id = txt("id");
                            if id.is_empty() {
                                continue;
                            }
                            let mut v4: Vec<String> = Vec::new();
                            let mut v6: Vec<String> = Vec::new();
                            for a in r.addresses.iter() {
                                match a.to_ip_addr() {
                                    IpAddr::V4(ip) => v4.push(format!("{ip}:{}", r.port)),
                                    IpAddr::V6(ip) => {
                                        if !ip.is_unicast_link_local() {
                                            v6.push(format!("[{ip}]:{}", r.port))
                                        }
                                    }
                                }
                            }
                            v4.sort();
                            v6.sort();
                            v4.extend(v6);
                            let kind = if r.ty_domain.starts_with("_maitre-installer") { "installer" } else { "node" };
                            found.lock().unwrap().insert(
                                format!("{kind}:{id}"),
                                Found {
                                    kind: kind.into(),
                                    id,
                                    name: txt("name"),
                                    machine: txt("machine"),
                                    fleet_id: txt("fleet"),
                                    version: txt("v"),
                                    addresses: v4,
                                },
                            );
                        }
                        ServiceEvent::ServiceRemoved(ty, fullname) => {
                            let kind = if ty.starts_with("_maitre-installer") { "installer" } else { "node" };
                            // We keyed by id, not fullname; drop entries whose
                            // short id appears in the instance name "(xxxxxxxx)".
                            let mut f = found.lock().unwrap();
                            f.retain(|k, v| {
                                !(v.kind == kind && k.starts_with(kind) && fullname.contains(&v.id[..v.id.len().min(8)]))
                            });
                        }
                        _ => {}
                    }
                }
            });
        }
        Self { found, daemon: Some(daemon) }
    }

    pub fn snapshot(&self) -> Vec<Found> {
        self.found.lock().unwrap().values().cloned().collect()
    }

    pub fn stop(&self) {
        if let Some(d) = &self.daemon {
            let _ = d.shutdown();
        }
    }
}
