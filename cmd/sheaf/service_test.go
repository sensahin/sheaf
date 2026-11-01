package main

import (
	"strings"
	"testing"
)

func TestSystemdUnit(t *testing.T) {
	u := systemdUnit("/usr/local/bin/sheaf", "/home/senol/.sheaf", "/home/senol/.local/bin:/usr/bin", "127.0.0.1:7517", "https://vps.example.ts.net", true)
	for _, want := range []string{
		"ExecStart=/usr/local/bin/sheaf daemon --web 127.0.0.1:7517 --public-url https://vps.example.ts.net --headless\n",
		"KillMode=process\n",
		"Environment=PATH=/home/senol/.local/bin:/usr/bin\n",
		"Environment=SHEAF_HOME=/home/senol/.sheaf\n",
		"WantedBy=default.target\n",
	} {
		if !strings.Contains(u, want) {
			t.Fatalf("unit lacks %q:\n%s", want, u)
		}
	}
}
