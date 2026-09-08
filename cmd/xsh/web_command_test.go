package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWebCommandFlags(t *testing.T) {
	for _, args := range [][]string{{"--host", ""}, {"--port", "-1"}, {"--port", "65536"}, {"--root-path", "relative"}, {"--root-path", "/xsh?query"}, {"unexpected"}, {"--unknown"}} {
		if err := runWebCommand(args, &bytes.Buffer{}); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
	var output bytes.Buffer
	if err := runWebCommand([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "host") || !strings.Contains(output.String(), "port") || !strings.Contains(output.String(), "root-path") {
		t.Fatal("missing host, port or root-path help")
	}
}
