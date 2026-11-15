# Volumes

Docker named volumes are declared in the Compose files.
`deploy/scripts/backup-postgres.sh` reads the attachment and extension volumes
through a one-off container and stores them, next to the database dump, in the
encrypted archive; `restore-postgres.sh` writes them back into the same
volumes. Production backups should be written outside containers, under the
host path configured by `SFORUM_BACKUP_DIR` or `./backups` by default.

Runtime volumes store PostgreSQL, Redis, Meilisearch, uploaded attachments, and
installed extension packages. Public themes use package assets plus the Page
Registry; admin components load directly from immutable digest endpoints, so no
frontend release artifact volume is required.
