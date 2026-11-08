package deploy

import (
	"strings"
	"testing"

	"github.com/amohammedimran/shipit/internal/config"
)

func TestSafeTag(t *testing.T) {
	cases := map[string]string{"v1.2.0": "v1.2.0", "v1.2.0-3-gabc123": "v1.2.0-3-gabc123", "feature/x y": "feature-x-y", "": "release", "//": "release"}
	for in, want := range cases {
		if got := SafeTag(in); got != want {
			t.Errorf("SafeTag(%q)=%q want %q", in, got, want)
		}
	}
	if len(SafeTag(strings.Repeat("a", 300))) > 80 {
		t.Error("long tags must be cut")
	}
}

func TestValidateValuesNeverEchoesValues(t *testing.T) {
	e := &config.Env{Name: "production", Protected: true,
		Require: []string{"KEY", "MISSING"},
		Rules:   map[string]string{"KEY": "^rzp_live_"}}
	vals := map[string]string{"KEY": "rzp_test_SECRETVALUE", "DATABASE_URL": "postgres://localhost/db"}
	cs := ValidateValues(e, vals)
	var problems, warns int
	for _, c := range cs {
		if strings.Contains(c.Message, "SECRETVALUE") || strings.Contains(c.Name, "SECRETVALUE") {
			t.Fatalf("a value leaked into the output: %+v", c)
		}
		switch c.Status {
		case "problem":
			problems++
		case "warn":
			warns++
		}
	}
	if problems != 2 {
		t.Errorf("want 2 problems (wrong prefix, missing key), got %d: %+v", problems, cs)
	}
	if warns != 1 {
		t.Errorf("want a localhost warning for a protected environment, got %d", warns)
	}
	vals["KEY"] = "rzp_live_ok"
	vals["MISSING"] = "x"
	for _, c := range ValidateValues(e, vals) {
		if c.Status == "problem" {
			t.Errorf("a correct file must pass: %+v", c)
		}
	}
}
