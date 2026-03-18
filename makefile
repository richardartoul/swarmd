.PHONY: agentrepl agentrepl-memory server tui

agentrepl:
	SWARMD_DEBUG_PROMPT=true go run ./pkg/agent/cmd/agentrepl -root . -prompt "Explore the internet for interesting facts then summarize them for me."

agentrepl-memory:
	go run ./pkg/agent/cmd/agentrepl -memfs -root /workspace

server:
	go run ./pkg/server/cmd/swarmd server

tui:
	go run ./pkg/server/cmd/swarmd tui
