# Maître Catalog

The curated list of Services a Maître Fleet can install. MIT licensed so
anyone can fork, add entries, or run their own Catalog. This directory will
become its own repository; it lives in the monorepo until then.

## Layout

```
entries/<id>/manifest.yaml   what the Console shows and asks; see FORMAT.md
entries/<id>/<id>.container  Podman Quadlet template the agent renders
index.json                   generated: `maitre catalog-index entries > index.json`
```

Images are pinned by digest in the manifest. Updating an entry means
changing the digest; the agent's Service auto-update follows the index.
