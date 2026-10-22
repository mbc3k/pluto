package tve

import "testing"

func TestHiddenFormUnescapes(t *testing.T) {
	doc := `<html><form action="/acs?x=1" method="post"><input type="hidden" name="SAMLResponse" value="saml&amp;ok"><input type="text" name="visible" value="no"><input type="hidden" name="RelayState" value="st&#39;ate"></form></html>`
	action, fields, err := HiddenForm(doc, "https://sp.auth.adobe.com/saml/start")
	if err != nil {
		t.Fatal(err)
	}
	if action != "https://sp.auth.adobe.com/acs?x=1" {
		t.Fatalf("action %s", action)
	}
	if fields.Get("SAMLResponse") != "saml&ok" || fields.Get("RelayState") != "st'ate" {
		t.Fatalf("fields %v", fields)
	}
	numeric := `<form action="https&#x3a;&#x2f;&#x2f;idp.dtvce.com&#x2f;start" method="post"><input type="hidden" name="SAMLRequest" value="abc"></form>`
	action, _, err = HiddenForm(numeric, "https://sp.auth.adobe.com/saml")
	if err != nil {
		t.Fatal(err)
	}
	if action != "https://idp.dtvce.com/start" {
		t.Fatalf("numeric action %s", action)
	}
	if fields.Get("visible") != "" {
		t.Fatal("visible field was included")
	}
}

func TestHiddenFormMissing(t *testing.T) {
	if _, _, err := HiddenForm("<html>no form</html>", "https://example.com/"); err == nil {
		t.Fatal("expected missing form")
	}
	if _, _, err := HiddenForm(`<form action="/x"></form>`, "https://example.com/"); err == nil {
		t.Fatal("expected missing fields")
	}
}
