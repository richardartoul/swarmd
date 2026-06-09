.PHONY: agentrepl agentrepl-memory server tui lint test

# lint mirrors the CI lint job: formatting, vet, and staticcheck over
# swarmd's own packages (pkg/sh outside the listed paths is a vendored fork
# of mvdan/sh and tracks upstream).
lint:
	@unformatted="$$(gofmt -l .)"; \
	if [ -n "$$unformatted" ]; then \
		echo "gofmt needed:"; echo "$$unformatted"; exit 1; \
	fi
	go vet ./...
	go run honnef.co/go/tools/cmd/staticcheck@2025.1.1 ./pkg/agent/... ./pkg/server/... ./pkg/tools/... \
		./pkg/sh/sandbox/... ./pkg/sh/moreinterp/... ./pkg/sh/memfs/...

test:
	go test ./...

agentrepl:
	go run ./pkg/agent/cmd/agentrepl -root .

agentrepl-memory:
	go run ./pkg/agent/cmd/agentrepl -memfs -root /workspace

server:
	go run ./pkg/server/cmd/swarmd server

tui:
	go run ./pkg/server/cmd/swarmd tui
