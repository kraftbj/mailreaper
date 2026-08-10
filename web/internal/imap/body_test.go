package imap

import (
	"strings"
	"testing"
)

/*
Shape mirrors the real messages this scanner sees: a long header block
(DKIM signatures, Received chains) followed by a quoted-printable text/html
body. Before the MIME fix, FetchBody returned this whole blob verbatim, so the
scanner's 2000-char snippet never reached the body at all.
*/
const htmlOnlyMessage = "Content-Transfer-Encoding: quoted-printable\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"From: \"Longhorn Foundation\" <updates@go.texaslonghorns.com>\r\n" +
	"Subject: ACTION REQUESTED: Renew Your Membership Today!\r\n" +
	"Dkim-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; s=dk2016; " +
	"bh=g5po5+hLXHUGdvCxLCf3lnshXqdoZRMWV8nrcmC67kM=; b=Tq6edtOZQOZYzXL9V2Cj" +
	"kLdQHJR83Zkj3o9ba3v3JHvstouV7H4JuFMlXboopb1hU3jsHj3o5+fF6jfEOsJt9zClc55X\r\n" +
	"Mime-Version: 1.0\r\n" +
	"\r\n" +
	"<html><head><style>.btn { color: #bf5700; }</style></head><body>\r\n" +
	"<div><strong>RENEW YOUR MEMBERSHIP TODAY!</strong></div>\r\n" +
	"<p>Your gift must be made by Monday, August 31 at 11:59 p.m. CT to count=\r\n" +
	" toward your 2026 priority points.</p>\r\n" +
	"<script>trackOpen();</script>\r\n" +
	"<p>Thanks &amp; Hook &#39;em!</p></body></html>\r\n"

func TestExtractTextBodyHTMLOnly(t *testing.T) {
	got, err := extractTextBody([]byte(htmlOnlyMessage))
	if err != nil {
		t.Fatalf("extractTextBody: %v", err)
	}

	// The real deadline must survive quoted-printable soft line breaks.
	if !strings.Contains(got, "must be made by Monday, August 31 at 11:59 p.m. CT") {
		t.Errorf("deadline sentence missing from extracted body; got:\n%s", got)
	}
	// Headers must not leak into the snippet sent to the LLM.
	for _, bad := range []string{"Dkim-Signature", "Content-Transfer-Encoding", "Mime-Version"} {
		if strings.Contains(got, bad) {
			t.Errorf("header %q leaked into extracted body; got:\n%s", bad, got)
		}
	}
	// Script and style contents are not readable text.
	for _, bad := range []string{"trackOpen", "#bf5700"} {
		if strings.Contains(got, bad) {
			t.Errorf("non-text content %q leaked into extracted body; got:\n%s", bad, got)
		}
	}
	// HTML entities are decoded.
	if !strings.Contains(got, "Thanks & Hook 'em!") {
		t.Errorf("entities not decoded; got:\n%s", got)
	}
}

const multipartMessage = "MIME-Version: 1.0\r\n" +
	"Subject: Sale ends soon\r\n" +
	"Content-Type: multipart/alternative; boundary=\"XYZ\"\r\n" +
	"\r\n" +
	"--XYZ\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"Plain text version: offer valid through April 30.\r\n" +
	"--XYZ\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>HTML version: offer valid through April 30.</p>\r\n" +
	"--XYZ--\r\n"

func TestExtractTextBodyPrefersPlainText(t *testing.T) {
	got, err := extractTextBody([]byte(multipartMessage))
	if err != nil {
		t.Fatalf("extractTextBody: %v", err)
	}
	if !strings.Contains(got, "Plain text version: offer valid through April 30.") {
		t.Errorf("plain-text part missing; got:\n%s", got)
	}
	if strings.Contains(got, "HTML version") {
		t.Errorf("html alternative should not be used when text/plain exists; got:\n%s", got)
	}
}

const base64HTMLMessage = "MIME-Version: 1.0\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"Content-Transfer-Encoding: base64\r\n" +
	"\r\n" +
	"PHA+UlNWUCBieSBGcmlkYXksIE1heSAyLjwvcD4=\r\n"

func TestExtractTextBodyBase64(t *testing.T) {
	got, err := extractTextBody([]byte(base64HTMLMessage))
	if err != nil {
		t.Fatalf("extractTextBody: %v", err)
	}
	if !strings.Contains(got, "RSVP by Friday, May 2.") {
		t.Errorf("base64 body not decoded; got:\n%s", got)
	}
}
