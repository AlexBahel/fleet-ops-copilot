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
// path against a real kube-apiserver (controller-runtime envtest), with the
// Swarmada CRDs and Swarmada's own RBAC roles loaded from the Swarmada module.
//
// Swarmada's controllers and webhooks do not run here. Where a test needs
// Swarmada's FleetTask activation rule, a stand-in webhook in this file
// applies the rule as documented in Swarmada (internal/webhook/fleettask_webhook.go:
// clearing spec.draft is refused to every ServiceAccount and needs the
// activate verb). It is a stand-in, not Swarmada's code.
package envtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
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

const (
	swarmNS     = "warehouse-a"
	focNS       = "foc-system"
	mcpUser     = "system:serviceaccount:foc-system:foc-mcp"
	approvUser  = "system:serviceaccount:foc-system:foc-approvals"
	agentUser   = "system:serviceaccount:foc-system:foc-agent"
	standinPath = "/validate-fleettask-standin"
)

var (
	testEnv  *envtest.Environment
	adminCfg *rest.Config
	admin    client.Client
	people   = map[string]*rest.Config{}
	repoRoot string
)

func saGroups() []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:" + focNS, "system:authenticated"}
}

// asSA returns a client that impersonates a copilot ServiceAccount.
func asSA(t testing.TB, user string) client.Client {
	t.Helper()
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: saGroups()}
	c, err := client.New(cfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func saConfig(user string) *rest.Config {
	cfg := rest.CopyConfig(adminCfg)
	cfg.Impersonate = rest.ImpersonationConfig{UserName: user, Groups: saGroups()}
	return cfg
}

// asPerson returns a client authenticated as an envtest user (client certificate).
func asPerson(t testing.TB, name string) client.Client {
	t.Helper()
	c, err := client.New(people[name], client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func swarmadaDir() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/swarmada/swarmada").Output()
	if err != nil {
		return "", fmt.Errorf("locating the Swarmada module: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "envtest setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	wd, _ := os.Getwd()
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))
	sw, err := swarmadaDir()
	if err != nil {
		return 0, err
	}
	fail := admissionv1.Fail
	none := admissionv1.SideEffectClassNone
	path := standinPath
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd"), filepath.Join(sw, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			ValidatingWebhooks: []*admissionv1.ValidatingWebhookConfiguration{{
				ObjectMeta: metav1.ObjectMeta{Name: "standin-swarmada-fleettask"},
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
	adminCfg, err = testEnv.Start()
	if err != nil {
		return 0, fmt.Errorf("starting envtest (is KUBEBUILDER_ASSETS set? run `make test-envtest`): %w", err)
	}
	defer func() { _ = testEnv.Stop() }()
	admin, err = client.New(adminCfg, client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		return 0, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := startStandinWebhook(ctx); err != nil {
		return 0, err
	}
	for _, f := range []string{
		filepath.Join(repoRoot, "config", "rbac", "namespace_and_serviceaccounts.yaml"),
		filepath.Join(repoRoot, "config", "rbac", "roles.yaml"),
		filepath.Join(sw, "config", "rbac", "swarmada_fleet_manager_role.yaml"),
		filepath.Join(sw, "config", "rbac", "swarmada_operator_role.yaml"),
	} {
		if err := applyFile(ctx, f); err != nil {
			return 0, err
		}
	}
	if err := admin.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: swarmNS}}); err != nil {
		return 0, err
	}
	for _, f := range []string{
		filepath.Join(repoRoot, "config", "rbac", "bindings.yaml"),
		filepath.Join(repoRoot, "config", "admission", "policies.yaml"),
	} {
		if err := applyFile(ctx, f); err != nil {
			return 0, err
		}
	}
	// People. dana: fleet-operations engineer and approver. sam: shift
	// supervisor (operator) who also holds the approver role. eve: approver
	// role only, no Swarmada write permission.
	for name, groups := range map[string][]string{"dana": {"fleet-ops"}, "sam": {"supervisors"}, "eve": {"auditors"}} {
		u, err := testEnv.ControlPlane.AddUser(envtest.User{Name: name, Groups: groups}, nil)
		if err != nil {
			return 0, err
		}
		people[name] = u.Config()
	}
	bind := func(name, role, user string) error {
		return admin.Create(ctx, &rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: swarmNS},
			RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: role},
			Subjects:   []rbacv1.Subject{{Kind: "User", Name: user, APIGroup: "rbac.authorization.k8s.io"}},
		})
	}
	for _, b := range [][3]string{
		{"dana-approver", "fleetops:approver", "dana"}, {"dana-fleet-manager", "swarmada:fleet-manager", "dana"},
		{"sam-approver", "fleetops:approver", "sam"}, {"sam-operator", "swarmada:operator", "sam"},
		{"eve-approver", "fleetops:approver", "eve"},
	} {
		if err := bind(b[0], b[1], b[2]); err != nil {
			return 0, err
		}
	}
	if err := waitForPolicies(ctx); err != nil {
		return 0, err
	}
	return m.Run(), nil
}

