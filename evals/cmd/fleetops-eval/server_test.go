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

package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// propsBody is the shape llama-server b12004 returned on a kind cluster
// (fields not read are cut).
const propsBody = `{"default_generation_settings":{"params":{"temperature":0.8},"n_ctx":8192},"total_slots":1,
"model_alias":"qwen2.5-7b-instruct","model_ftype":"Q4_K - Medium",
"model_path":"/models/qwen2.5-7b-instruct-q4_k_m-00001-of-00002.gguf","build_info":"b12004-3c809f443"}`

func TestLlamaProps(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/props" || r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(propsBody))
	}))
	defer ts.Close()
	got, err := llamaProps(context.Background(), ts.Client(), ts.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"server.props.n_ctx": "8192", "server.props.total_slots": "1", "server.props.build_info": "b12004-3c809f443",
		"server.props.model_ftype": "Q4_K - Medium", "server.props.model_alias": "qwen2.5-7b-instruct",
		"server.props.model_path": "/models/qwen2.5-7b-instruct-q4_k_m-00001-of-00002.gguf",
	} {
		if got[k] != want {
			t.Errorf("%s = %q, want %q", k, got[k], want)
		}
	}
	if got["server.props.readAt"] == "" {
		t.Error("server.props.readAt not set")
	}
}

func TestLlamaPropsErrors(t *testing.T) {
	for name, h := range map[string]http.HandlerFunc{
		"status":   func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "loading", http.StatusServiceUnavailable) },
		"not json": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		ts := httptest.NewServer(h)
		if _, err := llamaProps(context.Background(), ts.Client(), ts.URL); err == nil {
			t.Errorf("%s: no error", name)
		}
		ts.Close()
	}
}

func TestThreadsFromLog(t *testing.T) {
	for log, want := range map[string]string{
		"load_tensors: ...\n1.27.909.496 I cmn          init: llama threadpool init, n_threads = 12\nmain: model loaded\n": "12",
		"system_info: n_threads = 8 (n_threads_batch = 8) / 8 | CPU : NEON = 1\n":                                          "8",
		"no thread line here\n": "",
	} {
		if got, line := threadsFromLog(strings.NewReader(log)); got != want || (want != "" && !strings.Contains(line, "n_threads = "+want)) {
			t.Errorf("threadsFromLog(%q) = %q, %q; want %q", log, got, line, want)
		}
	}
}

func TestLlamaDeployment(t *testing.T) {
	sel := map[string]string{"app.kubernetes.io/name": "llama-server"}
	d := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "llama-server", Namespace: "fleetops-system"},
		Spec: appsv1.DeploymentSpec{
			Selector: &metav1.LabelSelector{MatchLabels: sel},
			Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name: "llama-server", Image: "ghcr.io/ggml-org/llama.cpp:server-b12004",
				Args: []string{"--ctx-size", "8192", "--parallel", "1"},
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourceMemory: resource.MustParse("5Gi")},
					Limits:   corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("7Gi")},
				},
			}}}},
		},
	}
	pod := func(name string, phase corev1.PodPhase) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "fleetops-system", Labels: sel},
			Spec:       corev1.PodSpec{NodeName: "foc-up-control-plane"},
			Status: corev1.PodStatus{Phase: phase, ContainerStatuses: []corev1.ContainerStatus{{
				Name: "llama-server", ImageID: "ghcr.io/ggml-org/llama.cpp@sha256:abc", RestartCount: 0,
			}}},
		}
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "foc-up-control-plane"},
		Status: corev1.NodeStatus{
			Capacity: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourceMemory: resource.MustParse("22526144Ki")},
			NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.37.0", OSImage: "Debian", KernelVersion: "6.12", Architecture: "arm64", ContainerRuntimeVersion: "containerd://2.3.4"},
		},
	}
	cs := fake.NewSimpleClientset(d, pod("llama-server-a", corev1.PodRunning), pod("llama-server-old", corev1.PodFailed), node)
	srv, n, err := llamaDeployment(context.Background(), cs, "fleetops-system", "llama-server")
	if err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{
		"server.args": "--ctx-size 8192 --parallel 1", "server.image": "ghcr.io/ggml-org/llama.cpp:server-b12004",
		"server.imageID": "ghcr.io/ggml-org/llama.cpp@sha256:abc", "server.pod": "llama-server-a",
		"server.resources.requests.cpu": "2", "server.resources.requests.memory": "5Gi",
		"server.resources.limits.cpu": "none", "server.resources.limits.memory": "7Gi",
		// The fake clientset's log is "fake logs": no thread line.
		"server.threads": "not found in the pod log",
	} {
		if srv[k] != want {
			t.Errorf("%s = %q, want %q", k, srv[k], want)
		}
	}
	if n["node.capacity.cpu"] != "8" || n["node.capacity.memory"] != "22526144Ki" || n["node.kubelet"] != "v1.37.0" {
		t.Errorf("node = %v", n)
	}

	two := fake.NewSimpleClientset(d, pod("a", corev1.PodRunning), pod("b", corev1.PodRunning), node)
	if _, _, err := llamaDeployment(context.Background(), two, "fleetops-system", "llama-server"); err == nil {
		t.Error("two running pods: no error")
	}
}
