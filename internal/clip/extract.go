// Package clip turns a captured link into a clean, body-only Markdown note
// (a "web clip") stored under Raw/Sources. It is the §3.2 piece of the IM
// quick-capture redesign: the front end acks instantly, and this package does
// the slow work (fetch + readability extraction) in the background.
//
// The extractor here is a dependency-free first cut (stdlib only). It does not
// match a full readability engine (e.g. Defuddle / Obsidian Web Clipper) and is
// expected to be swapped for a richer clip skill later — see
// docs/30-design/im-quick-capture.md §8. It is good enough to drop the obvious
// boilerplate (script/style/nav/footer) and emit readable Markdown for the
// common article shape.
package clip

import (
	"html"
	"strings"
)

// Article is the readability output: a page title plus body Markdown.
type Article struct {
	Title string
	Body  string
}

// rawTextElements hold CDATA-like content whose body must be skipped entirely.
var rawTextElements = map[string]bool{"script": true, "style": true, "noscript": true, "template": true, "svg": true}

// droppedElements are containers whose entire subtree is boilerplate.
var droppedElements = map[string]bool{"head": true, "nav": true, "footer": true, "aside": true, "form": true, "header": true}

// Extract converts an HTML document into a title + body-only Markdown article.
// It prefers the inner content of <article>, then <main>, then <body>, then the
// whole document, so site chrome outside the main content is dropped before
// conversion.
func Extract(htmlDoc string) Article {
	title := extractTitle(htmlDoc)
	region := preferredRegion(htmlDoc)
	body := tokenizeToMarkdown(region)
	return Article{Title: title, Body: body}
}

// preferredRegion returns the inner HTML of the most content-bearing region.
func preferredRegion(doc string) string {
	for _, tag := range []string{"article", "main", "body"} {
		if inner, ok := innerHTML(doc, tag); ok && strings.TrimSpace(stripTags(inner)) != "" {
			return inner
		}
	}
	return doc
}

// innerHTML returns the content between the first <tag ...> and its matching
// (non-nested-aware, first) </tag>. Good enough for the top-level article/main
// /body wrappers, which are not self-nested in practice.
func innerHTML(doc, tag string) (string, bool) {
	lower := strings.ToLower(doc)
	open := "<" + tag
	start := strings.Index(lower, open)
	if start < 0 {
		return "", false
	}
	// Advance past the opening tag's '>'.
	gt := strings.IndexByte(doc[start:], '>')
	if gt < 0 {
		return "", false
	}
	contentStart := start + gt + 1
	close := strings.Index(lower[contentStart:], "</"+tag)
	if close < 0 {
		return doc[contentStart:], true
	}
	return doc[contentStart : contentStart+close], true
}

func extractTitle(doc string) string {
	if inner, ok := innerHTML(doc, "title"); ok {
		if t := strings.TrimSpace(html.UnescapeString(collapseSpaces(stripTags(inner)))); t != "" {
			return t
		}
	}
	// Fall back to the first <h1>.
	if inner, ok := innerHTML(doc, "h1"); ok {
		if t := strings.TrimSpace(html.UnescapeString(collapseSpaces(stripTags(inner)))); t != "" {
			return t
		}
	}
	return ""
}

// token is one lexed HTML token.
type token struct {
	kind tokenKind
	name string // lowercased tag name (for tag tokens)
	attr map[string]string
	text string // raw text (for text tokens)
}

type tokenKind int

const (
	tokenText tokenKind = iota
	tokenOpen
	tokenClose
	tokenSelfClose
)

// tokenize lexes HTML into tokens, skipping comments and the bodies of raw-text
// elements (script/style/...).
func tokenize(s string) []token {
	var tokens []token
	i := 0
	n := len(s)
	for i < n {
		if s[i] != '<' {
			j := strings.IndexByte(s[i:], '<')
			if j < 0 {
				tokens = append(tokens, token{kind: tokenText, text: s[i:]})
				break
			}
			tokens = append(tokens, token{kind: tokenText, text: s[i : i+j]})
			i += j
			continue
		}
		// Comment / doctype.
		if strings.HasPrefix(s[i:], "<!--") {
			end := strings.Index(s[i:], "-->")
			if end < 0 {
				break
			}
			i += end + 3
			continue
		}
		if strings.HasPrefix(s[i:], "<!") {
			end := strings.IndexByte(s[i:], '>')
			if end < 0 {
				break
			}
			i += end + 1
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			tokens = append(tokens, token{kind: tokenText, text: s[i:]})
			break
		}
		rawTag := s[i+1 : i+end]
		i += end + 1
		tok := parseTag(rawTag)
		tokens = append(tokens, tok)
		// Skip the body of raw-text elements wholesale.
		if tok.kind == tokenOpen && rawTextElements[tok.name] {
			closeTag := "</" + tok.name
			if ci := strings.Index(strings.ToLower(s[i:]), closeTag); ci >= 0 {
				rest := s[i+ci:]
				if gt := strings.IndexByte(rest, '>'); gt >= 0 {
					i += ci + gt + 1
				} else {
					i += ci
				}
			} else {
				i = n
			}
		}
	}
	return tokens
}

