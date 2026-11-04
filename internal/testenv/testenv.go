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

// Package testenv starts a real kube-apiserver (controller-runtime envtest)
// with the copilot's CRDs, RBAC and admission policies, the Swarmada CRDs and
// Swarmada's own RBAC roles loaded from the Swarmada module. It is shared by
// the envtest suite and the fixture evaluations.
//
// Swarmada's controllers and webhooks do not run. A stand-in webhook applies
// Swarmada's documented FleetTask activation rule (clearing spec.draft is
// refused to every ServiceAccount and needs the activate verb;
// internal/webhook/fleettask_webhook.go in Swarmada). It is a stand-in, not
// Swarmada's code; it exists so the draft gate is present and fail-closed.
package testenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	"sigs.k8s.io/controller-runtime/pkg/webhook"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	swarmv1 "github.com/swarmada/swarmada/api/v1"

	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

// Identities used throughout the tests and evaluations.
const (
	SwarmNS     = "warehouse-a"
	FocNS       = "foc-system"
	MCPUser     = "system:serviceaccount:foc-system:foc-mcp"
	ApprovUser  = "system:serviceaccount:foc-system:foc-approvals"
	AgentUser   = "system:serviceaccount:foc-system:foc-agent"
	StandinName = "standin-swarmada-fleettask"
	standinPath = "/validate-fleettask-standin"
)

// Person is an authenticated user (client certificate) with RoleBindings in SwarmNS.
type Person struct {
	Name   string
	Groups []string
	Roles  []string // ClusterRoles bound in SwarmNS
}

// DefaultPeople: dana approves and holds swarmada:fleet-manager; sam is a
// shift supervisor (swarmada:operator) who also holds the approver role; eve
// holds only the approver role.
var DefaultPeople = []Person{
	{Name: "dana", Groups: []string{"fleet-ops"}, Roles: []string{"fleetops:approver", "swarmada:fleet-manager"}},
	{Name: "sam", Groups: []string{"supervisors"}, Roles: []string{"fleetops:approver", "swarmada:operator"}},
	{Name: "eve", Groups: []string{"auditors"}, Roles: []string{"fleetops:approver"}},
}

// Env is a running test API server.
type Env struct {
	Admin       client.Client
	AdminCfg    *rest.Config
	RepoRoot    string
	SwarmadaDir string
	people      map[string]*rest.Config
	env         *envtest.Environment
	cancel      context.CancelFunc
}

// SwarmadaDir returns the Swarmada module directory as resolved by the Go
// toolchain (go.mod pin, or the local workspace before the public release).
func SwarmadaDir(repoRoot string) (string, error) {
	cmd := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/swarmada/swarmada")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("locating the Swarmada module: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Start brings up the API server and installs everything. Call Stop when done.
func Start(repoRoot string, people []Person) (*Env, error) {
	sw, err := SwarmadaDir(repoRoot)
	if err != nil {
		return nil, err
	}
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	path := standinPath
	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd"), filepath.Join(sw, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionv1.ValidatingWebhookConfiguration{{
				ObjectMeta: metav1.ObjectMeta{Name: StandinName},
				TypeMeta:   metav1.TypeMeta{Kind: "ValidatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"},
				Webhooks: []admissionv1.ValidatingWebhook{{
					Name:                    "vfleettask.standin.test",
					AdmissionReviewVersions: []string{"v1"},
					SideEffects:             &none,
					FailurePolicy:           &fail,
					ClientConfig:            admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Name: "standin", Namespace: "default", Path: &path}},
					Rules: []admissionv1.RuleWithOperations{{
						Operations: []admissionv1.OperationType{admissionv1.Update},
						Rule:       admissionv1.Rule{APIGroups: []string{"swarmada.io"}, APIVersions: []string{"v1"}, Resources: []string{"fleettasks"}},
					}},
				}},
			}},
		},
	}
	cfg, err := te.Start()
	if err != nil {
		return nil, fmt.Errorf("starting envtest (is KUBEBUILDER_ASSETS set? see `make test-envtest`): %w", err)
	}
	e := &Env{AdminCfg: cfg, RepoRoot: repoRoot, SwarmadaDir: sw, env: te, people: map[string]*rest.Config{}}
	ok := false
	defer func() {
		if !ok {
			e.Stop()
		}
	}()
	if e.Admin, err = client.New(cfg, client.Options{Scheme: fleet.NewScheme()}); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	if err := e.startStandin(ctx); err != nil {
		return nil, err
	}
	for _, f := range []string{
		filepath.Join(repoRoot, "config", "rbac", "namespace_and_serviceaccounts.yaml"),
		filepath.Join(repoRoot, "config", "rbac", "roles.yaml"),
		filepath.Join(sw, "config", "rbac", "swarmada_fleet_manager_role.yaml"),
		filepath.Join(sw, "config", "rbac", "swarmada_operator_role.yaml"),
	} {
		if err := e.ApplyFile(ctx, f); err != nil {
			return nil, err
		}
	}
	if err := e.Admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: SwarmNS}}); err != nil {
		return nil, err
	}
	for _, f := range []string{filepath.Join(repoRoot, "config", "rbac", "bindings.yaml"), filepath.Join(repoRoot, "config", "admission", "policies.yaml")} {
		if err := e.ApplyFile(ctx, f); err != nil {
			return nil, err
		}
	}
	for _, p := range people {
		u, err := te.ControlPlane.AddUser(envtest.User{Name: p.Name, Groups: p.Groups}, nil)
		if err != nil {
			return nil, err
		}
		e.people[p.Name] = u.Config()
		for _, role := range p.Roles {
			rb := &rbacv1.RoleBinding{
				ObjectMeta: metav1.ObjectMeta{Name: p.Name + "-" + strings.ReplaceAll(role, ":", "-"), Namespace: SwarmNS},
				RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
				Subjects:   []rbacv1.Subject{{Kind: "User", Name: p.Name, APIGroup: "rbac.authorization.k8s.io"}},
			}
			if err := e.Admin.Create(ctx, rb); err != nil {
				return nil, err
			}
		}
	}
	if err := e.waitForPolicies(ctx); err != nil {
		return nil, err
	}
	ok = true
	return e, nil
}

