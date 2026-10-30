package app

import (
	"bytes"
	"strconv"
)

const (
	bridgeScriptPath     = "/_rc/bridge.js"
	commentTicketSuffix  = ":c"
	maxBridgeInjectBytes = 5 << 20
	bridgeScanBytes      = 64 << 10
)

var (
	bridgeTag = []byte(`<script src="` + bridgeScriptPath + `"></script>`)
	utf8BOM   = []byte{0xEF, 0xBB, 0xBF}
)

// contentTicketSubject names what a viewer ticket authorizes. Tickets minted
// for signed-in viewers carry the comment suffix so their HTML gets the bridge;
// share-link tickets never do.
func contentTicketSubject(artifactID string, version int, comments bool) string {
	subject := artifactID + ":" + strconv.Itoa(version)
	if comments {
		subject += commentTicketSuffix
	}
	return subject
}

func ticketAllows(subject, artifactID string, version int) (ok, comments bool) {
	base := contentTicketSubject(artifactID, version, false)
	switch subject {
	case base:
		return true, false
	case base + commentTicketSuffix:
		return true, true
	}
	return false, false
}

// injectBridge inserts the bridge script after the first <head>, <html>, or
// doctype tag found in the first bridgeScanBytes outside HTML comments, or at
// the start of the document. Matching is ASCII case-insensitive so byte
// offsets stay valid.
func injectBridge(doc []byte) []byte {
	lower := blankComments(asciiLower(doc[:min(len(doc), bridgeScanBytes)]))
	at := openingTagEnd(lower, "<head")
	if at < 0 {
		at = openingTagEnd(lower, "<html")
	}
	if at < 0 {
		at = openingTagEnd(lower, "<!doctype")
	}
	if at < 0 {
		at = 0
		if bytes.HasPrefix(doc, utf8BOM) {
			at = len(utf8BOM)
		}
	}
	out := make([]byte, 0, len(doc)+len(bridgeTag))
	out = append(out, doc[:at]...)
	out = append(out, bridgeTag...)
	return append(out, doc[at:]...)
}

func asciiLower(b []byte) []byte {
	out := make([]byte, len(b))
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return out
}

// blankComments overwrites each <!-- ... --> span in b with spaces, in place,
// so tag matching skips it while byte offsets stay valid. An unterminated
// comment blanks everything after its start.
func blankComments(b []byte) []byte {
	for from := 0; ; {
		i := bytes.Index(b[from:], []byte("<!--"))
		if i < 0 {
			return b
		}
		start := from + i
		end := len(b)
		if j := bytes.Index(b[start+len("<!--"):], []byte("-->")); j >= 0 {
			end = start + len("<!--") + j + len("-->")
		}
		for k := start; k < end; k++ {
			b[k] = ' '
		}
		from = end
	}
}

// openingTagEnd returns the index just past the '>' closing the first tag
// that starts with prefix and is followed by a tag-name terminator, or -1.
func openingTagEnd(lower []byte, prefix string) int {
	for from := 0; ; {
		i := bytes.Index(lower[from:], []byte(prefix))
		if i < 0 {
			return -1
		}
		next := from + i + len(prefix)
		if next < len(lower) && isTagNameEnd(lower[next]) {
			if end := bytes.IndexByte(lower[next:], '>'); end >= 0 {
				return next + end + 1
			}
			return -1
		}
		from = next
	}
}

func isTagNameEnd(c byte) bool {
	switch c {
	case ' ', '\t', '\n', '\r', '\f', '/', '>':
		return true
	}
	return false
}
