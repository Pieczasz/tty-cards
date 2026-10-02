#!/usr/bin/env bash
# Postgres backup for terminal-card. Run from the repo root (next to compose.yaml).
#
# Prereq: zstd on the host  ->  sudo apt-get install -y zstd
#
# Cron (daily 03:30, keep 14 days):
#   30 3 * * * cd /srv/terminal-card && ./scripts/backup.sh >> backups/backup.log 2>&1
#
# Restore:
#   zstd -dc backups/terminal_card-YYYYMMDD-HHMMSS.sql.zst | \
#     docker compose exec -T db psql -U "$DB_USER" "$DB_NAME"
set -euo pipefail

# A dump is the whole user base, keys included: readable by the owner only.
umask 077

command -v zstd >/dev/null || {
	echo "zstd not found; install it (e.g. sudo apt-get install -y zstd)" >&2
	exit 1
}

# Read only the keys this script uses, as data. Sourcing .env would run any shell in
# it, as whoever runs this cron job, and import DB_PASSWORD into the environment.
# The environment wins over .env, and .env over the defaults below.
env_value() {
	[ -f .env ] || return 0
	sed -n "s/^[[:space:]]*$1=//p" .env | tail -n 1 | sed -e "s/^[\"']//" -e "s/[\"'][[:space:]]*\$//"
}

DB_USER="${DB_USER:-$(env_value DB_USER)}"
DB_NAME="${DB_NAME:-$(env_value DB_NAME)}"
DB_USER="${DB_USER:-postgres}"
DB_NAME="${DB_NAME:-terminal_card}"
BACKUP_DIR="${BACKUP_DIR:-backups}"
RETENTION_DAYS="${RETENTION_DAYS:-14}"

mkdir -p "$BACKUP_DIR"
out="$BACKUP_DIR/terminal_card-$(date +%Y%m%d-%H%M%S).sql.zst"

# Written under a name the retention sweep and a restore both ignore, and renamed
# only once pg_dump and zstd have both succeeded: a failed run leaves no file that
# looks like a good backup.
trap 'rm -f "$out.part"' EXIT
docker compose exec -T db pg_dump -U "$DB_USER" "$DB_NAME" | zstd -q -T0 -o "$out.part"
mv "$out.part" "$out"
echo "wrote $out"

find "$BACKUP_DIR" -name 'terminal_card-*.sql.zst' -mtime +"$RETENTION_DAYS" -delete
