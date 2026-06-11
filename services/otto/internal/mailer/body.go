package mailer

import (
	"fmt"
	"strings"
)

// otpEmail renders the OTP message once so every provider (SendGrid,
// Resend, …) ships byte-identical content. The body is intentionally
// plain: most "unexpected chat OTP" mails go through spam filters, and
// heavy templates make the "is this suspicious" judgement harder for the
// recipient.
func otpEmail(recipientName, code, storeName string) (subject, text, html string) {
	if storeName == "" {
		storeName = "the store"
	}
	subject = fmt.Sprintf("%s — your support chat verification code", storeName)
	text = fmt.Sprintf(
		"Hi%s,\n\nYour one-time code to start a support chat on %s is:\n\n  %s\n\n"+
			"It expires in 10 minutes. If you did not request this, you can safely ignore this email.\n",
		greetingSuffix(recipientName), storeName, code,
	)
	html = fmt.Sprintf(
		`<div style="font-family: system-ui, -apple-system, Segoe UI, Roboto, sans-serif; color: #111827; font-size: 14px; line-height: 1.5;">`+
			`<p>Hi%s,</p>`+
			`<p>Your one-time code to start a support chat on <strong>%s</strong> is:</p>`+
			`<p style="font-size: 28px; letter-spacing: 8px; font-weight: 600; margin: 18px 0;">%s</p>`+
			`<p style="color: #6b7280;">It expires in 10 minutes. If you did not request this, you can safely ignore this email.</p>`+
			`</div>`,
		greetingSuffix(recipientName), htmlEscape(storeName), code,
	)
	return subject, text, html
}

func greetingSuffix(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return " " + name
}

// htmlEscape is a tiny helper so we don't pull in html/template for a
// single substitution. The only value that reaches HTML context is the
// store name, which is merchant-controlled string data.
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		"\"", "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}