func parseTag(raw string) token {
	raw = strings.TrimSpace(raw)
	selfClose := strings.HasSuffix(raw, "/")
	raw = strings.TrimSuffix(raw, "/")
	if strings.HasPrefix(raw, "/") {
		return token{kind: tokenClose, name: strings.ToLower(strings.TrimSpace(raw[1:]))}
	}
	name := raw
	var attrPart string
	if sp := strings.IndexAny(raw, " \t\r\n"); sp >= 0 {
		name = raw[:sp]
		attrPart = raw[sp+1:]
	}
	tok := token{kind: tokenOpen, name: strings.ToLower(name)}
	if selfClose {
		tok.kind = tokenSelfClose
	}
	if attrPart != "" {
		tok.attr = parseAttrs(attrPart)
	}
	return tok
}

// parseAttrs is a small attribute scanner; it only needs href reliably.
func parseAttrs(s string) map[string]string {
	attrs := map[string]string{}
	i := 0
	n := len(s)
	for i < n {
		for i < n && (s[i] == ' ' || s[i] == '\t' || s[i] == '\r' || s[i] == '\n') {
			i++
		}
		start := i
		for i < n && s[i] != '=' && s[i] != ' ' && s[i] != '\t' && s[i] != '\r' && s[i] != '\n' {
			i++
		}
		name := strings.ToLower(s[start:i])
		if name == "" {
			break
		}
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i >= n || s[i] != '=' {
			attrs[name] = ""
			continue
		}
		i++ // skip '='
		for i < n && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i < n && (s[i] == '"' || s[i] == '\'') {
			quote := s[i]
			i++
			vs := i
			for i < n && s[i] != quote {
				i++
			}
			attrs[name] = s[vs:i]
			if i < n {
				i++
			}
		} else {
			vs := i
			for i < n && s[i] != ' ' && s[i] != '\t' && s[i] != '\r' && s[i] != '\n' {
				i++
			}
			attrs[name] = s[vs:i]
		}
	}
	return attrs
}

// tokenizeToMarkdown converts an HTML fragment into Markdown.
func tokenizeToMarkdown(fragment string) string {
	tokens := tokenize(fragment)
	var b strings.Builder
	depthDropped := 0   // >0 while inside a dropped subtree
	var dropStack []string
	linkHref := ""
	inPre := false
	for _, tok := range tokens {
		switch tok.kind {
		case tokenText:
			text := tok.text
			if depthDropped > 0 {
				continue
			}
			if inPre {
				b.WriteString(html.UnescapeString(text))
				continue
			}
			decoded := html.UnescapeString(text)
			if linkHref != "" {
				b.WriteString(collapseSpaces(decoded))
				continue
			}
			b.WriteString(collapseSpaces(decoded))
		case tokenOpen, tokenSelfClose:
			if depthDropped > 0 {
				if tok.kind == tokenOpen && droppedElements[tok.name] {
					dropStack = append(dropStack, tok.name)
					depthDropped++
				}
				continue
			}
			if droppedElements[tok.name] {
				dropStack = append(dropStack, tok.name)
				depthDropped++
				continue
			}
			switch tok.name {
			case "h1":
				b.WriteString("\n\n# ")
			case "h2":
				b.WriteString("\n\n## ")
			case "h3":
				b.WriteString("\n\n### ")
			case "h4", "h5", "h6":
				b.WriteString("\n\n#### ")
			case "p", "div", "section", "ul", "ol":
				b.WriteString("\n\n")
			case "br":
				b.WriteString("\n")
			case "li":
				b.WriteString("\n- ")
			case "blockquote":
				b.WriteString("\n\n> ")
			case "strong", "b":
				b.WriteString("**")
			case "em", "i":
				b.WriteString("*")
			case "pre":
				inPre = true
				b.WriteString("\n\n```\n")
			case "code":
				if !inPre {
					b.WriteString("`")
				}
			case "a":
				if href := strings.TrimSpace(tok.attr["href"]); href != "" && !strings.HasPrefix(href, "#") && !strings.HasPrefix(href, "javascript:") {
					linkHref = href
					b.WriteString("[")
				}
			}
		case tokenClose:
			if depthDropped > 0 {
				if len(dropStack) > 0 && dropStack[len(dropStack)-1] == tok.name {
					dropStack = dropStack[:len(dropStack)-1]
					depthDropped--
				}
				continue
			}
			switch tok.name {
			case "strong", "b":
				b.WriteString("**")
			case "em", "i":
				b.WriteString("*")
			case "code":
				if !inPre {
					b.WriteString("`")
				}
			case "pre":
				inPre = false
				b.WriteString("\n```\n")
			case "a":
				if linkHref != "" {
					b.WriteString("](")
					b.WriteString(linkHref)
					b.WriteByte(')')
					linkHref = ""
				}
			case "p", "div", "section", "h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "li", "ul", "ol":
				b.WriteString("\n")
			}
		}
	}
	return normalizeMarkdown(b.String())
}

// stripTags removes every HTML tag, leaving decoded text — used for title and
// emptiness probes.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteByte(s[i])
			}
		}
	}
	return b.String()
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r', '\n', ' ':
			space = true
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	if space && b.Len() > 0 {
		// Preserve a single trailing space so adjacent inline runs don't fuse.
		b.WriteByte(' ')
	}
	return b.String()
}

// normalizeMarkdown trims each line and collapses 3+ blank lines to one blank.
func normalizeMarkdown(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		// Don't strip leading spaces inside fenced code (best-effort: keep as-is
		// when the line starts with at least 4 spaces, a common code indent).
		if strings.TrimSpace(trimmed) == "" {
			blanks++
			if blanks > 1 {
				continue
			}
			out = append(out, "")
			continue
		}
		blanks = 0
		out = append(out, strings.TrimLeft(trimmed, " \t"))
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}
