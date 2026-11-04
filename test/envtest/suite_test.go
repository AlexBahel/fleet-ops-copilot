//go:build envtest

/*
Copyright 2026 Alex Bahel

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package envtest runs the copilot's RBAC, admission policies and approval
// path against a real kube-apiserver. The environment (Swarmada CRDs and
// roles from the Swarmada module, the copilot's manifests, a stand-in for
// Swarmada's FleetTask activation webhook) is built by internal/testenv.
package envtest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/testenv"
)

const (
	swarmNS    = testenv.SwarmNS
	focNS      = testenv.FocNS
	mcpUser    = testenv.MCPUser
	approvUser = testenv.ApprovUser
	agentUser  = testenv.AgentUser
)

var (
	env      *testenv.Env
	adminCfg *rest.Config
	admin    client.Client
	repoRoot string
	people   = map[string]*rest.Config{}
)

func saGroups() []string { return testenv.SAGroups() }

func saConfig(user string) *rest.Config { return env.SAConfig(user) }

func asSA(t testing.TB, user string) client.Client {
	t.Helper()
	c, err := env.SAClient(user)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func asPerson(t testing.TB, name string) client.Client {
	t.Helper()
	c, err := client.New(people[name], client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func setStandinFailurePolicy(ctx context.Context, fp string) error {
	return env.SetStandinFailurePolicy(ctx, admissionv1.FailurePolicyType(fp))
}

func TestMain(m *testing.M) {
	wd, _ := os.Getwd()
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
	var err error
	env, err = testenv.Start(repoRoot, testenv.DefaultPeople)
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest setup:", err)
		os.Exit(1)
	}
	adminCfg, admin = env.AdminCfg, env.Admin
	for _, p := range testenv.DefaultPeople {
		people[p.Name] = env.PersonConfig(p.Name)
	}
	code := m.Run()
	env.Stop()
	os.Exit(code)
}
