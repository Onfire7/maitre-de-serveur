//! Finds Installers and Nodes on the local network with mDNS/DNS-SD. The
//! browser runs for the life of the app; the UI asks for a snapshot.

use mdns_sd::{ServiceDaemon, ServiceEvent};
use serde::Serialize;
use std::collections::BTreeMap;
use std::net::IpAddr;
use std::sync::{Arc, Mutex};

pub const TYPE_INSTALLER: &str = "_maitre-installer._tcp.local.";
pub const TYPE_NODE: &str = "_maitre-node._tcp.local.";
/// Developer Depots announce themselves so the Console can list every one
/// on the network and warn when more than one is talking (decision 96).
pub const TYPE_DEPOT: &str = "_maitre-depot._tcp.local.";
/// A Console asking to join a Fleet (decision 99) announces itself with its key.
pub const TYPE_DEVICE: &str = "_maitre-device._tcp.local.";

#[derive(Serialize, Clone, Debug)]
pub struct Found {
    /// "installer", "node" or "depot".
    pub kind: String,
    /// Key fingerprint from the TXT record; what the Console pins.
    pub id: String,
    pub name: String,
    pub machine: String,
    pub fleet_id: String,
    pub version: String,
    /// host:port candidates, IPv4 first.
    pub addresses: Vec<String>,
    /// Devices: their public key, PEM. Nodes: "1" while a recovery window is open.
    #[serde(default)]
    pub key_pem: String,
    #[serde(default)]
    pub recovery: bool,
}

#[derive(Clone)]
pub struct Discovery {
    found: Arc<Mutex<BTreeMap<String, Found>>>,
    #[allow(dead_code)] // kept alive so the browser keeps running; stop() is for tests and shutdown
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
        for ty in [TYPE_INSTALLER, TYPE_NODE, TYPE_DEPOT, TYPE_DEVICE] {
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
                            let kind = kind_of(&r.ty_domain);
                            found.lock().unwrap().insert(
                                format!("{kind}:{id}"),
                                Found {
                                    kind: kind.into(),
                                    id,
                                    name: txt("name"),
                                    machine: txt("machine"),
                                    fleet_id: txt("fleet"),
                                    version: txt("v"),
                                    key_pem: {
                                        let k = txt("key");
                                        if k.is_empty() { String::new() } else { crate::identity::public_key_pem_of_b64(&k) }
                                    },
                                    recovery: txt("recovery") == "1",
                                    addresses: v4,
                                },
                            );
                        }
                        ServiceEvent::ServiceRemoved(ty, fullname) => {
                            let kind = kind_of(&ty);
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

    /// Announce this Console as a device asking to join (decision 99). The
    /// key travels as base64 SPKI in TXT. Returns the registered name.
    pub fn announce_device(&self, id: &str, name: &str, key_raw: &[u8]) -> Result<String, String> {
        let daemon = self.daemon.as_ref().ok_or("mDNS is not available")?;
        let host = format!("{}.local.", std::fs::read_to_string("/etc/hostname").map(|h| h.trim().to_string()).ok().filter(|h| !h.is_empty()).unwrap_or_else(|| "maitre-desktop".into()));
        let instance = format!("{} ({})", name, &id[..8.min(id.len())]);
        let props = [("id", id.to_string()), ("name", name.to_string()), ("key", crate::identity::b64_of_raw_key(key_raw))];
        let info = mdns_sd::ServiceInfo::new(TYPE_DEVICE, &instance, &host, "", 7442, &props[..])
            .map_err(|e| e.to_string())?
            .enable_addr_auto();
        daemon.register(info).map_err(|e| e.to_string())?;
        Ok(format!("{instance}.{TYPE_DEVICE}"))
    }

    pub fn withdraw(&self, fullname: &str) {
        if let Some(d) = &self.daemon {
            let _ = d.unregister(fullname);
        }
    }

    pub fn snapshot(&self) -> Vec<Found> {
        self.found.lock().unwrap().values().cloned().collect()
    }

    #[allow(dead_code)]
    pub fn stop(&self) {
        if let Some(d) = &self.daemon {
            let _ = d.shutdown();
        }
    }
}

fn kind_of(ty: &str) -> &'static str {
    if ty.starts_with("_maitre-installer") {
        "installer"
    } else if ty.starts_with("_maitre-depot") {
        "depot"
    } else if ty.starts_with("_maitre-device") {
        "device"
    } else {
        "node"
    }
}
