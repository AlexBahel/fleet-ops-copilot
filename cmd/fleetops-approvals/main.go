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

// Command fleetops-approvals runs the approval controller: it accepts or refuses
// ApprovalDecisions and executes approved zone maintenance and zone stops.
package main

import (
	"flag"
	"log"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
)

func main() {
	policyPath := flag.String("policy", "policies/default.yaml", "policy file")
	auditPath := flag.String("audit", "fleetops-approvals-audit.jsonl", "JSONL audit file")
	metrics := flag.String("metrics-bind-address", "0", "metrics address; 0 disables")
	probe := flag.String("health-probe-bind-address", ":8081", "health probe address")
	flag.Parse()
	ctrl.SetLogger(zap.New())

	p, err := policy.Load(*policyPath)
	if err != nil {
		log.Fatal(err)
	}
	red := redact.FromEnv()
	aw, err := audit.Open(*auditPath, red)
	if err != nil {
		log.Fatal(err)
	}
	defer func() { _ = aw.Close() }()
	cfg := ctrl.GetConfigOrDie()
	scheme := fleet.NewScheme()
	nss := map[string]cache.Config{}
	for _, ns := range p.Namespaces {
		nss[ns] = cache.Config{}
	}
	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme, Metrics: metricsserver.Options{BindAddress: *metrics}, HealthProbeBindAddress: *probe,
		Cache: cache.Options{DefaultNamespaces: nss},
	})
	if err != nil {
		log.Fatal(err)
	}
	// Reads and writes go straight to the API server (no cache), so every
	// policy re-check sees current state.
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		log.Fatal(err)
	}
	rec := &approval.Reconciler{
		C: c, Engine: policy.New(p), Authz: approval.SARAuthorizer{C: c}, Audit: aw,
		Events: &audit.EventSink{C: c, Scheme: scheme, Component: "fleetops-approvals", Red: red},
	}
	if err := rec.SetupWithManager(mgr); err != nil {
		log.Fatal(err)
	}
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		log.Fatal(err)
	}
}
