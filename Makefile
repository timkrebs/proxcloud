.PHONY: dev backend frontend build test check gen-types compose

# Native dev: backend with hot reload + next dev (two processes).
dev:
	@$(MAKE) -j2 backend frontend

backend:
	cd backend && air

frontend:
	cd frontend && npm run dev

build:
	cd backend && go build ./...
	cd frontend && npm run build

test:
	cd backend && go test ./...
	cd frontend && npm run test --if-present

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
