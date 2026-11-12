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
// Swarmada's controllers and webhooks do not run: Swarmada's webhook code is in
// its internal/webhook package, which another module cannot import. Two
// stand-in webhooks reproduce the two Swarmada admission behaviours the
// copilot depends on, from that code; they are stand-ins, not Swarmada's code:
//
//   - FleetTask activation (validating): clearing spec.draft is refused to every
//     ServiceAccount and needs the activate verb (authorizeActivate,
//     internal/webhook/fleettask_webhook.go).
//   - FleetZone estop actor (mutating): on an estop-triggered transition the
//     authenticated user is stamped into swarmada.io/estop-actor, and while
//     triggered the swarmada.io/estop-in-force finalizer is added (stampEstopActor,
//     internal/webhook/estop_actor_defaulter.go).
package testenv

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
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
	authnv1 "k8s.io/api/authentication/v1"
	authzv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/discovery"
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
	SwarmNS          = "warehouse-a"
	FleetopsNS       = "fleetops-system"
	MCPUser          = "system:serviceaccount:fleetops-system:fleetops-mcp"
	ApprovUser       = "system:serviceaccount:fleetops-system:fleetops-approvals"
	AgentUser        = "system:serviceaccount:fleetops-system:fleetops-agent"
	StandinName      = "standin-swarmada-fleettask"
	standinPath      = "/validate-fleettask-standin"
	standinZoneName  = "standin-swarmada-fleetzone"
	standinZonePath  = "/mutate-fleetzone-standin"
	EstopActorAnn    = "swarmada.io/estop-actor"
	EstopInForceFinz = "swarmada.io/estop-in-force"
	// OperatorNS holds one ServiceAccount per eval operator; the harness mints
	// its bearer token with the TokenRequest API (D57).
	OperatorNS = "fleetops-eval-operators"
)

// OperatorIdentity is the username the API server gives the token the
// harness mints for a scenario operator.
func OperatorIdentity(operator string) string {
	return "system:serviceaccount:" + OperatorNS + ":" + operator
}

// Person is an authenticated user with RoleBindings in SwarmNS. Each one has
// a client certificate and a static bearer token (--token-auth-file) for the
// same username and groups, so a person can also be the verified requester
// of a tool call (D57).
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
	tokens      map[string]string
	tokenDir    string
	env         *envtest.Environment
	cancel      context.CancelFunc
}

// SwarmadaDirEnv overrides where the Swarmada module (its config/crd/bases
// and config/rbac) is read from.
const SwarmadaDirEnv = "FLEETOPS_SWARMADA_DIR"

