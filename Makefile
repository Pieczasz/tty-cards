.PHONY: build test test-short test-integration lint fmt fix deadcode clean ci loadtest

ci: fmt fix lint deadcode test build

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

# Tests count as roots: the engine's WithTurnTimeout and the two figlet-cache hooks
# exist for them. Anything unreachable from main AND from every test is dead.
deadcode:
	@out=$$(go run golang.org/x/tools/cmd/deadcode@v0.50.0 -test ./...); if [ -n "$$out" ]; then echo "$$out"; exit 1; fi

fmt:
	go fmt ./...

fix:
	go fix ./...

clean:
	rm -rf bin/

# Concurrent-SSH-session load test against an already-running server. Every run
# registers fresh accounts, so PREFIX must change between runs on the same
# database or registration fails with "username taken". The server allows 5 new
# accounts per network per hour by default, so start the one under test with e.g.
# REGISTRATION_LIMIT=10000 (and PROXY_PROTOCOL=false when ADDR skips nginx).
# Override any flag: make loadtest SESSIONS=500 PREFIX=r2 HOLD=60s
loadtest:
	go run ./cmd/loadtest -addr $(or $(ADDR),127.0.0.1:6969) -sessions $(or $(SESSIONS),200) \
		-hold $(or $(HOLD),60s) -ramp $(or $(RAMP),10s) -journey $(or $(JOURNEY),menu) \
		-prefix $(or $(PREFIX),load)

# Database Migrations
install-tools:
	go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@v4.18.3

migrate-create:
	@read -p "Enter migration name: " name; \
	migrate create -ext sql -dir internal/db/migrations -seq $$name

migrate-up:
	migrate -path internal/db/migrations -database "$$DB_DSN" up

migrate-down:
	migrate -path internal/db/migrations -database "$$DB_DSN" down -all