// Stop shuts the API server down.
func (e *Env) Stop() {
	if e.cancel != nil {
		e.cancel()
	}
	_ = e.env.Stop()
}

// SAGroups are the groups the API server gives a copilot ServiceAccount.
func SAGroups() []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:" + FocNS, "system:authenticated"}
}

// SAConfig returns an admin config impersonating a ServiceAccount.
func (e *Env) SAConfig(user string) *rest.Config {
	cfg := rest.CopyConfig(e.AdminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: SAGroups()}
	return cfg
}

// SAClient returns a client impersonating a ServiceAccount.
func (e *Env) SAClient(user string) (client.Client, error) {
	return client.New(e.SAConfig(user), client.Options{Scheme: fleet.NewScheme()})
}

// PersonConfig returns a person's certificate-authenticated config.
func (e *Env) PersonConfig(name string) *rest.Config { return e.people[name] }

// ApplyFile creates every object in a multi-document YAML file.
func (e *Env) ApplyFile(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	objs, err := DecodeYAML(b)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	for _, u := range objs {
		if err := e.Admin.Create(ctx, u); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s: creating %s %s: %w", path, u.GetKind(), u.GetName(), err)
		}
	}
	return nil
}

// DecodeYAML splits a multi-document YAML stream into objects.
func DecodeYAML(b []byte) ([]*unstructured.Unstructured, error) {
	dec := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	var out []*unstructured.Unstructured
	for {
		doc, err := dec.Read()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := utilyaml.Unmarshal(doc, &u.Object); err != nil {
			return nil, err
		}
		if len(u.Object) > 0 {
			out = append(out, u)
		}
	}
}

// SetStandinFailurePolicy switches the stand-in draft gate between Fail and Ignore.
func (e *Env) SetStandinFailurePolicy(ctx context.Context, fp admissionv1.FailurePolicyType) error {
	var cfg admissionv1.ValidatingWebhookConfiguration
	if err := e.Admin.Get(ctx, client.ObjectKey{Name: StandinName}, &cfg); err != nil {
		return err
	}
	cfg.Webhooks[0].FailurePolicy = &fp
	return e.Admin.Update(ctx, &cfg)
}

// waitForPolicies blocks until the ValidatingAdmissionPolicies are enforced:
// the API server compiles and binds them asynchronously.
func (e *Env) waitForPolicies(ctx context.Context) error {
	c, err := e.SAClient(MCPUser)
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		probe := &swarmv1.FleetTask{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vap-probe-", Namespace: SwarmNS},
			Spec:       swarmv1.FleetTaskSpec{Actions: []swarmv1.FleetTaskAction{{Name: "a", Action: swarmv1.FleetActionSpec{Type: swarmv1.ActionTypeNavigate}}}},
		}
		err := c.Create(ctx, probe)
		if err != nil && strings.Contains(err.Error(), "spec.draft") {
			return nil
		}
		if err == nil {
			_ = e.Admin.Delete(ctx, probe)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return errors.New("admission policies not enforced after 30s")
}

func (e *Env) startStandin(ctx context.Context) error {
	o := e.env.WebhookInstallOptions
	srv := webhook.NewServer(webhook.Options{Host: o.LocalServingHost, Port: o.LocalServingPort, CertDir: o.LocalServingCertDir})
	srv.Register(standinPath, &webhook.Admission{Handler: admission.HandlerFunc(e.standinActivate)})
	go func() { _ = srv.Start(ctx) }()
	addr := fmt.Sprintf("https://%s:%d%s", o.LocalServingHost, o.LocalServingPort, standinPath)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: time.Second} //nolint:gosec // local readiness probe
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := hc.Get(addr); err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("stand-in webhook did not start")
}

func (e *Env) standinActivate(ctx context.Context, req admission.Request) admission.Response {
	var oldT, newT swarmv1.FleetTask
	if err := json.Unmarshal(req.OldObject.Raw, &oldT); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if err := json.Unmarshal(req.Object.Raw, &newT); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if !oldT.Spec.Draft || newT.Spec.Draft {
		return admission.Allowed("")
	}
	u := req.UserInfo
	if strings.HasPrefix(u.Username, "system:serviceaccount:") {
		return admission.Denied(fmt.Sprintf("%q is a ServiceAccount, and a ServiceAccount cannot activate draft FleetTask %q (stand-in for Swarmada's rule)", u.Username, newT.Name))
	}
	sar := &authzv1.SubjectAccessReview{Spec: authzv1.SubjectAccessReviewSpec{User: u.Username, Groups: u.Groups, ResourceAttributes: &authzv1.ResourceAttributes{
		Namespace: newT.Namespace, Group: "swarmada.io", Resource: "fleettasks", Name: newT.Name, Verb: "activate",
	}}}
	if err := e.Admin.Create(ctx, sar); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if !sar.Status.Allowed {
		return admission.Denied(fmt.Sprintf("user %q is not permitted to activate draft FleetTask %q (stand-in for Swarmada's rule)", u.Username, newT.Name))
	}
	return admission.Allowed("")
}