// SwarmadaDir returns the Swarmada module directory: $FLEETOPS_SWARMADA_DIR if
// set, else `go list -m -f '{{.Dir}}' github.com/swarmada/swarmada` (the
// go.mod pin, or a local go.work that overrides it). No path is
// stored in the repository.
func SwarmadaDir(repoRoot string) (string, error) {
	if d := os.Getenv(SwarmadaDirEnv); d != "" {
		if _, err := os.Stat(filepath.Join(d, "config", "crd", "bases")); err != nil {
			return "", fmt.Errorf("%s=%s: %w", SwarmadaDirEnv, d, err)
		}
		return d, nil
	}
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
	ignore := admissionv1.Ignore
	none := admissionv1.SideEffectClassNone
	path := standinPath
	zpath := standinZonePath
	te := &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join(repoRoot, "config", "crd"), filepath.Join(sw, "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
		WebhookInstallOptions: envtest.WebhookInstallOptions{
			MutatingWebhooks: []*admissionv1.MutatingWebhookConfiguration{{
				ObjectMeta: metav1.ObjectMeta{Name: standinZoneName},
				TypeMeta:   metav1.TypeMeta{Kind: "MutatingWebhookConfiguration", APIVersion: "admissionregistration.k8s.io/v1"},
				Webhooks: []admissionv1.MutatingWebhook{{
					Name:                    "mfleetzone.standin.test",
					AdmissionReviewVersions: []string{"v1"},
					SideEffects:             &none,
					FailurePolicy:           &ignore, // as Swarmada's mfleetzone.swarmada.io
					ClientConfig:            admissionv1.WebhookClientConfig{Service: &admissionv1.ServiceReference{Name: "standin", Namespace: "default", Path: &zpath}},
					Rules: []admissionv1.RuleWithOperations{{
						Operations: []admissionv1.OperationType{admissionv1.Create, admissionv1.Update},
						Rule:       admissionv1.Rule{APIGroups: []string{"swarmada.io"}, APIVersions: []string{"v1"}, Resources: []string{"fleetzones"}},
					}},
				}},
			}},
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
	tokens, tokenDir, err := writeTokenFile(people)
	if err != nil {
		return nil, err
	}
	te.ControlPlane.GetAPIServer().Configure().Set("token-auth-file", filepath.Join(tokenDir, "tokens.csv"))
	cfg, err := te.Start()
	if err != nil {
		_ = os.RemoveAll(tokenDir)
		return nil, fmt.Errorf("starting envtest (is KUBEBUILDER_ASSETS set? see `make test-envtest`): %w", err)
	}
	e := &Env{AdminCfg: cfg, RepoRoot: repoRoot, SwarmadaDir: sw, env: te, people: map[string]*rest.Config{}, tokens: tokens, tokenDir: tokenDir}
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
	if e.tokenDir != "" {
		_ = os.RemoveAll(e.tokenDir)
	}
}

// writeTokenFile writes a kube-apiserver static token file with a random
// token per person (username and groups as in the person's certificate).
func writeTokenFile(people []Person) (map[string]string, string, error) {
	dir, err := os.MkdirTemp("", "fleetops-envtest-tokens-")
	if err != nil {
		return nil, "", err
	}
	tokens := map[string]string{}
	var b strings.Builder
	for _, p := range people {
		raw := make([]byte, 24)
		if _, err := rand.Read(raw); err != nil {
			return nil, "", err
		}
		tok := hex.EncodeToString(raw)
		tokens[p.Name] = tok
		fmt.Fprintf(&b, "%s,%s,%s,\"%s\"\n", tok, p.Name, p.Name, strings.Join(p.Groups, ","))
	}
	if err := os.WriteFile(filepath.Join(dir, "tokens.csv"), []byte(b.String()), 0o600); err != nil {
		return nil, "", err
	}
	return tokens, dir, nil
}

// Discovery is a discovery client as the admin.
func (e *Env) Discovery() discovery.DiscoveryInterface {
	return discovery.NewDiscoveryClientForConfigOrDie(e.AdminCfg)
}

// PersonToken returns a person's bearer token (static token file).
func (e *Env) PersonToken(name string) string { return e.tokens[name] }

// MintToken returns a bearer token for ServiceAccount ns/name from the
// TokenRequest API, creating the namespace and ServiceAccount if needed.
func (e *Env) MintToken(ctx context.Context, ns, name string) (string, error) {
	for _, obj := range []client.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}},
		&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}},
	} {
		if err := e.Admin.Create(ctx, obj); err != nil && !apierrors.IsAlreadyExists(err) {
			return "", err
		}
	}
	exp := int64(3600)
	tr := &authnv1.TokenRequest{Spec: authnv1.TokenRequestSpec{ExpirationSeconds: &exp}}
	sa := &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}}
	if err := e.Admin.SubResource("token").Create(ctx, sa, tr); err != nil {
		return "", fmt.Errorf("TokenRequest for %s/%s: %w", ns, name, err)
	}
	return tr.Status.Token, nil
}

// SAGroups are the groups the API server gives a copilot ServiceAccount.
func SAGroups() []string {
	return []string{"system:serviceaccounts", "system:serviceaccounts:" + FleetopsNS, "system:authenticated"}
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
	c, err := e.SAClient(ApprovUser)
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
	srv.Register(standinZonePath, &webhook.Admission{Handler: admission.HandlerFunc(standinEstopActor)})
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

// standinEstopActor mirrors Swarmada's stampEstopActor: on a transition of
// swarmada.io/estop-triggered it records the admission user in
// swarmada.io/estop-actor, and while the annotation is present it holds the
// swarmada.io/estop-in-force finalizer.
func standinEstopActor(_ context.Context, req admission.Request) admission.Response {
	var obj, old swarmv1.FleetZone
	if err := json.Unmarshal(req.Object.Raw, &obj); err != nil {
		return admission.Errored(http.StatusBadRequest, err)
	}
	if len(req.OldObject.Raw) > 0 {
		if err := json.Unmarshal(req.OldObject.Raw, &old); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}
	}
	oldVal, had := old.Annotations[fleet.EstopAnnotation]
	newVal, has := obj.Annotations[fleet.EstopAnnotation]
	if had == has && oldVal == newVal {
		return admission.Allowed("")
	}
	if obj.Annotations == nil {
		obj.Annotations = map[string]string{}
	}
	obj.Annotations[EstopActorAnn] = req.UserInfo.Username
	if has {
		held := false
		for _, f := range obj.Finalizers {
			held = held || f == EstopInForceFinz
		}
		if !held {
			obj.Finalizers = append(obj.Finalizers, EstopInForceFinz)
		}
	}
	b, err := json.Marshal(&obj)
	if err != nil {
		return admission.Errored(http.StatusInternalServerError, err)
	}
	return admission.PatchResponseFromRaw(req.Object.Raw, b)
}
