#!/bin/bash
# bootstrap.sh — one-time Postgres bootstrap for the prism production
# deployment. Runs against the host's existing Postgres (the one
# agents-pay-service already uses).
#
# What it does:
#   1. Creates a Postgres user `prism` (idempotent — re-running is safe)
#   2. Creates a database `prism` owned by that user (idempotent)
#   3. Grants schema privileges
#   4. Verifies the new user can connect to the new database
#
# What it does NOT do:
#   • Create any tables — prism creates those itself on first start
#     via golang-migrate (see aperturedb/postgres.go).
#   • Edit pg_hba.conf or postgresql.conf — those are operator
#     judgement calls (which subnets to allow, listen_addresses, etc).
#     See README.md "Postgres — accept connections from docker bridge".
#
# Usage:
#   PRISM_DB_PASSWORD='strong-pw' sudo -E ./bootstrap.sh
#       Reads the password from env. -E preserves the env var across
#       sudo (otherwise the variable is stripped).
#
#   sudo ./bootstrap.sh
#       Prompts interactively for the password (input is hidden).
#
# Re-running is safe — existing user has its password reset, existing
# database is left as-is.

set -euo pipefail

DB_NAME="${DB_NAME:-prism}"
DB_USER="${DB_USER:-prism}"

if [ "${EUID}" -ne 0 ] && [ -z "${PGUSER:-}" ]; then
    echo "Run as root via sudo (so we can su - postgres) or set PGUSER" >&2
    echo "to a Postgres superuser account." >&2
    exit 1
fi

# Pick the password. Env var preferred (scriptable, no terminal echo).
if [ -z "${PRISM_DB_PASSWORD:-}" ]; then
    echo -n "Password for the new Postgres user '$DB_USER': "
    read -rs PRISM_DB_PASSWORD
    echo
    if [ -z "$PRISM_DB_PASSWORD" ]; then
        echo "Empty password rejected." >&2
        exit 1
    fi
fi

# psql wrapper — by default reach Postgres as the system 'postgres'
# superuser via local socket (the standard Debian / Ubuntu setup).
# Override with PGUSER + PGHOST + .pgpass for remote / non-default
# installs.
if [ -n "${PGUSER:-}" ]; then
    PSQL=(psql)
else
    PSQL=(sudo -u postgres psql)
fi

run_sql() {
    "${PSQL[@]}" -v ON_ERROR_STOP=1 -X -q "$@"
}

echo "→ Bootstrapping Postgres for prism (db=$DB_NAME, user=$DB_USER)"

# 1. User: create or reset password.
echo "  • ensuring user '$DB_USER' exists"
run_sql <<SQL
DO \$\$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_catalog.pg_roles WHERE rolname = '$DB_USER') THEN
        ALTER USER "$DB_USER" WITH PASSWORD '$PRISM_DB_PASSWORD';
        RAISE NOTICE 'User % already existed — password reset', '$DB_USER';
    ELSE
        CREATE USER "$DB_USER" WITH PASSWORD '$PRISM_DB_PASSWORD';
        RAISE NOTICE 'User % created', '$DB_USER';
    END IF;
END
\$\$;
SQL

# 2. Database: create if absent. Can't go inside a DO block — CREATE
#    DATABASE forbids running in a transaction.
DB_EXISTS=$(run_sql -tAc "SELECT 1 FROM pg_database WHERE datname = '$DB_NAME'" || true)
if [ "$DB_EXISTS" = "1" ]; then
    echo "  • database '$DB_NAME' already exists — leaving as is"
else
    echo "  • creating database '$DB_NAME' owned by '$DB_USER'"
    run_sql -c "CREATE DATABASE \"$DB_NAME\" OWNER \"$DB_USER\" ENCODING 'UTF8'"
fi

# 3. Schema privileges. PG 15+ revokes public CREATE on the public
#    schema by default, so the owner needs an explicit grant to be
#    able to create the migration tables on first prism start.
echo "  • granting schema privileges on public to '$DB_USER'"
run_sql -d "$DB_NAME" <<SQL
GRANT ALL ON SCHEMA public TO "$DB_USER";
SQL

# 4. Verify the new credentials actually work.
echo "  • verifying '$DB_USER' can log in to '$DB_NAME'"
PGPASSWORD="$PRISM_DB_PASSWORD" psql \
    -h "${PGHOST:-127.0.0.1}" \
    -U "$DB_USER" \
    -d "$DB_NAME" \
    -tAc "SELECT 'connected as ' || current_user || ' to ' || current_database()"

echo
echo "✓ Done. Next steps:"
echo "    1. Make sure pg_hba.conf allows the docker bridge subnet"
echo "       to authenticate as '$DB_USER' (see README.md §1.2)."
echo "    2. cp prism.yaml.example prism.yaml and set"
echo "       postgres.password to the value you just used."
echo "    3. docker compose up -d --build"
