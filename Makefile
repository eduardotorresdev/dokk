.PHONY: dev run test vet check-ui check build build-linux deploy

HOST ?= root@your-server

dev:
	cd ui && npm run dev

run:
	DOKK_USER=$${DOKK_USER:-admin} DOKK_PASSWORD=$${DOKK_PASSWORD:-admin} go run .

test:
	go test ./...

vet:
	go vet ./...

check-ui:
	find ui/src ui/scripts \( -name '*.js' -o -name '*.mjs' \) -print0 | xargs -0 -n1 node --check
	node ui/scripts/check-i18n.mjs

check: vet test check-ui

build:
	go build -o bin/dokk .

build-linux:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-X main.version=$$(git describe --tags --always --dirty)" -o bin/dokk-linux-amd64 .

deploy: build-linux
	scp bin/dokk-linux-amd64 $(HOST):/usr/local/bin/dokk.new
	ssh $(HOST) 'mv /usr/local/bin/dokk.new /usr/local/bin/dokk && systemctl restart dokk'
