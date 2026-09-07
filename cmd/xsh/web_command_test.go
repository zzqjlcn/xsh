package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestWebCommandFlags(t *testing.T) {
	for _, args := range [][]string{{"--port", "-1"}, {"--port", "65536"}, {"unexpected"}, {"--unknown"}} {
		if err := runWebCommand(args, &bytes.Buffer{}); err == nil {
			t.Errorf("accepted invalid arguments %v", args)
		}
	}
	var output bytes.Buffer
	if err := runWebCommand([]string{"--help"}, &output); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "port") {
		t.Fatal("missing port help")
	}
}
