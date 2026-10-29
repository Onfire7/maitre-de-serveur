# Catalog entries that need features

Entries that declare grants (docs/popularity-and-grants.md), the ones changed to avoid needing one, and the apps waiting on support Maître does not have yet. Kept up to date as entries are added.

## Easiest to test first: ESPHome

ESPHome exercises an optional grant with the least in the way:

- one small container, no account to create, no hardware needed;
- its one feature, `discovery` (`Network=host`), changes what you can see from outside:
  - **on**: the page answers at `http://<node address>:6052/` directly as well as at `esphome.local`, and the unit has `Network=host` and no `PublishPort=`;
  - **off**: only `esphome.local` answers, the unit has Maître's network and a loopback `PublishPort=`, and `ESPHOME_DASHBOARD_USE_PING=true`;
- the Console's question shows the reason and the switch, and "Install without discovery" signs no grant.

To test it on a Node: install it twice under two names, once with discovery allowed and once without, and compare `/etc/containers/systemd/<name>.container`, `ss -ltnp | grep 6052`, and the grants listed in the Service's menu. A second Service with discovery on must be refused, since port 6052 is taken.

Next easiest: Home Assistant (the same single feature, but a 1.5 GB image and an owner account to create), then Emby (a device setting and a GPU, but no account outside the Node), then Plex (two features, a Plex account, and a GPU for the second).

## Entries with optional grants

| Entry | Feature | Grant | Without it |
|---|---|---|---|
| ESPHome | discovery | `Network=host` | Devices checked by ping, updated by address |
| Home Assistant | discovery | `Network=host` | Devices added by address; HomeKit and self-discovering integrations do not work |
| Plex | discovery | `Network=host` | Apps find the server through the Plex account; claim it with a token |
| Plex | hardware_transcoding | `AddDevice=` the GPU picked (device setting, `video`) | The processor transcodes |
| Emby | hardware_transcoding | `AddDevice=` the GPU picked (device setting, `video`) | The processor transcodes |

No entry has a required grant yet.

## Entries changed to need no grant

| Entry | What the source asked for | What the entry does instead |
|---|---|---|
| Node-RED | `network_mode: host` | Its own network; it reaches MQTT and Home Assistant by address |
| Homarr | The Docker socket | Dropped; its container view does not work |
| Plex | `/dev/dvb` (TV tuners) | Dropped until other devices are supported |
| Mosquitto | A config file the user writes | The image's own `/mosquitto-no-auth.conf`; no passwords |
| Ollama | AMD GPU devices (`/dev/kfd`, `/dev/dri`) | Runs on the processor |
| Emby | Raspberry Pi devices (`/dev/vchiq`, `/dev/video1x`) and its HTTPS port | Dropped; Maître serves the page |
| Heimdall | Port 443 of the Node | Dropped; the Node's web server holds it |
| Open WebUI | The NVIDIA runtime (CasaOS's file, `GlobalArgs=--runtime nvidia`) | Converted from Big Bear's file instead, which has none |

## Waiting on support

Each of these fails `catalog-check` with what Maître needs: SELinux work to be checked on a real Node, except NVIDIA, which needs a design of its own:

| Needs | Apps |
|---|---|
| Serial devices (`container_use_devices`, off on Fedora, or a narrower rule) | Zigbee2MQTT, Z-Wave JS UI, Home Assistant's radio sticks, ESPHome flashing over USB from the Node |
| `/dev/net/tun` (the same rule) | WireGuard, Tailscale, Gluetun |
| `/dev/kfd` (the same rule) | Ollama on AMD GPUs |
| NVIDIA's container runtime or CDI devices (a design, and drivers in the image) | Ollama, Open WebUI, Jellyfin, Plex and Immich on NVIDIA GPUs |
| The Podman socket (SELinux without `label=disable`) | Dozzle, Portainer, Homarr's and Homepage's container views |
| Bluetooth over the system bus (BlueZ in the image, and SELinux to the bus) | Home Assistant's Bluetooth |

## Left out for other reasons

| App | Why |
|---|---|
| Kiwix | Exits with no `.zim` file, so it would fail its health check until someone adds content; needs a way to start with a library to download |
| Kopia (Big Bear's) | Its image serves HTTPS on its own port, which Maître's proxy cannot reach; it also asks for SYS_ADMIN, the Docker socket and Docker's volumes |
| SABnzbd | Refuses requests by hostnames not in its whitelist, which is how Maître's proxy reaches it, until its settings are changed |
| Firefly (CasaOS) | Not Firefly III: a VPN app needing `/dev/net/tun` |
