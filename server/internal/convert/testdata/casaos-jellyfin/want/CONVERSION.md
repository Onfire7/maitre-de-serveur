# Conversion of Jellyfin

From `docker-compose.yml`, by `maitre catalog add`.

## Needs a person

The entry is unfinished until these are decided and its `unfinished:` list is removed.

- jellyfin mounts the Node's own folder /opt/vc/lib; point it at a Service volume, or keep it and allow it when installing
- jellyfin asks for AddDevice=/dev/dri:/dev/dri:rwm, which a Trusted Device must allow at install; keep it only if the app needs it, and declare it under grants: with why (optional: if the app works without it)
- jellyfin asks for AddDevice=/dev/vchiq:/dev/vchiq:rwm, which Maître does not support yet (it needs SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in); find another way, or leave the entry out
- jellyfin asks for AddDevice=/dev/vcsm:/dev/vcsm:rwm, which Maître does not support yet (it needs SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in); find another way, or leave the entry out
- jellyfin asks for AddDevice=/dev/video10:/dev/video10:rwm, which Maître does not support yet (it needs SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in); find another way, or leave the entry out
- jellyfin asks for AddDevice=/dev/video11:/dev/video11:rwm, which Maître does not support yet (it needs SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in); find another way, or leave the entry out
- jellyfin asks for AddDevice=/dev/video12:/dev/video12:rwm, which Maître does not support yet (it needs SELinux letting a container use the device (container-selinux's container_use_devices boolean, off on Fedora, or a narrower rule) set in the Node image, checked on a Node with the device plugged in); find another way, or leave the entry out

## Changed for you

Done differently from the Compose file; check they suit the app.

- jellyfin: deploy.resources reservations (MemoryLow=) are dropped; Maître sets no reservations
- jellyfin: container_name "jellyfin" is dropped; Maître names the container, and other containers reach it as "jellyfin"
- jellyfin: CasaOS's /DATA/AppData/input/config is the Service volume "config", kept in a Storage Space
- jellyfin: CasaOS's shared /DATA/Media is the shared volume "media"; the user picks the folder at install
- restart: policies are replaced by Maître's: each container restarts when it fails
- PGID, which CasaOS sets itself, defaults to "1000"
- PUID, which CasaOS sets itself, defaults to "1000"
