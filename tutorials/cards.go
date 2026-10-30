package main

import (
	"fmt"
	"html"
	"strings"
)

// titleCard opens a tutorial.
func titleCard(title, subtitle string) string {
	return fmt.Sprintf(`<div class="big">%s</div><p>%s</p>`, html.EscapeString(title), html.EscapeString(subtitle))
}

// tableCard shows a heading, an optional line, and rows of name and value;
// values are HTML.
func tableCard(heading, intro string, rows ...[2]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h2>%s</h2>", html.EscapeString(heading))
	if intro != "" {
		fmt.Fprintf(&b, "<p>%s</p>", intro)
	}
	b.WriteString("<table>")
	for _, r := range rows {
		fmt.Fprintf(&b, "<tr><td>%s</td><td>%s</td></tr>", html.EscapeString(r[0]), r[1])
	}
	b.WriteString("</table>")
	return b.String()
}

// codeCard shows a heading, code blocks and lines of text (HTML).
func codeCard(heading string, parts ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "<h2>%s</h2>", html.EscapeString(heading))
	for _, p := range parts {
		if code, ok := strings.CutPrefix(p, "$ "); ok {
			fmt.Fprintf(&b, "<pre>%s</pre>", html.EscapeString(code))
		} else {
			fmt.Fprintf(&b, "<p>%s</p>", p)
		}
	}
	return b.String()
}

// code formats s as inline code.
func code(s string) string { return "<code>" + html.EscapeString(s) + "</code>" }

// endCard closes a tutorial.
func endCard(next string) string {
	return fmt.Sprintf(`<div class="big">Done</div><p>%s</p><p>Documentation: <span class="accent">github.com/daimoc/jitsi-media-tools</span></p>`+
		`<p class="credit">Video: Big Buck Bunny, © 2008 Blender Foundation, www.bigbuckbunny.org, CC BY 3.0</p>`, next)
}