func applyFile(ctx context.Context, path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := utilyaml.NewYAMLReader(bufio.NewReader(bytes.NewReader(b)))
	for {
		doc, err := dec.Read()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if len(bytes.TrimSpace(doc)) == 0 {
			continue
		}
		u := &unstructured.Unstructured{}
		if err := utilyaml.Unmarshal(doc, &u.Object); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if len(u.Object) == 0 {
			continue
		}
		if err := admin.Create(ctx, u); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("%s: creating %s %s: %w", path, u.GetKind(), u.GetName(), err)
		}
	}
}

// waitForPolicies blocks until the ValidatingAdmissionPolicies are enforced:
// the API server compiles and binds them asynchronously.
func waitForPolicies(ctx context.Context) error {
	c, err := client.New(saConfig(mcpUser), client.Options{Scheme: fleet.NewScheme()})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		probe := &swarmv1.FleetTask{
			ObjectMeta: metav1.ObjectMeta{GenerateName: "vap-probe-", Namespace: swarmNS},
			Spec:       swarmv1.FleetTaskSpec{Draft: false, Actions: []swarmv1.FleetTaskAction{{Name: "a", Action: swarmv1.FleetActionSpec{Type: swarmv1.ActionTypeNavigate}}}},
		}
		err := c.Create(ctx, probe)
		if err != nil && strings.Contains(err.Error(), "spec.draft") {
			return nil
		}
		if err == nil {
			_ = admin.Delete(ctx, probe)
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("admission policies not enforced after 30s")
}

// startStandinWebhook serves the stand-in for Swarmada's FleetTask
// activation rule.
func startStandinWebhook(ctx context.Context) error {
	o := testEnv.WebhookInstallOptions
	srv := webhook.NewServer(webhook.Options{Host: o.LocalServingHost, Port: o.LocalServingPort, CertDir: o.LocalServingCertDir})
	srv.Register(standinPath, &webhook.Admission{Handler: admission.HandlerFunc(standinActivate)})
	go func() { _ = srv.Start(ctx) }()
	// Wait until it serves TLS.
	deadline := time.Now().Add(15 * time.Second)
	addr := fmt.Sprintf("https://%s:%d%s", o.LocalServingHost, o.LocalServingPort, standinPath)
	hc := &http.Client{Transport: &http.Transport{TLSClientConfig: insecureTLS()}, Timeout: time.Second}
	for time.Now().Before(deadline) {
		if resp, err := hc.Get(addr); err == nil {
			_ = resp.Body.Close()
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("stand-in webhook did not start")
}

func standinActivate(ctx context.Context, req admission.Request) admission.Response {
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
	if err := admin.Create(ctx, sar); err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	if !sar.Status.Allowed {
		return admission.Denied(fmt.Sprintf("user %q is not permitted to activate draft FleetTask %q (stand-in for Swarmada's rule)", u.Username, newT.Name))
	}
	return admission.Allowed("")
}
