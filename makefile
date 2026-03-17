.PHONY: agentrepl agentrepl-memory server tui

agentrepl:
	go run ./pkg/agent/cmd/agentrepl -root .

agentrepl-memory:
	go run ./pkg/agent/cmd/agentrepl -memfs -root /workspace

server:
	go run ./pkg/server/cmd/swarmd server

tui:
	go run ./pkg/server/cmd/swarmd tui
