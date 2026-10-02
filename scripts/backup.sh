#!/usr/bin/env bash
# Backs up what a new host cannot recreate: the database, and the ssh host key the
# backend presents (lose it and every returning player gets a host-key-changed
# warning, which is indistinguishable from an attack). Run against the running
# compose stack; scheduling, restore and the drill are in README.md "Backups".
#
#   BACKUP_DIR     where archives go, relative to the repo root   (backups)
#   BACKUP_KEEP    how many archives to keep, here and remotely   (14)
#   BACKUP_REMOTE  an rclone remote:path; unset means no off-host copy
#   RCLONE_CONFIG  the rclone config file   (~/.config/rclone/rclone.conf)
#
# Each run writes one tty-cards-<UTC stamp>.tar holding db.dump (pg_dump custom
# format, already compressed) and ssh-keys.tar (the contents of the backend's /data/ssh).
set -euo pipefail
cd "$(dirname "$0")/.."

# An archive is the whole user base plus the host's private key: owner-only.
umask 077

die() {
	echo "backup: $*" >&2
	exit 1
}

[ -z "${RETENTION_DAYS:-}" ] || die "RETENTION_DAYS was replaced by BACKUP_KEEP, a count of archives"
dir=${BACKUP_DIR:-backups}
keep=${BACKUP_KEEP:-14}
case $keep in '' | *[!0-9]* | 0) die "BACKUP_KEEP must be a positive integer, got '$keep'" ;; esac

mkdir -p "$dir"
name=tty-cards-$(date -u +%Y%m%dT%H%M%SZ).tar
work=$(mktemp -d "$dir/.work.XXXXXX")
trap 'rm -rf "$work"' EXIT

# Inside the db container, so its own POSTGRES_* say which database and role: no
# password and no .env parsing on this side.
docker compose exec -T db sh -c 'exec pg_dump --format=custom -U "$POSTGRES_USER" "$POSTGRES_DB"' >"$work/db.dump"

# The backend image is FROM scratch - no shell, no tar - but docker cp reads a
# container's volumes without running anything in it, stopped containers included.
backend=$(docker compose ps -aq backend)
[ -n "$backend" ] || die "no backend container, so no ssh host key to back up"
docker cp "$backend:/data/ssh/." - >"$work/ssh-keys.tar"
tar -tf "$work/ssh-keys.tar" | grep -qx './server' || die "the ssh-keys volume holds no host key (server)"

# Built in the work directory and renamed into place: an interrupted run leaves
# nothing that looks like a good archive to retention, restore or the drill.
tar -cf "$work/$name" -C "$work" db.dump ssh-keys.tar
mv "$work/$name" "$dir/$name"
echo "wrote $dir/$name"

# Names sort by time, so everything past the newest $keep goes.
prune() { sort -r | tail -n +"$((keep + 1))"; }
find "$dir" -maxdepth 1 -name 'tty-cards-*.tar' -exec basename {} \; | prune | while read -r old; do
	rm -f "$dir/$old"
done

[ -n "${BACKUP_REMOTE:-}" ] || exit 0

# rclone from the pinned image in compose.yaml. The config directory is mounted
# writable because OAuth remotes save refreshed tokens back into it.
config=${RCLONE_CONFIG:-$HOME/.config/rclone/rclone.conf}
[ -f "$config" ] || die "BACKUP_REMOTE is set but $config does not exist (rclone config)"
rclone() {
	docker compose --progress quiet --profile backup run --rm -T \
		-v "$(cd "$dir" && pwd):/backups:ro" \
		-v "$(cd "$(dirname "$config")" && pwd):/config/rclone" \
		rclone --config "/config/rclone/$(basename "$config")" "$@" </dev/null
}
remote=${BACKUP_REMOTE%/}
rclone copyto "/backups/$name" "$remote/$name"
echo "copied to $remote/$name"
for old in $(rclone lsf --files-only --include 'tty-cards-*.tar' "$remote" | prune); do
	rclone deletefile "$remote/$old"
done
