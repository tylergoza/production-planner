BINARY := production-planner
LDFLAGS := -s -w

.PHONY: dev run test build build-linux build-linux-arm provision deploy backup restore move clean

# Serve templates/static from disk: edit HTML/JS/CSS and just refresh.
# Port 8090, so the maintenance tracker can run alongside on 8080.
dev:
	DEV=1 go run . -addr 127.0.0.1:8090

run: build
	./bin/$(BINARY)

test:
	go test ./...

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY) .

# Digital Ocean droplets / most home-lab servers
build-linux:
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-amd64 .

# Raspberry Pi 4/5 and other ARM boxes
build-linux-arm:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$(LDFLAGS)" -o bin/$(BINARY)-linux-arm64 .

# DigitalOcean droplet via Ansible: see "DigitalOcean with Ansible" in the README.
# Run through with-do-church, except backup FROM=<ip>, which needs only SSH.
#   make provision [RESTORE_PENDING=true]   box, alerts, DNS, then deploy
#   make deploy
#   make backup FROM=<ip or name> [STOP=true]
#   make restore [BACKUP=latest|<dir>] [FORCE=true]
#   make move FROM=<box>                    to the apps_box in vars.yml
RESTORE_PENDING ?= false
STOP    ?= false
FORCE   ?= false
BACKUP  ?= latest

provision:
	cd deploy/ansible && ansible-playbook provision.yml -e restore_pending=$(RESTORE_PENDING)

deploy:
	cd deploy/ansible && ansible-playbook deploy.yml

backup:
	cd deploy/ansible && ansible-playbook backup.yml -e from=$(FROM) -e stop=$(STOP)

restore:
	cd deploy/ansible && ansible-playbook restore.yml -e backup=$(BACKUP) -e force=$(FORCE)

move:
	cd deploy/ansible && ansible-playbook move.yml -e from=$(FROM) -e force=$(FORCE)

clean:
	rm -rf bin
