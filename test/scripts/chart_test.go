// Copyright 2026 Alex Bahel
// Licensed under the Apache License, Version 2.0 (see LICENSE).

package scripts

import (
	"os/exec"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

type agentDeployment struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Args []string `json:"args"`
					Env  []struct {
						Name      string `json:"name"`
						Value     string `json:"value"`
						ValueFrom any    `json:"valueFrom"`
					} `json:"env"`
				} `json:"containers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

// renderAgent renders the chart with helm and returns fleetops-agent's Deployment.
func renderAgent(t *testing.T, sets ...string) agentDeployment {
	t.Helper()
	helm, err := exec.LookPath("helm")
	if err != nil {
		t.Skip("helm not on PATH")
	}
	args := []string{"template", "fleetops", "../../charts/fleet-ops-copilot", "-n", "fleetops-system", "-s", "templates/fleetops-agent.yaml"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	out, err := exec.Command(helm, args...).CombinedOutput() //nolint:gosec // G204: helm with fixed arguments
	if err != nil {
		t.Fatalf("helm template %v: %v\n%s", sets, err, out)
	}
	for _, doc := range strings.Split(string(out), "\n---") {
		var d agentDeployment
		if err := yaml.Unmarshal([]byte(doc), &d); err == nil && d.Kind == "Deployment" && d.Metadata.Name == "fleetops-agent" {
			return d
		}
	}
	t.Fatalf("no fleetops-agent Deployment in:\n%s", out)
	return agentDeployment{}
}

// D103: llm.profile=airgap runs fleetops-agent with the llamacpp backend
// against llama-server, with the v6 record's sampling (D49), and no API key;
// the default stays the cloud profile with the key from the Secret.
func TestChartAirgapProfileRunsLlamaCpp(t *testing.T) {
	d := renderAgent(t, "llm.profile=airgap")
	c := d.Spec.Template.Spec.Containers[0]
	args := strings.Join(c.Args, " ")
	for _, want := range []string{"-backend=llamacpp", "-llama-url=http://llama-server:8080", "-model=qwen2.5-7b-instruct"} {
		if !strings.Contains(args, want) {
			t.Errorf("airgap args lack %s: %v", want, c.Args)
		}
	}
	env := map[string]string{}
	for _, e := range c.Env {
		env[e.Name] = e.Value
		if e.ValueFrom != nil {
			t.Errorf("airgap env %s comes from a Secret or another source", e.Name)
		}
	}
	for k, v := range map[string]string{
		"FLEETOPS_LLAMACPP_TEMPERATURE": "0.7", "FLEETOPS_LLAMACPP_TOP_P": "0.8",
		"FLEETOPS_LLAMACPP_TOP_K": "20", "FLEETOPS_LLAMACPP_REPEAT_PENALTY": "1.05",
	} {
		if env[k] != v {
			t.Errorf("airgap env %s=%q, want %q", k, env[k], v)
		}
	}
	if _, ok := env["ANTHROPIC_API_KEY"]; ok {
		t.Error("airgap profile sets ANTHROPIC_API_KEY")
	}

	d = renderAgent(t)
	c = d.Spec.Template.Spec.Containers[0]
	if !strings.Contains(strings.Join(c.Args, " "), "-backend=claude") {
		t.Errorf("default args: %v", c.Args)
	}
	found := false
	for _, e := range c.Env {
		if e.Name == "ANTHROPIC_API_KEY" && e.ValueFrom != nil {
			found = true
		}
	}
	if !found {
		t.Error("default profile without the API key from the Secret")
	}
}
