package config

import (
	"strings"
	"testing"
)

func TestRESTPortDefaultsAndIsValidated(t *testing.T) {
	c := &Config{}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if c.Server.RESTPort != 3375 {
		t.Fatalf("default rest_port = %d, want 3375", c.Server.RESTPort)
	}

	c = &Config{Server: ServerConfig{Port: 43391, RESTPort: 43392}}
	if err := c.Validate(); err != nil || c.Server.RESTPort != 43392 {
		t.Fatalf("a configured rest_port is kept: %v %d", err, c.Server.RESTPort)
	}

	c = &Config{Server: ServerConfig{Port: 43391, RESTPort: 43391}}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "port of its own") {
		t.Fatalf("same port as gRPC must be refused, got %v", err)
	}
	c = &Config{Server: ServerConfig{RESTPort: 70000}}
	if err := c.Validate(); err == nil {
		t.Fatal("an out-of-range rest_port must be refused")
	}
}
