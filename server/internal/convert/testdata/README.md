# Conversion corpus

Each case's `input/` holds Compose files exactly as their projects publish
them; `want/` is the bundle `maitre catalog add` makes of them (without the
copied `compose.yaml`). Regenerate `want/` with
`go test ./internal/convert -update` and review the diff.

| Case | Source | Licence |
|---|---|---|
| immich | https://github.com/immich-app/immich/releases/download/v3.3.5/docker-compose.yml and example.env (as `.env`) | AGPL-3.0 |
| paperless-ngx | https://github.com/paperless-ngx/paperless-ngx/tree/main/docker/compose: docker-compose.postgres.yml, docker-compose.env | GPL-3.0 |
| authentik | https://goauthentik.io/docker-compose.yml | MIT |
| casaos-jellyfin | https://github.com/IceWhaleTech/CasaOS-AppStore/blob/main/Apps/Jellyfin/docker-compose.yml | Apache-2.0 |
| build, escaping | written for these tests | as this repository |
