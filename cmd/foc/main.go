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

// Command foc is the operator and approver CLI. It acts with the caller's own
// kubeconfig credentials.
//
//	foc requests [-n NS]
//	foc approve REQUEST [-n NS] [-comment TEXT]
//	foc reject  REQUEST [-n NS] [-comment TEXT]
//	foc audit verify FILE
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	focv1 "github.com/AlexBahel/fleet-ops-copilot/api/v1alpha1"
	"github.com/AlexBahel/fleet-ops-copilot/internal/approval"
	"github.com/AlexBahel/fleet-ops-copilot/internal/audit"
	"github.com/AlexBahel/fleet-ops-copilot/internal/fleet"
)

const usage = `usage:
  foc requests [-n NAMESPACE]
  foc approve REQUEST [-n NAMESPACE] [-comment TEXT]
  foc reject  REQUEST [-n NAMESPACE] [-comment TEXT]
  foc audit verify FILE`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "foc:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("%s", usage)
	}
	ctx := context.Background()
	switch args[0] {
	case "audit":
		if len(args) != 3 || args[1] != "verify" {
			return fmt.Errorf("%s", usage)
		}
		f, err := os.Open(args[2])
		if err != nil {
			return err
		}
		defer func() { _ = f.Close() }()
		n, err := audit.Verify(f)
		if err != nil {
			return fmt.Errorf("%s: %w (after %d valid entries)", args[2], err, n)
		}
		fmt.Printf("%s: %d entries, chain intact\n", args[2], n)
		return nil
	case "requests":
		fs := flag.NewFlagSet("requests", flag.ContinueOnError)
		ns := fs.String("n", "warehouse-a", "namespace")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		c, err := client.New(ctrl.GetConfigOrDie(), client.Options{Scheme: fleet.NewScheme()})
		if err != nil {
			return err
		}
		var l focv1.ApprovalRequestList
		if err := c.List(ctx, &l, client.InNamespace(*ns)); err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "NAME\tTYPE\tFOR\tSTATE\tDECIDED BY\tEXPIRES")
		for _, r := range l.Items {
			st := r.Status.State
			if st == "" {
				st = focv1.StatePending
			}
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n", r.Name, r.Spec.Type, r.Spec.RequestedFor, st, r.Status.DecidedBy, r.Spec.ExpiresAt.UTC().Format("2006-01-02T15:04:05Z"))
		}
		return w.Flush()
	case "approve", "reject":
		if len(args) < 2 {
			return fmt.Errorf("%s", usage)
		}
		fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
		ns := fs.String("n", "warehouse-a", "namespace")
		comment := fs.String("comment", "", "comment recorded on the decision")
		if err := fs.Parse(args[2:]); err != nil {
			return err
		}
		a, err := approval.NewApprover(ctrl.GetConfigOrDie())
		if err != nil {
			return err
		}
		v := focv1.VerdictApprove
		if args[0] == "reject" {
			v = focv1.VerdictReject
		}
		out, err := a.Decide(ctx, *ns, args[1], v, *comment)
		if err != nil {
			return err
		}
		fmt.Printf("decision %s recorded. %s\n", out.Decision, out.Message)
		return nil
	}
	return fmt.Errorf("%s", usage)
}
