#!/bin/sh
# Explicit versioned schema migration, using the SAME version as the server.
set -eu
for db in temporal temporal_visibility; do
  schema=temporal
  if [ "$db" = temporal_visibility ]; then schema=visibility; fi
  temporal-sql-tool --plugin postgres12 --ep postgres --port 5432 --user temporal \
    --database "$db" setup-schema --version 0.0
  temporal-sql-tool --plugin postgres12 --ep postgres --port 5432 --user temporal \
    --database "$db" update-schema --schema-dir "/etc/temporal/schema/postgresql/v12/$schema/versioned"
done
