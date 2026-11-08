package remote

import "testing"

func TestKVAndChecks(t *testing.T) {
	kv := KV("os=Ubuntu 24.04\narch=aarch64\nnoise\ntool.docker=yes\n")
	if kv["os"] != "Ubuntu 24.04" || kv["tool.docker"] != "yes" || len(kv) != 3 {
		t.Errorf("KV wrong: %v", kv)
	}
	cs := Checks("ok|KEY|is set\nproblem|OTHER|is missing or empty\ngarbage\nwarn|permissions|file mode is 644, expected 600\n")
	if len(cs) != 3 || cs[1].Status != "problem" || cs[1].Name != "OTHER" {
		t.Errorf("Checks wrong: %+v", cs)
	}
}

func TestHealthTries(t *testing.T) {
	if tries(60) != 30 || tries(6) != 3 || tries(0) != 30 {
		t.Error("timeout to tries conversion is wrong")
	}
}
