.PHONY: build run stop clean test db-backup db-restore deploy deploy-build deploy-push deploy-db deploy-up deploy-down deploy-full deploy-logs

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

db-backup:
	mkdir -p backup
	sqlite3 data/cato.db ".backup 'backup/cato-$$(date +%F).db'"
	@echo "Backup saved to backup/cato-$$(date +%F).db"

db-restore:
	@if [ -z "$(FILE)" ]; then echo "Usage: make db-restore FILE=backup/cato-YYYY-MM-DD.db"; exit 1; fi
	cp "$(FILE)" data/cato.db
	@echo "Restored from $(FILE)"

# --- NAS deployment ---
NAS_HOST ?= nas2
NAS_PATH ?= /volume1/Shared/Mediapedia
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
	ssh $(NAS_HOST) 'mkdir -p $(NAS_PATH)/data'
	cat data/cato.db | ssh $(NAS_HOST) 'cat > $(NAS_PATH)/data/cato.db'

deploy-up:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml up -d --build'

deploy-down:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml down'

deploy: deploy-push deploy-up
	@echo "Cato deployed to http://10.0.0.42:7080"

deploy-full: deploy-push deploy-db deploy-up
	@echo "Cato deployed with database to http://10.0.0.42:7080"

deploy-logs:
	ssh $(NAS_HOST) 'cd $(NAS_PATH) && $(COMPOSE) -f docker-compose.yml logs -f'
