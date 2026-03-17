// See LICENSE for licensing information

package agent

import websearchtool "github.com/richardartoul/swarmd/pkg/tools/websearch"

func NewDuckDuckGoWebSearchBackend() WebSearchBackend {
	return websearchtool.NewDuckDuckGoBackend()
}

// NewGoogleWebSearchBackend is kept for backward compatibility and now returns the default DuckDuckGo HTML backend.
func NewGoogleWebSearchBackend() WebSearchBackend {
	return websearchtool.NewGoogleBackend()
}
