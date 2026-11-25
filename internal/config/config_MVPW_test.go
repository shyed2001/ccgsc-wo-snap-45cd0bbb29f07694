package config

import "testing"

func TestValidateRunOrigin(t *testing.T) {
	c := Default()
	c.RunOrigin = "https://ccgsc-run.digitalbd.org"
	c.PublicOrigin = "https://ccgsc.digitalbd.org"
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.PublicOrigin = ""
	if err := c.Validate(); err == nil {
		t.Fatal("accepted run origin without public origin")
	}
	c.PublicOrigin = "https://ccgsc.digitalbd.org"
	c.RunOrigin = "https://user@example.com/run"
	if err := c.Validate(); err == nil {
		t.Fatal("accepted run origin with userinfo and path")
	}
}
