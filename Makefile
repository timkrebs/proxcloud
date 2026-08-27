.PHONY: dev backend frontend build test test-integration check gen-types compose restore-drill hooks lint fmt cover

# Native dev: backend with hot reload + next dev (two processes).
dev:
	@$(MAKE) -j2 backend frontend

backend:
	@if command -v air >/dev/null 2>&1; then \
		cd backend && air; \
	else \
		echo ">> air not installed — running without hot reload"; \
		echo ">> (optional: go install github.com/air-verse/air@latest)"; \
		cd backend && go run ./cmd/proxcloud; \
	fi

frontend:
	cd frontend && npm run dev

build:
	cd backend && go build ./...
	cd frontend && npm run build

# Unit tests only (fast, no services). The DB-dependent store tests carry
# //go:build integration and are NOT compiled here (ADR-0024).
test:
	cd backend && go test ./...
	cd frontend && npm run test --if-present

# Integration tests: the DB-dependent suite. Requires a Postgres reachable via
# DATABASE_URL (a scratch DB — the destructive guard needs a test/scratch name or
# PROXCLOUD_ALLOW_DESTRUCTIVE_TESTS=1). Runs the COMPLETE suite (unit + integration).
test-integration:
	cd backend && go test -race -tags=integration ./...

gen-types:
	cd backend && go run github.com/gzuidhof/tygo generate

# Full gate: build, vet, tests, generated types committed.
check:
	cd backend && go vet ./... && go build ./... && go test ./...
	cd frontend && npm run lint && npm run test --if-present
	$(MAKE) gen-types && git diff --exit-code frontend/src/lib/api/generated || \
		(echo "ERROR: generated types out of date — commit the tygo output" && exit 1)

compose:
	docker-compose up --build

# Prove a pg_dump snapshot restores (release-engineer.md / disaster-recovery).
# Spins its OWN throwaway Postgres and never touches a dev/prod `proxcloud` DB.
restore-drill:
	./deploy/restore-drill.sh

# ── Source stage: hooks, formatting, lint, coverage ──────────────────────────

# Install the local pre-commit hooks (advisory; CI is authoritative, ADR-0022).
hooks:
	@if command -v pre-commit >/dev/null 2>&1; then \
		pre-commit install --hook-type pre-commit --hook-type commit-msg && \
		echo ">> pre-commit hooks installed"; \
	else \
		echo ">> pre-commit not installed — install it, then re-run 'make hooks':"; \
		echo ">>   pipx install pre-commit   (or) pip install pre-commit"; \
		exit 1; \
	fi

# Auto-format everything: gofmt + goimports (if present) + prettier.
fmt:
	cd backend && gofmt -w .
	@command -v goimports >/dev/null 2>&1 && (cd backend && goimports -w .) || \
		echo ">> goimports not installed (optional): go install golang.org/x/tools/cmd/goimports@latest"
	cd frontend && npm run format

# The CI 'source' + lint gates, run locally: gofmt/prettier must be clean; vet +
# staticcheck + eslint.
lint:
	cd backend && test -z "$$(gofmt -l .)" || (echo "ERROR: gofmt-dirty files above" && gofmt -l backend && exit 1)
	cd backend && go vet ./...
	cd backend && go install honnef.co/go/tools/cmd/staticcheck@v0.7.0 && "$$(go env GOPATH)/bin/staticcheck" ./...
	cd frontend && npm run lint && npm run format:check

# Measure + gate backend coverage against the ratchet floor in .github/coverage.env
# (ADR-0023). Requires DATABASE_URL (scratch DB) so the complete profile is real.
cover:
	cd backend && go test -tags=integration -coverprofile=integration.cover -coverpkg=./... ./...
	cd backend && go tool cover -func=integration.cover | tail -1
	@. ./.github/coverage.env; \
	total="$$(cd backend && go tool cover -func=integration.cover | tail -1 | awk '{gsub(/%/,"",$$3); print $$3}')"; \
	echo ">> backend coverage $$total% (floor $$COVERAGE_MIN_BACKEND%)"; \
	awk -v c="$$total" -v m="$$COVERAGE_MIN_BACKEND" 'BEGIN{ if (c+0 < m+0){ print "FAIL: below floor"; exit 1 } }'
	cd frontend && npm run test:coverage
