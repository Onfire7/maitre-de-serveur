# Conversion of Immich

From `docker-compose.yml`, by `maitre catalog add`.

## Needs a person

The entry is unfinished until these are decided and its `unfinished:` list is removed.

- Write the entry's summary and description

## Changed for you

Done differently from the Compose file; check they suit the app.

- database: container_name "immich_postgres" is dropped; Maître names the container, and other containers reach it as "database"
- database: the folder ./postgres is the Service volume "postgres", kept in a Storage Space
- immich-machine-learning: container_name "immich_machine_learning" is dropped; Maître names the container, and other containers reach it as "immich-machine-learning"
- immich-machine-learning: the volume model-cache is the Service volume "model_cache", kept in a Storage Space
- immich-server: container_name "immich_server" is dropped; Maître names the container, and other containers reach it as "immich-server"
- immich-server: the folder ./library is the Service volume "library", kept in a Storage Space
- immich-server: the host's /etc/localtime is replaced by the TZ setting, the Node's time zone unless changed
- redis: container_name "immich_redis" is dropped; Maître names the container, and other containers reach it as "redis"
- restart: policies are replaced by Maître's: each container restarts when it fails
- DB_PASSWORD is made up at install, instead of "postgres"
