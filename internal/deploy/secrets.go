package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/amohammedimran/shipit/internal/config"
	"github.com/amohammedimran/shipit/internal/detect"
	"github.com/amohammedimran/shipit/internal/remote"
	"github.com/amohammedimran/shipit/internal/ui"
)

// ValidateValues checks an env file's values against the environment's rules.
// It returns names and verdicts only. A value is never put in the output.
func ValidateValues(e *config.Env, vals map[string]string) []remote.Check {
	var out []remote.Check
	for _, k := range e.Require {
		if strings.TrimSpace(vals[k]) == "" {
			out = append(out, remote.Check{Status: "problem", Name: k, Message: "is missing or empty"})
		} else {
			out = append(out, remote.Check{Status: "ok", Name: k, Message: "is set"})
		}
	}
	for k, pat := range e.Rules {
		v, has := vals[k]
		if !has || v == "" {
			continue
		}
		if regexp.MustCompile(pat).MatchString(v) {
			out = append(out, remote.Check{Status: "ok", Name: k, Message: "matches the rule for " + e.Name})
		} else {
			out = append(out, remote.Check{Status: "problem", Name: k, Message: "does not match the rule for " + e.Name + " (expected to match " + pat + ")"})
		}
	}
	if e.Protected {
		for k, v := range vals {
			if (strings.HasSuffix(k, "_URL") || strings.HasSuffix(k, "_HOST") || strings.HasSuffix(k, "_URI")) &&
				(strings.Contains(v, "localhost") || strings.Contains(v, "127.0.0.1")) && !strings.HasPrefix(k, "NEXT_PUBLIC") {
				out = append(out, remote.Check{Status: "warn", Name: k, Message: "points at localhost. On a server that means the server itself, which is often not what you want"})
			}
		}
	}
	return out
}

// PushEnv sends this environment's local settings file to the server.
func (d *Deployer) PushEnv(ctx context.Context) error {
	e := d.Env
	w := d.out()
	if e.EnvFile == "" {
		ui.Fail("No settings file is chosen for "+e.Name+".", "", "Add env_file = .env.production to .shipit/"+e.Name+".conf")
		return ErrStopped
	}
	if (e.Protected || e.Name == "production") && detect.LooksLocal(filepath.Base(e.EnvFile)) {
		ui.Fail(e.EnvFile+" looks like a local file, and "+e.Name+" is a live environment.", "Sending it could put test keys into production.", "Choose the production file in .shipit/"+e.Name+".conf")
		return ErrStopped
	}
	p := filepath.Join(d.Dir, e.EnvFile)
	vals, err := detect.Values(p)
	if err != nil {
		ui.Fail("Cannot read "+e.EnvFile+".", err.Error(), "")
		return ErrStopped
	}
	cs := ValidateValues(e, vals)
	if PrintChecks(cs) > 0 {
		ui.Fail("Nothing was sent.", "The file does not meet the rules for "+e.Name+".", "Fix "+e.EnvFile+" and run this again")
		return ErrStopped
	}
	data, _ := os.ReadFile(p)
	fmt.Fprintf(w, "Send %d settings from %s to %s:%s\n", len(vals), ui.Bold(e.EnvFile), d.Conn.Describe(), remote.EnvPath(e))
	fmt.Fprintln(w, ui.Dim("The old file there is backed up first. Values are never printed."))
	if !d.Opt.Yes {
		ok, cerr := ui.Confirm("Send them?", true, "This copies the file to the server, readable only by the server login (mode 600).")
		if cerr != nil || !ok {
			fmt.Fprintln(w, "Cancelled. Nothing was changed.")
			return ErrStopped
		}
	}
	if d.Opt.DryRun {
		fmt.Fprintln(w, "This was a dry run. Nothing was sent.")
		return nil
	}
	if raw, berr := d.runQuiet(ctx, "backup-env", nil); berr != nil {
		explain("Could not back up the old file.", raw, "")
		return ErrStopped
	}
	if err := d.Conn.Put(ctx, remote.EnvPath(e), 0o600, data); err != nil {
		ui.Fail("Could not send the file.", err.Error(), "Run: shipit doctor "+e.Name)
		return ErrStopped
	}
	ui.OK("Sent. Only the server login can read the file there.")
	return nil
}

// CheckEnv checks the file that is on the server. It prints verdicts only.
func (d *Deployer) CheckEnv(ctx context.Context) error {
	e := d.Env
	raw, _ := d.runQuiet(ctx, "check-env", nil)
	cs := remote.Checks(raw)
	if len(cs) > 0 && cs[0].Name == "file" {
		ui.Fail("The server has no settings file for "+e.Name+".", remote.EnvPath(e)+" does not exist.", "run shipit secrets push "+e.Name)
		return ErrStopped
	}
	if PrintChecks(cs) > 0 {
		return ErrStopped
	}
	return nil
}
