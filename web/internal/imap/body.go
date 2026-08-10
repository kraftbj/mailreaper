package imap

import (
	"bytes"
	"fmt"
	"html"
	"io"
	"regexp"
	"strings"

	"github.com/emersion/go-message"
	_ "github.com/emersion/go-message/charset"
)

/*
maxPartBytes caps how much of any single MIME part is read. Marketing email
routinely runs past 100 KB of HTML; the scanner only ever sends the LLM the
first couple thousand characters, so reading more is wasted work.
*/
const maxPartBytes = 256 * 1024

/*
extractTextBody turns a raw RFC822 message into the readable text a human
would see. It walks the MIME tree, decodes transfer encodings and charsets,
and prefers a text/plain part over text/html. When only HTML is present the
markup is stripped to text.

This exists because the LLM prompt needs body prose. Handing it the raw
message instead means the model sees only headers and MIME boilerplate.
*/
func extractTextBody(raw []byte) (string, error) {
	entity, err := message.Read(bytes.NewReader(raw))
	if err != nil && message.IsUnknownCharset(err) {
		// Unknown charset is recoverable: entity is still usable, bytes are
		// just not transcoded to UTF-8.
		err = nil
	}
	if err != nil {
		return "", fmt.Errorf("imap: parse message: %w", err)
	}

	plain, htmlText := collectParts(entity, 0)
	if strings.TrimSpace(plain) != "" {
		return normalizeWhitespace(plain), nil
	}
	if strings.TrimSpace(htmlText) != "" {
		return normalizeWhitespace(htmlToText(htmlText)), nil
	}
	return "", nil
}

/*
collectParts walks the MIME tree depth-first and accumulates the text/plain
and text/html content it finds. Parts with any other content type (images,
PDFs, calendar invites) are skipped. depth guards against a malformed message
nesting multiparts without end.
*/
func collectParts(entity *message.Entity, depth int) (plain, htmlText string) {
	if depth > 10 {
		return "", ""
	}

	if mr := entity.MultipartReader(); mr != nil {
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			p, h := collectParts(part, depth+1)
			plain += p
			htmlText += h
		}
		return plain, htmlText
	}

	mediaType, _, err := entity.Header.ContentType()
	if err != nil {
		mediaType = "text/plain"
	}
	// Attachments are not readable body text even when they are text/*.
	if disp, _, err := entity.Header.ContentDisposition(); err == nil && disp == "attachment" {
		return "", ""
	}

	switch mediaType {
	case "text/plain", "text/html":
	default:
		return "", ""
	}

	body, err := io.ReadAll(io.LimitReader(entity.Body, maxPartBytes))
	if err != nil {
		return "", ""
	}
	if mediaType == "text/html" {
		return "", string(body)
	}
	return string(body), ""
}

var (
	/*
	   Elements whose contents are markup or code, never readable text. Listed
	   one at a time because RE2 has no backreferences to pair the close tag.
	*/
	nonTextRe = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>` +
		`|<style\b[^>]*>.*?</style\s*>` +
		`|<head\b[^>]*>.*?</head\s*>` +
		`|<title\b[^>]*>.*?</title\s*>`)
	// Elements that introduce a visual line break.
	blockTagRe = regexp.MustCompile(`(?i)<(/?(p|div|br|tr|li|h[1-6]|table|section|article|blockquote)|td)\b[^>]*>`)
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]*>`)

	spaceRunRe     = regexp.MustCompile(`[ \t\x{00a0}]+`)
	blankLineRunRe = regexp.MustCompile(`\n[ \t]*(\n[ \t]*)+`)
)

/*
htmlToText renders HTML markup down to its visible text. Script, style and
head contents are dropped entirely; block-level tags become newlines so that
sentences do not run together; remaining tags are removed and entities
decoded. This is deliberately a text extractor, not a renderer — the output
feeds an LLM prompt, not a display.
*/
func htmlToText(src string) string {
	src = commentRe.ReplaceAllString(src, " ")
	src = nonTextRe.ReplaceAllString(src, " ")
	src = blockTagRe.ReplaceAllString(src, "\n")
	src = tagRe.ReplaceAllString(src, " ")
	return html.UnescapeString(src)
}

/*
normalizeWhitespace collapses the runs of spaces and blank lines that HTML
layout leaves behind, so the truncated snippet carries prose rather than
whitespace.
*/
func normalizeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "​", "")
	s = spaceRunRe.ReplaceAllString(s, " ")
	s = blankLineRunRe.ReplaceAllString(s, "\n\n")
	var lines []string
	for _, line := range strings.Split(s, "\n") {
		lines = append(lines, strings.TrimSpace(line))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
