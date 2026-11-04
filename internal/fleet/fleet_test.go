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

package fleet

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func vwc(fp *admissionv1.FailurePolicyType, groups, resources []string, ops ...admissionv1.OperationType) *admissionv1.ValidatingWebhookConfiguration {
	return &admissionv1.ValidatingWebhookConfiguration{
		ObjectMeta: metav1.ObjectMeta{Name: "cfg"},
		Webhooks: []admissionv1.ValidatingWebhook{{Name: "wh", FailurePolicy: fp, Rules: []admissionv1.RuleWithOperations{{
			Operations: ops, Rule: admissionv1.Rule{APIGroups: groups, Resources: resources},
		}}}},
	}
}

func TestDraftGate(t *testing.T) {
	fail, ignore := admissionv1.Fail, admissionv1.Ignore
	sw, ft := []string{"swarmada.io"}, []string{"fleettasks"}
	cases := []struct {
		name string
		cfg  *admissionv1.ValidatingWebhookConfiguration
		want bool
	}{
		{"none", nil, false},
		{"swarmada shape (CREATE+UPDATE, Fail)", vwc(&fail, sw, ft, admissionv1.Create, admissionv1.Update), true},
		{"failurePolicy unset defaults to Fail", vwc(nil, sw, ft, admissionv1.Update), true},
		{"Ignore does not count", vwc(&ignore, sw, ft, admissionv1.Update), false},
		{"CREATE only does not count", vwc(&fail, sw, ft, admissionv1.Create), false},
		{"other resource", vwc(&fail, sw, []string{"fleetzones"}, admissionv1.Update), false},
		{"other group", vwc(&fail, []string{"example.com"}, ft, admissionv1.Update), false},
		{"wildcards", vwc(&fail, []string{"*"}, []string{"*"}, admissionv1.OperationAll), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := fake.NewClientBuilder().WithScheme(NewScheme())
			if tc.cfg != nil {
				b = b.WithObjects(tc.cfg)
			}
			got, _, err := DraftGate(context.Background(), b.Build())
			if err != nil || got != tc.want {
				t.Fatalf("DraftGate = %v, %v; want %v", got, err, tc.want)
			}
		})
	}
}
