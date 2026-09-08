package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestVersionAndActionableHelp(t *testing.T) {
	for _, args := range [][]string{{"--version", "--json"}, {"version", "--json"}} {
		var out, diag bytes.Buffer
		if code := Run(context.Background(), args, &out, &diag, ProductionDependencies()); code != 0 {
			t.Fatal(code, diag.String())
		}
		var info map[string]string
		if json.Unmarshal(out.Bytes(), &info) != nil || info["version"] == "" || info["platform"] == "" {
			t.Fatal(out.String())
		}
	}
	for _, name := range []string{"join", "answers"} {
		var out, diag bytes.Buffer
		Run(context.Background(), []string{name, "--help"}, &out, &diag, ProductionDependencies())
		if !strings.Contains(diag.String(), "HOST_IP SESSION_ID") || !strings.Contains(diag.String(), "Example:") {
			t.Fatal(diag.String())
		}
	}
	var out, diag bytes.Buffer
	Run(context.Background(), []string{"help", "legacy"}, &out, &diag, ProductionDependencies())
	if !strings.Contains(out.String(), "repair-thread") {
		t.Fatal(out.String())
	}
}
func TestClientDoctorDoesNotRequireCodexOrProject(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	var out, diag bytes.Buffer
	if code := Run(context.Background(), []string{"doctor", "--json"}, &out, &diag, ProductionDependencies()); code != 0 {
		t.Fatal(code, diag.String())
	}
	var checks []diagnosis
	if err := json.Unmarshal(out.Bytes(), &checks); err != nil {
		t.Fatal(err)
	}
	for _, c := range checks {
		if c.Check == "Codex" || c.Check == "Git project" {
			t.Fatal(c)
		}
	}
}
