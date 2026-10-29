# Conversion of Authentik

From `docker-compose.yml`, by `maitre catalog add`.

## Needs a person

The entry is unfinished until these are decided and its `unfinished:` list is removed.

- The web page is taken to be server's port 9000, of 2 published ports; confirm web.component and web.port
- worker mounts the Node's own folder /var/run/docker.sock; point it at a Service volume, or keep it and allow it when installing
- Write the entry's summary and description

## Changed for you

Done differently from the Compose file; check they suit the app.

- postgresql: the volume database is the Service volume "database", kept in a Storage Space
- server: the folder ./data is the Service volume "data", kept in a Storage Space
- server: the folder ./custom-templates is the Service volume "custom_templates", kept in a Storage Space
- worker: the folder ./data is the Service volume "data", kept in a Storage Space
- worker: the folder ./certs is the Service volume "certs", kept in a Storage Space
- worker: the folder ./custom-templates is the Service volume "custom_templates", kept in a Storage Space
- server waits for postgresql's health check through Notify=healthy, instead of compose2quadlet's polling loop, which would run on the Node
- restart: policies are replaced by Maître's: each container restarts when it fails
- AUTHENTIK_SECRET_KEY is made up at install
- PG_PASS is made up at install

## Notes

- .env is not beside the Compose file; it is read as empty, and its variables become settings
