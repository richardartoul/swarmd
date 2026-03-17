package readwebpage

import (
	"strings"
	"time"

	toolscommon "github.com/richardartoul/swarmd/pkg/tools/common"
	"golang.org/x/net/html"
)

const (
	defaultReadWebPageTimeout = 20 * time.Second
	defaultReadWebPageBytes   = 2 << 20
	maxReadWebPageLinks       = 25
)

func htmlDocumentTitle(doc *html.Node) string {
	var title string
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if title != "" || node == nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == "title" {
			title = toolscommon.CollapseWhitespace(toolscommon.NodeTextContent(node))
			return
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return title
}

func collectDocumentLinks(doc *html.Node, limit int) []string {
	if doc == nil || limit <= 0 {
		return nil
	}
	links := make([]string, 0, toolscommon.MinInt(limit, maxReadWebPageLinks))
	seen := make(map[string]struct{}, limit)
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node == nil || len(links) >= limit {
			return
		}
		if node.Type == html.ElementNode && node.Data == "a" {
			href := strings.TrimSpace(toolscommon.HTMLAttr(node, "href"))
			if parsed, err := toolscommon.ValidateHTTPToolURL(href); err == nil {
				value := parsed.String()
				if _, ok := seen[value]; !ok {
					seen[value] = struct{}{}
					links = append(links, value)
				}
			}
		}
		for child := node.FirstChild; child != nil && len(links) < limit; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return links
}
