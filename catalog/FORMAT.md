# Manifest format

`manifest.yaml`, one per entry:

```yaml
id: jellyfin                 # [a-z0-9-], unique, also the Service's default name
name: Jellyfin
summary: one line shown in lists
description: |
  longer text shown on the entry page
icon: https://.../icon.svg
category: media              # media | home | network | tools | infrastructure
image: docker.io/jellyfin/jellyfin:10.10.7@sha256:...   # tag for humans, digest for machines
version: "10.10.7"
exposure: remote             # default exposure level: private | remote
own_login: false             # true if the app has its own accounts (changes Console wording)
infrastructure: false        # DHCP/DNS style: reserved address, early start
host_ports:                  # ports the app must own on the Node itself
  - 53/udp
web:                         # the page the Console opens; omit for headless apps
  port: 8096
  path: /
volumes:                     # named data dirs; placed at <pool>/<service>/<name>
  - name: config
    path: /config
  - name: media
    path: /media
    size: large              # small | medium | large: hint for Tier choice
settings:                    # asked at install, available to the template as {{ .Settings.KEY }}
  - key: TZ
    label: Time zone
    type: timezone           # text | number | password | timezone | bool
    default: auto
health:
  http: /health              # or `cmd: ...`
```

`<id>.container` is a Podman Quadlet `[Container]` unit with Go template
fields. The agent supplies `.Image`, `.Name`, `.Volumes` (name → host path),
`.Settings`, `.Network` and `.Env`. Entries should keep the template short
and let the agent add `[Service]`/`[Install]` sections.
