.PHONY: build test test-short test-integration lint fmt fix deadcode vulncheck semgrep cover clean ci loadtest backup restore restore-drill install-tools migrate-create migrate-up migrate-down

ci: fmt fix lint deadcode semgrep cover build

build:
	go build -o bin/server ./cmd/server

test:
	go test -race -shuffle=on ./...

test-short:
	go test -race -short -shuffle=on ./...

test-integration:
	go test -race -shuffle=on -tags=integration ./... -count=1 -timeout 15m

lint:
	golangci-lint run
	golangci-lint run --build-tags=integration

# The tools' versions live here only: CI runs these targets, so a bump cannot reach
# one and not the other. They are not go.mod tool directives, which would pull
# x/tools and x/vuln into the module graph the server's own dependencies resolve in.
DEADCODE = go run golang.org/x/tools/cmd/deadcode@v0.50.0
GOVULNCHECK = go run golang.org/x/vuln/cmd/govulncheck@v1.8.0

# Tests count as roots: the engine's WithTurnTimeout and the two figlet-cache hooks
# exist for them. Anything unreachable from main AND from every test is dead.
deadcode:
	@out=$$($(DEADCODE) -test ./...); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

# Known vulnerabilities the code can reach, not just ones somewhere in go.sum.
vulncheck:
	$(GOVULNCHECK) ./...

# Project invariants golangci-lint cannot express (.semgrep/tty-cards.yml). The rule
# tests run first: a rule that stopped matching would otherwise pass as a clean tree.
# The explicit test paths are needed because semgrep skips dot directories when
# discovering tests.
SEMGREP = uvx semgrep@1.178.0
semgrep:
	$(SEMGREP) --metrics=off --test --config .semgrep/tty-cards.yml .semgrep/tty-cards.go
	$(SEMGREP) scan --metrics=off --config .semgrep/ --error .

# Unit + integration coverage of the production packages, failing under COVER_MIN.
# Test helpers and the load-test tool are not the product, so they are left out of the
# denominator. With -coverpkg every test binary reports every package's blocks, so a
# block (file:start,end) counts once, covered if any binary ran it. The total is read
# from the profile alone: go tool cover -func maps blocks onto the source as it is on
# disk now, and miscounts a file edited since the run.
COVER_MIN = 95.0
COVER_PKGS = $(shell go list ./... | grep -vE '/internal/(testutil|game/gametest|tui/tuitest)$$|/cmd/loadtest$$' | paste -sd, -)
cover:
	go test -race -shuffle=on -tags=integration -count=1 -timeout 15m \
		-coverpkg=$(COVER_PKGS) -coverprofile=coverage.out ./...
	@awk -v min=$(COVER_MIN) '$$1 != "mode:" { stmts[$$1] = $$2; if ($$3 > 0) hit[$$1] = 1 } \
		END { for (b in stmts) { all += stmts[b]; if (b in hit) run += stmts[b] } \
		pct = all ? 100 * run / all : 0; printf "total coverage %.1f%%, minimum %s%%\n", pct, min; \
		exit (sprintf("%.1f", pct) + 0 < min + 0) }' coverage.out

fmt:
	go fmt ./...

fix:
	go fix ./...

clean:
	rm -rf bin/

# Concurrent-SSH-session load test against an already-running server. Every run
# registers fresh accounts, so PREFIX must change between runs on the same
# database or registration fails with "username taken". Start the server under test
# with REGISTRATION_LIMIT, REGISTRATION_LIMIT_WIDE and REGISTRATION_GLOBAL_LIMIT above
# SESSIONS, RATE_LIMIT_CONNECTIONS above SESSIONS/RAMP per second, and
# PROXY_PROTOCOL=false when ADDR skips nginx. PLAY is the share of sessions seated at
# Crazy Eights tables of TABLE seats; the rest follow JOURNEY.
# Override any flag: make loadtest SESSIONS=1000 PLAY=0.4 PREFIX=r2 HOLD=10m
loadtest:
	go run ./cmd/loadtest -addr $(or $(ADDR),127.0.0.1:6969) -sessions $(or $(SESSIONS),200) \
		-hold $(or $(HOLD),60s) -ramp $(or $(RAMP),10s) -journey $(or $(JOURNEY),menu) \
		-prefix $(or $(PREFIX),load) -play-fraction $(or $(PLAY),0) -table-size $(or $(TABLE),4)

# Backups of the running compose stack: README.md "Backups". restore needs the
# archive (BACKUP=backups/tty-cards-<stamp>.tar); restore-drill defaults to the newest.
backup:
	./scripts/backup.sh

restore:
	./scripts/restore.sh $(BACKUP)

restore-drill:
	./scripts/restore-drill.sh $(BACKUP)

# Database Migrations
install-tools:
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3

migrate-create:
	@read -p "Enter migration name: " name; \
	migrate create -ext sql -dir internal/db/migrations -seq $$name

migrate-up:
	migrate -path internal/db/migrations -database "$$DB_DSN" up

# One step, and only after a typed yes: DB_DSN stays exported for migrate-up, and
# with a single init migration one step down is every table.
migrate-down:
	@read -p "Roll back the newest migration on \$$DB_DSN? Type yes: " ok; [ "$$ok" = yes ]
	migrate -path internal/db/migrations -database "$$DB_DSN" down 1
