package tve

import (
	"fmt"
	"html"
	"net/url"
	"strings"
)

// HiddenForm reads the first auto-submitted form: its action and every
// hidden input. Adobe and DirecTV both hand the next hop back this way.
func HiddenForm(doc, pageURL string) (string, url.Values, error) {
	lower := strings.ToLower(doc)
	formAt := strings.Index(lower, "<form")
	if formAt < 0 {
		return "", nil, fmt.Errorf("sign-in page has no form")
	}
	end := strings.Index(lower[formAt:], "</form>")
	if end < 0 {
		end = len(lower) - formAt
	}
	form := doc[formAt : formAt+end]
	action := attr(form, "action")
	if action == "" {
		action = pageURL
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return "", nil, fmt.Errorf("sign-in page url: %w", err)
	}
	resolved, err := base.Parse(action)
	if err != nil {
		return "", nil, fmt.Errorf("sign-in form action: %w", err)
	}
	fields := url.Values{}
	rest := form
	for {
		i := strings.Index(strings.ToLower(rest), "<input")
		if i < 0 {
			break
		}
		rest = rest[i:]
		tagEnd := strings.Index(rest, ">")
		if tagEnd < 0 {
			break
		}
		tag := rest[:tagEnd]
		rest = rest[tagEnd+1:]
		if !strings.EqualFold(attr(tag, "type"), "hidden") {
			continue
		}
		name := attr(tag, "name")
		if name == "" {
			continue
		}
		fields.Set(name, attr(tag, "value"))
	}
	if len(fields) == 0 {
		return "", nil, fmt.Errorf("sign-in form has no hidden fields")
	}
	return resolved.String(), fields, nil
}

func attr(tag, name string) string {
	key := strings.ToLower(name) + "="
	lower := strings.ToLower(tag)
	i := strings.Index(lower, key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key):]
	if rest == "" {
		return ""
	}
	quote := rest[0]
	if quote != '"' && quote != '\'' {
		end := strings.IndexAny(rest, " \t>")
		if end < 0 {
			return htmlUnescape(rest)
		}
		return htmlUnescape(rest[:end])
	}
	rest = rest[1:]
	end := strings.IndexByte(rest, quote)
	if end < 0 {
		return htmlUnescape(rest)
	}
	return htmlUnescape(rest[:end])
}

func htmlUnescape(s string) string {
	// Adobe's SAML form encodes the next URL with numeric character
	// references (&#x3a; for ":"). Named entities alone leave that action
	// unusable.
	return html.UnescapeString(s)
}
