.PHONY: build run stop clean test test-race test-js test-backup test-e2e vet security release-check db-backup db-restore deploy deploy-build deploy-push deploy-db deploy-up deploy-down deploy-full deploy-logs

DB_PATH ?= data/cato.db
BACKUP_DIR ?= backup
GOVULNCHECK_VERSION := v1.8.0

build:
	go build -o cato ./cmd/cato

run: build
	@mkdir -p data/covers
	@{ ./cato & echo $$! > .pid; }
	@echo "Cato running on http://localhost:7080 (PID: $$(cat .pid))"

stop:
	@if [ -f .pid ]; then kill $$(cat .pid) 2>/dev/null && rm -f .pid && echo "Cato stopped"; else echo "Cato not running"; fi

clean:
	rm -f cato .pid
	rm -f data/cato.db data/cato.db-journal data/cato.db-wal data/cato.db-shm

test:
	go test ./...

test-race:
	go test -race ./...

vet:
	go vet ./...

test-js:
	node scripts/cache-regression.cjs
	node scripts/rendering-regression.cjs
	node scripts/view-cache-regression.cjs

test-backup:
	python3 -B scripts/database_test.py

test-e2e:
	npm --prefix e2e ci
	cd e2e && npx playwright install chromium && npm run test:e2e

security:
	go run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...
	npm --prefix e2e audit --audit-level=high

# Keep this sequential, including under make -j: one owner builds/runs tests.
release-check:
	$(MAKE) test
	$(MAKE) test-race
	$(MAKE) vet
	$(MAKE) build
	$(MAKE) deploy-build
	$(MAKE) test-js
	$(MAKE) test-backup
	$(MAKE) security
	$(MAKE) test-e2e

db-backup:
	python3 -B scripts/database.py backup --db "$(DB_PATH)" --backup-dir "$(BACKUP_DIR)"

db-restore:
	@if [ -z "$(FILE)" ]; then echo "Usage: make db-restore FILE=backup/cato-YYYY-MM-DD.db"; exit 1; fi
	@if [ "$(SERVICE_STOPPED)" != "1" ]; then echo "Stop all Cato processes first, then set SERVICE_STOPPED=1. See docs/production-runbook.md."; exit 1; fi
	python3 -B scripts/database.py restore --db "$(DB_PATH)" --backup-dir "$(BACKUP_DIR)" --file "$(FILE)" --service-stopped

# --- NAS deployment ---
NAS_HOST ?= nas2
NAS_PATH ?= /volume1/Shared/Cato
DOCKER ?= PATH=/usr/local/bin:/usr/bin:/bin /usr/local/bin/docker
COMPOSE ?= PATH=/usr/local/bin:/usr/bin:/bin /usr/local/bin/docker-compose

deploy-build:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o cato ./cmd/cato

deploy-push: deploy-build
	cat cato | ssh $(NAS_HOST) 'cat > $(NAS_PATH)/cato && chmod +x $(NAS_PATH)/cato'
	cat Dockerfile | ssh $(NAS_HOST) 'cat > $(NAS_PATH)/Dockerfile'
	cat docker-compose.yml | ssh $(NAS_HOST) 'cat > $(NAS_PATH)/docker-compose.yml'
	tar czf - web/static | ssh $(NAS_HOST) 'cd $(NAS_PATH) && rm -rf web/static && tar xzf -'

deploy-db:
	@echo "Automatic production database replacement is disabled. Follow the stopped-service restore procedure in docs/production-runbook.md."
	@exit 1

deploy-up:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml up -d --build'

deploy-down:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml down'

deploy: deploy-push deploy-up
	@echo "Cato deployed to http://10.0.0.42:7080"

deploy-full:
	@echo "deploy-full is disabled to protect production data. Use make deploy for code, or the documented restore procedure for data."
	@exit 1

deploy-logs:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml logs -f'
