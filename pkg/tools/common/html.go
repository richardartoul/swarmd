package common

import (
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

func CollapseWhitespace(text string) string {
	fields := strings.FieldsFunc(text, unicode.IsSpace)
	return strings.TrimSpace(strings.Join(fields, " "))
}

func NodeTextContent(node *html.Node) string {
	if node == nil {
		return ""
	}
	if node.Type == html.TextNode {
		return node.Data
	}
	if node.Type == html.ElementNode {
		switch node.Data {
		case "script", "style", "noscript":
			return ""
		}
	}
	var b strings.Builder
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		b.WriteString(NodeTextContent(child))
		b.WriteByte(' ')
	}
	return b.String()
}

func HTMLAttr(node *html.Node, name string) string {
	if node == nil {
		return ""
	}
	for _, attr := range node.Attr {
		if attr.Key == name {
			return attr.Val
		}
	}
	return ""
}
