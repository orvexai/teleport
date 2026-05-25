# CloudNativePG Postgres image + the wal2json logical-decoding plugin, which
# Teleport's pgbk backend requires for its change feed
# (lib/backend/pgbk/background.go: pg_create_logical_replication_slot(_, 'wal2json')).
# The stock CNPG image doesn't ship it. Used by the teleport + teleport-dev CNPG
# clusters (spec.imageName).
FROM ghcr.io/cloudnative-pg/postgresql:18.1-system-trixie

USER root
RUN apt-get update \
    && apt-get install -y --no-install-recommends postgresql-18-wal2json \
    && rm -rf /var/lib/apt/lists/*
# Back to the CNPG postgres uid.
USER 26
