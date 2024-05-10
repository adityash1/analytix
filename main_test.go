package main

import (
	"testing"
)

func TestDecodeData(t *testing.T) {
	payload, err := decodeData("eyJ0cmFja2luZyI6eyJ0eXBlIjoicGFnZSIsImlkZW50aXR5IjoiIiwidWEiOiJNb3ppbGxhLzUuMCAoTWFjaW50b3NoOyBJbnRlbCBNYWMgT1MgWCAxMF8xNV83KSBBcHBsZVdlYktpdC81MzcuMzYgKEtIVE1MLCBsaWtlIEdlY2tvKSBDaHJvbWUvMTI0LjAuMC4wIFNhZmFyaS81MzcuMzYiLCJldmVudCI6Ii8iLCJjYXRlZ29yeSI6IlBhZ2Ugdmlld3MiLCJyZWZlcnJlciI6IiIsImlzVG91Y2hEZXZpY2UiOmZhbHNlfSwic2l0ZV9pZCI6Im15LXNpdGUtaWQtaGVyZSJ9")
	if err != nil {
		t.Fatal(err)
	} else if len(payload) < 10 {
		t.Errorf("payload bytes seems invalid at len %d", len(payload))
	}
}
