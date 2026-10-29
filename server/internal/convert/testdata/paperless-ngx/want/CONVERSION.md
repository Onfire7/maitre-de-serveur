# Conversion of Paperless Ngx

From `docker-compose.postgres.yml`, by `maitre catalog add`.

## Needs a person

The entry is unfinished until these are decided and its `unfinished:` list is removed.

- db: POSTGRES_PASSWORD is written into the file; make it a setting (default: generate) unless the app relies on this value
- Write the entry's summary and description

## Changed for you

Done differently from the Compose file; check they suit the app.

- webserver's PAPERLESS_SECRET_KEY is a placeholder ("change-me"); it is the setting PAPERLESS_SECRET_KEY, made up at install
- broker: the volume redisdata is the Service volume "redisdata", kept in a Storage Space
- db: the volume pgdata is the Service volume "pgdata", kept in a Storage Space
- webserver: the volume data is the Service volume "data", kept in a Storage Space
- webserver: the volume media is the Service volume "media", kept in a Storage Space
- webserver: the folder ./export is the Service volume "export", kept in a Storage Space
- webserver: the folder ./consume is the Service volume "consume", kept in a Storage Space
- restart: policies are replaced by Maître's: each container restarts when it fails
