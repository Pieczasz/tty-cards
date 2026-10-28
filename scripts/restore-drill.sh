#!/usr/bin/env bash
# Proves a backup restores, without touching the live stack: the newest archive (or
# the one named) goes into a throwaway Postgres container of the same image the db
# service runs, with no network, and is checked there. Run it on a schedule - an
# untested backup is a hope, not a backup.
#
#   scripts/restore-drill.sh [backups/tty-cards-<stamp>.tar]
#
# Fails unless: the dump restores with no error; schema_migrations is clean and no
# newer than this checkout's migrations; users holds at least DRILL_MIN_USERS rows
# (1 - an empty dump restores cleanly too); and the archive carries the ssh host key.
set -euo pipefail
cd "$(dirname "$0")/.."
umask 077

die() {
	echo "drill: FAIL: $*" >&2
	exit 1
}

dir=${BACKUP_DIR:-backups}
min_users=${DRILL_MIN_USERS:-1}
archive=${1:-$(find "$dir" -maxdepth 1 -name 'tty-cards-*.tar' | sort | tail -n 1)}
[ -f "$archive" ] || die "no archive found in $dir"
echo "drill: $archive"

db=$(docker compose ps -aq db)
[ -n "$db" ] || die "no db container to take the Postgres image from"
image=$(docker inspect --format '{{.Config.Image}}' "$db")

work=$(mktemp -d)
name=tty-cards-drill-$$
trap 'docker rm -f "$name" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT
tar -xf "$archive" -C "$work" db.dump ssh-keys.tar

docker run -d --rm --name "$name" --network none \
	-e POSTGRES_PASSWORD=drill -e POSTGRES_DB=drill "$image" >/dev/null
# Over TCP on purpose: the image's init phase runs a socket-only server first, and
# a socket probe would pass before the real one is up.
for _ in $(seq 60); do
	docker exec "$name" pg_isready -q -h 127.0.0.1 -U postgres && break
	sleep 1
done
docker exec "$name" pg_isready -q -h 127.0.0.1 -U postgres || die "the drill database never came up"

docker exec -i "$name" pg_restore --exit-on-error --no-owner -U postgres -d drill <"$work/db.dump" ||
	die "pg_restore failed"

sql() { docker exec "$name" psql -XAtq -v ON_ERROR_STOP=1 -U postgres -d drill -c "$1"; }

IFS='|' read -r version dirty <<<"$(sql 'SELECT version, dirty FROM schema_migrations')"
latest=$(find internal/db/migrations -name '*.up.sql' | sed 's|.*/0*\([0-9][0-9]*\)_.*|\1|' | sort -n | tail -n 1)
[ "$dirty" = f ] || die "schema_migrations is dirty at version $version"
[ "$version" -le "$latest" ] || die "the dump is at migration $version, newer than this checkout's $latest"
echo "drill: migration $version (checkout at $latest; migrate applies the rest on start)"

for table in $(sql "SELECT tablename FROM pg_tables WHERE schemaname = 'public' ORDER BY 1"); do
	echo "drill: $table $(sql "SELECT count(*) FROM \"$table\"")"
done
users=$(sql 'SELECT count(*) FROM users')
[ "$users" -ge "$min_users" ] || die "users has $users rows, fewer than DRILL_MIN_USERS=$min_users"

tar -xf "$work/ssh-keys.tar" -C "$work" ./server 2>/dev/null && [ -s "$work/server" ] ||
	die "the archive holds no ssh host key"
# The fingerprint players' known_hosts pin, to compare with the live server's.
fingerprint=$(ssh-keygen -lf "$work/server" 2>/dev/null) || die "the archived host key is not a readable private key"
echo "drill: ssh host key $fingerprint"
echo "drill: OK"
