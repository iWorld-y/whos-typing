package main

import (
	"bytes"
	"testing"
)

func TestSSHInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"ssh"}, {"ssh", "run", "host"}, {"ssh", "connect", "-oBad"}, {"ssh", "--config"}, {"ssh", "nope", "host"}} {
		var out, err bytes.Buffer
		if code := run(args, &out, &err); code != 2 {
			t.Fatalf("%v: %d %s", args, code, err.String())
		}
	}
}
