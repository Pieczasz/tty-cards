#!/bin/sh
# The certbot service's whole job (compose.yaml): obtain the apex certificate over
# the webroot nginx serves on :80, renew it when it is due, and copy it to /tls,
# where nginx reads it. The proxy service reloads nginx when that copy changes, so
# nothing here needs the Docker socket or a signal across containers.
set -eu
# The private key lands in /tls too: readable by root, which nginx's master is.
umask 077

: "${TTY_DOMAIN:?set TTY_DOMAIN}"
live=/etc/letsencrypt/live/$TTY_DOMAIN

# --keep-until-expiring makes this one command both the first issue and every
# renewal: it contacts Let's Encrypt only when there is no certificate or the one
# there is inside its renewal window (30 days), and is a no-op otherwise.
set -- certonly --non-interactive --agree-tos --keep-until-expiring \
	--webroot --webroot-path /var/www/certbot \
	--cert-name "$TTY_DOMAIN" --domain "$TTY_DOMAIN"
if [ -n "${ACME_EMAIL:-}" ]; then
	set -- "$@" --email "$ACME_EMAIL"
else
	set -- "$@" --register-unsafely-without-email
fi

while :; do
	# Twice a day, as Let's Encrypt asks. A failure (DNS not pointed here yet, port
	# 80 closed) retries hourly: well inside the five failed validations per hour
	# the production API allows for one name.
	pause=43200
	certbot "$@" || pause=3600

	# The key first: the proxy reloads on a change to fullchain.pem, so by the time
	# it sees one the matching key is already in place. mv is atomic within /tls.
	if [ -s "$live/fullchain.pem" ] && ! cmp -s "$live/fullchain.pem" /tls/fullchain.pem; then
		cp -L "$live/privkey.pem" /tls/privkey.pem.new
		cp -L "$live/fullchain.pem" /tls/fullchain.pem.new
		mv /tls/privkey.pem.new /tls/privkey.pem
		mv /tls/fullchain.pem.new /tls/fullchain.pem
		echo "installed the certificate for $TTY_DOMAIN"
	fi
	sleep "$pause"
done
