.PHONY: demo test check build smoke smoke-network evidence benchmark confirmation consensus-demo consensus-evidence smoke-bft bft-evidence

GO ?= go
TEST_FLAGS ?=
# Older Go linkers omit LC_UUID required by recent macOS versions.
# Keep this workaround local to the build; do not change the user's Go config.
ifeq ($(shell $(GO) env GOHOSTOS),darwin)
GO_LINK_FLAGS := -ldflags=-linkmode=external
SIGN_BINARIES := codesign --force --sign - bin/settlelab-demo bin/settlelab-node bin/settle
endif

demo:
	$(GO) run $(GO_LINK_FLAGS) ./cmd/demo

test:
	$(GO) test -race $(TEST_FLAGS) $(GO_LINK_FLAGS) ./...

check:
	$(GO) vet ./...
	$(GO) test -race $(TEST_FLAGS) $(GO_LINK_FLAGS) ./...

build:
	$(GO) build $(GO_LINK_FLAGS) -o bin/settlelab-demo ./cmd/demo
	$(GO) build $(GO_LINK_FLAGS) -o bin/settlelab-node ./cmd/node
	$(GO) build $(GO_LINK_FLAGS) -o bin/settle ./cmd/settle
	$(SIGN_BINARIES)

smoke: build
	python3 scripts/smoke.py

smoke-network: build
	python3 scripts/smoke_network.py

evidence: build
	python3 scripts/evidence.py

benchmark:
	$(GO) test $(GO_LINK_FLAGS) ./rpc -run '^$$' -bench BenchmarkReceiptRPC -benchmem -count=3

confirmation: build
	python3 scripts/confirmation.py

consensus-demo:
	$(GO) run $(GO_LINK_FLAGS) ./cmd/consensus-demo

consensus-evidence:
	python3 scripts/consensus_evidence.py

smoke-bft: build
	python3 scripts/smoke_bft.py

bft-evidence: smoke-bft
