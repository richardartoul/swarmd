package websearch

import (
	"strings"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	"golang.org/x/net/html"
)

const (
	defaultDuckDuckGoSearchURL = "https://html.duckduckgo.com/html/"
	defaultWebSearchProvider   = "duckduckgo_html"
)

func firstDescendantWithClass(node *html.Node, className string) *html.Node {
	if node == nil {
		return nil
	}
	if hasHTMLClass(node, className) {
		return node
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if match := firstDescendantWithClass(child, className); match != nil {
			return match
		}
	}
	return nil
}

func nearestAncestorWithClass(node *html.Node, className string) *html.Node {
	for current := node; current != nil; current = current.Parent {
		if hasHTMLClass(current, className) {
			return current
		}
	}
	return nil
}

func hasHTMLClass(node *html.Node, className string) bool {
	for _, field := range strings.Fields(toolscommon.HTMLAttr(node, "class")) {
		if field == className {
			return true
		}
	}
	return false
}
