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

// Command fleetops-mcp serves the copilot's tools over MCP (streamable HTTP or stdio).
//
// The requester of every call is the username of the caller's bearer token
// (Authorization header), verified with a TokenReview; without a verified
// token it is "unverified:<asserted name>", which is never approved (D57).
// Over stdio there is no header, so every requester is unverified.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/authn"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
	"github.com/AlexBahel/fleet-ops-copilot/internal/mcpserver"
	"github.com/AlexBahel/fleet-ops-copilot/internal/policy"
	"github.com/AlexBahel/fleet-ops-copilot/internal/redact"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tools"
	"github.com/AlexBahel/fleet-ops-copilot/internal/tracing"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func defaultIdentity() string {
	ns, sa := os.Getenv("POD_NAMESPACE"), os.Getenv("SERVICE_ACCOUNT")
	if ns == "" || sa == "" {
		return ""
	}
	return "system:serviceaccount:" + ns + ":" + sa
}

func run() error {
	policyPath := flag.String("policy", "policies/default.yaml", "policy file")
	auditPath := flag.String("audit", "fleetops-mcp-audit.jsonl", "JSONL audit file")
	namespace := flag.String("namespace", "warehouse-a", "default Swarmada namespace")
	listen := flag.String("listen", ":8080", "HTTP listen address for streamable MCP")
	stdio := flag.Bool("stdio", false, "serve MCP on stdin/stdout instead of HTTP")
	identity := flag.String("identity", defaultIdentity(), "principal fleetops-mcp runs as (spec.requestedBy); defaults from POD_NAMESPACE and SERVICE_ACCOUNT")
	otlp := flag.String("otlp-endpoint", tracing.EndpointFromEnv(), "OTLP/gRPC collector for tool and policy-decision spans (host:port, http:// or https://); empty disables tracing")
	flag.Parse()
	if *identity == "" {
		return errors.New("--identity is required (or set POD_NAMESPACE and SERVICE_ACCOUNT)")
	}

	p, err := policy.Load(*policyPath)
	if err != nil {
		return err
	}
	red := redact.FromEnv()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tp, shutdownTracing, err := tracing.Setup(ctx, tracing.Config{Endpoint: *otlp, ServiceName: "fleetops-mcp", Redactor: red})
	if err != nil {
		return err
	}
	defer func() {
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = shutdownTracing(sctx)
	}()
	aw, err := audit.Open(*auditPath, red)
	if err != nil {
		return err
	}
	defer func() { _ = aw.Close() }()
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	scheme := fleet.NewScheme()
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return err
	}
	svc := &tools.Service{
		C: c, Engine: policy.New(p), Audit: aw, Identity: *identity, DefaultNamespace: *namespace,
		Events: &audit.EventSink{C: c, Scheme: scheme, Component: "fleetops-mcp", Red: red},
		Tracer: tracing.Tracer(tp),
		Authn:  &authn.TokenReviewer{C: c},
	}
	srv := mcpserver.New(svc)
	if *stdio {
		return srv.Run(ctx, &mcp.StdioTransport{})
	}
	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpserver.Handler(srv))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprintln(w, "ok") })
	hs := &http.Server{Addr: *listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Printf("fleetops-mcp %s listening on %s/mcp as %s", mcpserver.Version, *listen, *identity)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
