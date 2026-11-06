package elevated

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"testing"

	"github.com/thinkaliker/labassistant/module"
)

// envProbe records the environment a module sees while Execute runs in the helper.
type envProbe struct{ seen map[string]string }

func (p *envProbe) Manifest() module.Manifest { return module.Manifest{Name: "probe"} }
func (p *envProbe) Detect(context.Context) (module.Detection, error) {
	return module.Detection{Applicable: true}, nil
}
func (p *envProbe) Status(context.Context) (module.Status, error) { return module.Status{}, nil }
func (p *envProbe) Execute(_ context.Context, _ module.ActionRequest, _ func(module.Event)) (module.Result, error) {
	p.seen = map[string]string{
		"LABASSISTANT_SYS_SIMULATE":  os.Getenv("LABASSISTANT_SYS_SIMULATE"),
		"LABASSISTANT_NOT_FORWARDED": os.Getenv("LABASSISTANT_NOT_FORWARDED"),
	}
	data, _ := json.Marshal(p.seen)
	return module.Result{State: module.JobSucceeded, Data: data}, nil
}

// TestHelperProcess is not a real test: TestRunForwardsEnvThroughEnvReset execs the test
// binary as the helper, and this is the helper's main.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("ELEVATED_TEST_HELPER") != "1" {
		t.Skip("helper subprocess only")
	}
	args := os.Args
	for i, a := range args {
		if a == "--request" && i+1 < len(args) {
			f, err := os.Open(args[i+1])
			if err != nil {
				os.Exit(3)
			}
			if err := Serve(context.Background(), f, os.Stdout, map[string]module.Module{"probe": &envProbe{}}); err != nil {
				os.Exit(4)
			}
			os.Exit(0)
		}
	}
	os.Exit(2)
}

// TestRunForwardsEnvThroughEnvReset runs a helper under `env -i`, which clears its environment
// the way sudo's env_reset does. The associate's opt-out must still reach the module.
func TestRunForwardsEnvThroughEnvReset(t *testing.T) {
	envBin, err := exec.LookPath("env")
	if err != nil {
		t.Skip("no env binary")
	}
	t.Setenv("LABASSISTANT_SYS_SIMULATE", "1")
	t.Setenv("LABASSISTANT_NOT_FORWARDED", "leaked")
	cmd := []string{envBin, "-i", "ELEVATED_TEST_HELPER=1", os.Args[0], "-test.run=^TestHelperProcess$", "--"}
	res, err := Run(context.Background(), cmd, "", Request{JobID: "j1", Module: "probe", Action: "x"}, func(module.Event) {})
	if err != nil {
		t.Fatal(err)
	}
	var seen map[string]string
	if err := json.Unmarshal(res.Data, &seen); err != nil {
		t.Fatalf("result data %s: %v", res.Data, err)
	}
	if seen["LABASSISTANT_SYS_SIMULATE"] != "1" {
		t.Errorf("helper saw LABASSISTANT_SYS_SIMULATE=%q, want 1", seen["LABASSISTANT_SYS_SIMULATE"])
	}
	if seen["LABASSISTANT_NOT_FORWARDED"] != "" {
		t.Errorf("non-allowlisted var reached the helper: %q", seen["LABASSISTANT_NOT_FORWARDED"])
	}
}

// TestServeAppliesForwardedEnv: sudo resets the helper's environment, so the opt-outs the
// associate was started with must arrive in the request — and only allowlisted keys apply.
func TestServeAppliesForwardedEnv(t *testing.T) {
	t.Setenv("LABASSISTANT_SYS_SIMULATE", "")
	t.Setenv("LABASSISTANT_NOT_FORWARDED", "")
	probe := &envProbe{}
	req, _ := json.Marshal(Request{Module: "probe", Action: "x", Env: map[string]string{
		"LABASSISTANT_SYS_SIMULATE":  "1",
		"LABASSISTANT_NOT_FORWARDED": "leaked",
	}})
	var out bytes.Buffer
	if err := Serve(context.Background(), bytes.NewReader(req), &out, map[string]module.Module{"probe": probe}); err != nil {
		t.Fatal(err)
	}
	if got := probe.seen["LABASSISTANT_SYS_SIMULATE"]; got != "1" {
		t.Errorf("forwarded var = %q, want 1", got)
	}
	if got := probe.seen["LABASSISTANT_NOT_FORWARDED"]; got != "" {
		t.Errorf("non-allowlisted var applied: %q", got)
	}
	var f Frame
	if err := json.Unmarshal(out.Bytes(), &f); err != nil || f.Result == nil || f.Result.State != module.JobSucceeded {
		t.Errorf("want a succeeded result frame, got %s", out.String())
	}
}

func TestForwardedEnvSnapshotsOnlySetVars(t *testing.T) {
	os.Unsetenv("LABASSISTANT_SYS_SIMULATE")
	if env := forwardedEnv(); env != nil {
		t.Errorf("unset var forwarded: %v", env)
	}
	t.Setenv("LABASSISTANT_SYS_SIMULATE", "1")
	if env := forwardedEnv(); env["LABASSISTANT_SYS_SIMULATE"] != "1" {
		t.Errorf("forwardedEnv = %v", env)
	}
}
