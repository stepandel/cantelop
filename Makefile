.PHONY: check check-architecture check-cli-api-contract check-cli-release check-console check-edge check-fire-fuse-release check-fire-fuse-state check-go check-python check-platform check-sdk-platform-contract check-starter qualify-release artifacts builder-artifacts compute-artifacts storage-artifacts cli-artifacts fire-fuse-artifacts deploy-builder deploy-compute deploy-storage

# Relative to platform/ansible. Pass extra flags with ANSIBLE_ARGS, for example
# ANSIBLE_ARGS="--check --diff" or ANSIBLE_ARGS="--tags base".
ANSIBLE_INVENTORY ?= inventory/staging/hosts.yml
ANSIBLE_ARGS ?=
SHIP_ARGS ?=

.PHONY: ship-fire-fuse ship-builder ship-storage ship-configure

# Software updates from this controller; deploy-* remains host provisioning.
ship-fire-fuse ship-builder ship-storage: ship-%:
	python3 scripts/ship.py $* $(SHIP_ARGS)

ship-configure:
	python3 scripts/ship.py configure $(SHIP_ARGS)

check: check-architecture check-cli-api-contract check-license-notices check-public-source check-go check-console check-edge check-python check-platform

.PHONY: check-public-source
check-public-source:
	python3 scripts/public-source.py verify

.PHONY: check-license-notices
check-license-notices:
	python3 scripts/release-notices.py check

check-architecture:
	python3 scripts/check-architecture.py

check-cli-api-contract:
	python3 scripts/check-cli-api-contract.py

check-go:
	go test ./...

check-console:
	cd console && bun test && bun run typecheck

check-edge:
	cd edge/contracts && bun test
	cd edge/workers && bun test && bun run typecheck && bun run test:fleet-runtime

check-python:
	python3 -m unittest discover -s scripts/tests
	python3 -m unittest discover -s platform/ansible/tests

check-platform:
	terraform fmt -check platform/cloudflare

check-cli-release:
	./scripts/check-cli-release.sh

check-fire-fuse-release:
	./scripts/check-fire-fuse-release.sh

check-fire-fuse-state:
	./scripts/check-fire-fuse-state-compatibility.sh

check-sdk-platform-contract:
	./scripts/check-sdk-platform-contract.sh

check-starter:
	./scripts/check-starter.sh

qualify-release:
	./scripts/qualify-release-candidate.sh

artifacts: compute-artifacts builder-artifacts

builder-artifacts:
	./scripts/build-builder-linux-amd64.sh

compute-artifacts:
	./scripts/build-compute-linux-amd64.sh
	./scripts/prepare-firecracker-linux-amd64.sh

storage-artifacts:
	./scripts/build-storage-linux-amd64.sh

cli-artifacts:
	./scripts/build-cli-release.sh

fire-fuse-artifacts:
	./scripts/build-fire-fuse-release.sh

deploy-builder deploy-compute deploy-storage: deploy-%:
	cd platform/ansible && ansible-playbook --inventory $(ANSIBLE_INVENTORY) --ask-vault-pass $*.yml $(ANSIBLE_ARGS)
