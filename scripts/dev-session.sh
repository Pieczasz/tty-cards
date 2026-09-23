#!/usr/bin/env bash
# Opens three SSH clients to a local terminal-card server in one tmux session.
#
#   ./scripts/dev-session.sh              # one window per player
#   TC_LAYOUT=panes ./scripts/dev-session.sh   # all three tiled in one window
#   TC_PORT=6969 ./scripts/dev-session.sh      # direct to the backend, bypassing nginx
#       (needs PROXY_PROTOCOL=false on the server: the listener otherwise REQUIREs a
#        PROXY header, which a bare ssh client never sends)
#   TC_DRY_RUN=1 ./scripts/dev-session.sh      # print the commands, open nothing
#
# The server's host key is regenerated whenever its volume is wiped, which is why
# known_hosts kept needing a purge. UserKnownHostsFile=/dev/null sidesteps that
# without touching the real known_hosts.
set -euo pipefail

HOST="${TC_HOST:-localhost}"
PORT="${TC_PORT:-22}"
SESSION="${TC_SESSION:-terminal-card}"
LAYOUT="${TC_LAYOUT:-windows}"

PLAYERS=(
	"one:$HOME/.ssh/id_localhost_1"
	"two:$HOME/.ssh/id_ed25519"
	"three:$HOME/.ssh/id_ed25519_second_user"
)

command -v tmux >/dev/null || {
	echo "tmux not found" >&2
	exit 1
}

for player in "${PLAYERS[@]}"; do
	key="${player#*:}"
	[ -r "$key" ] || {
		echo "missing ssh key: $key" >&2
		exit 1
	}
done

# IdentitiesOnly stops a loaded ssh-agent from offering another key first and
# logging every player in as the same user.
ssh_command() {
	local user="$1" key="$2"
	printf 'ssh -p %s -i %s -o IdentitiesOnly=yes -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR %s@%s' \
		"$PORT" "$key" "$user" "$HOST"
}

if [ -n "${TC_DRY_RUN:-}" ]; then
	for player in "${PLAYERS[@]}"; do
		ssh_command "${player%%:*}" "${player#*:}"
		echo
	done
	exit 0
fi

for _ in $(seq 30); do
	(exec 3<>"/dev/tcp/$HOST/$PORT") 2>/dev/null && break
	sleep 1
done || true
(exec 3<>"/dev/tcp/$HOST/$PORT") 2>/dev/null || {
	echo "nothing listening on $HOST:$PORT - start the server (docker compose up -d) first" >&2
	exit 1
}

tmux kill-session -t "$SESSION" 2>/dev/null || true

first=1
for player in "${PLAYERS[@]}"; do
	user="${player%%:*}"
	command="$(ssh_command "$user" "${player#*:}")"
	if [ "$first" = 1 ]; then
		tmux new-session -d -s "$SESSION" -n "$user" "$command"
		first=0
	elif [ "$LAYOUT" = panes ]; then
		tmux split-window -t "$SESSION" "$command"
	else
		tmux new-window -t "$SESSION" -n "$user" "$command"
	fi
done

if [ "$LAYOUT" = panes ]; then
	tmux select-layout -t "$SESSION" tiled
fi

# A stale $TMUX from a dead server makes switch-client fail with "no current
# client"; the shell is outside tmux in that case, so attaching is correct.
if [ -n "${TMUX:-}" ] && tmux switch-client -t "$SESSION" 2>/dev/null; then
	exit 0
fi
tmux attach -t "$SESSION"
