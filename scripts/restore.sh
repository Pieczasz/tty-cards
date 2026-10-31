#!/usr/bin/env bash
# Restores a scripts/backup.sh archive into this compose stack: the dump is restored
# into a new empty database that then replaces the live one, and the ssh host key is
# put back into the backend's volume. Works on a fresh host (nothing started yet) and
# on a running one. Destructive: it asks first unless given -y.
#
#   scripts/restore.sh [-y] backups/tty-cards-<stamp>.tar
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077

die() {
	echo "restore: $*" >&2
	exit 1
}

yes=
[ "${1:-}" = -y ] && { yes=1; shift; }
archive=${1:-}
[ -f "$archive" ] || die "usage: scripts/restore.sh [-y] <backup.tar>"

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
tar -xf "$archive" -C "$work" db.dump ssh-keys.tar

if [ -z "$yes" ]; then
	printf 'This replaces the database and the ssh host key with %s.\nType "restore" to continue: ' "$archive"
	read -r answer
	[ "$answer" = restore ] || die "aborted"
fi

# Nothing may write while the database is replaced. --wait: dropdb below needs the
# server accepting connections, not just its container started.
docker compose stop backend
docker compose up -d --wait db

# A fresh database rather than pg_restore --clean over the old one: --clean drops
# only what the dump contains, so a table from a newer migration would survive and
# make the next `migrate up` fail. It is restored beside the live one and swapped
# in only once pg_restore succeeds, so a truncated or corrupt dump leaves the live
# database as it was. template1, not postgres, so DB_NAME=postgres works too.
# POSTGRES_* are the db container's own, so no password is handled here.
docker compose exec -T db sh -euc '
	dropdb --if-exists --maintenance-db=template1 -U "$POSTGRES_USER" "${POSTGRES_DB}_restore"
	createdb --maintenance-db=template1 -U "$POSTGRES_USER" "${POSTGRES_DB}_restore"'
docker compose exec -T db sh -c \
	'exec pg_restore --exit-on-error --no-owner -U "$POSTGRES_USER" -d "${POSTGRES_DB}_restore"' <"$work/db.dump" ||
	die "pg_restore failed; the live database is untouched and the backend stopped (docker compose up -d)"
docker compose exec -T db sh -euc '
	dropdb --if-exists --force --maintenance-db=template1 -U "$POSTGRES_USER" "$POSTGRES_DB"
	psql -Xq -v ON_ERROR_STOP=1 -d template1 -U "$POSTGRES_USER" \
		-c "ALTER DATABASE \"${POSTGRES_DB}_restore\" RENAME TO \"$POSTGRES_DB\""'

# The key goes in through a created-but-stopped backend: its image has no shell, and
# docker cp writes into the container's volumes without starting it - into the
# volume itself only, since the rest of that filesystem is read-only. -a keeps the
# owner (the image's nonroot user) the archive recorded.
docker compose create backend
docker cp -a - "$(docker compose ps -aq backend):/data/ssh" <"$work/ssh-keys.tar"

# migrate runs first, as on every start, and applies anything newer than the dump.
docker compose up -d
echo "restored $archive"
