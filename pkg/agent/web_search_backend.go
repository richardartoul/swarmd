// See LICENSE for licensing information

package agent

import websearchtool "github.com/richardartoul/swarmd/pkg/tools/websearch"

// NewDuckDuckGoWebSearchBackend returns the default web_search backend,
// which scrapes the DuckDuckGo HTML endpoint.
func NewDuckDuckGoWebSearchBackend() WebSearchBackend {
	return websearchtool.NewDuckDuckGoBackend()
}

// NewGoogleWebSearchBackend is kept for backward compatibility and now returns the default DuckDuckGo HTML backend.
func NewGoogleWebSearchBackend() WebSearchBackend {
	return websearchtool.NewGoogleBackend()
}
