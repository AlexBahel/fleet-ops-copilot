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

// Server-side settings of a llamacpp measurement (ruling R41). The request
// fields the backend sends are in llm.LlamaCpp.Settings; what decides the
// rest of the measurement is on the server side: the context size and slot
// count llama-server runs with, its build, the threads it chose, and the
// resources its pod was given. They are read, not asserted: from
// llama-server's GET /props, and with -llama-context from the Deployment, its
// running pod (and the pod's log) and the node in the cluster.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// llamaProps reads GET <base>/props and returns the fields that describe the
// running server, keyed "server.props.<field>".
func llamaProps(ctx context.Context, hc *http.Client, base string) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/props", nil)
	if err != nil {
		return nil, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llama-server /props: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llama-server /props: http %d", resp.StatusCode)
	}
	var p struct {
		Default struct {
			NCtx *int64 `json:"n_ctx"`
		} `json:"default_generation_settings"`
		TotalSlots *int64 `json:"total_slots"`
		BuildInfo  string `json:"build_info"`
		ModelPath  string `json:"model_path"`
		ModelAlias string `json:"model_alias"`
		ModelFtype string `json:"model_ftype"`
	}
	if err := json.Unmarshal(b, &p); err != nil {
		return nil, fmt.Errorf("llama-server /props: %w", err)
	}
	out := map[string]string{"server.props.readAt": time.Now().UTC().Format(time.RFC3339)}
	if p.Default.NCtx != nil {
		out["server.props.n_ctx"] = fmt.Sprint(*p.Default.NCtx)
	}
	if p.TotalSlots != nil {
		out["server.props.total_slots"] = fmt.Sprint(*p.TotalSlots)
	}
	for k, v := range map[string]string{"build_info": p.BuildInfo, "model_path": p.ModelPath, "model_alias": p.ModelAlias, "model_ftype": p.ModelFtype} {
		if v != "" {
			out["server.props."+k] = v
		}
	}
	return out, nil
}

// threadsLine matches llama.cpp's startup line naming the thread count, for
// example "llama threadpool init, n_threads = 12" or
// "system_info: n_threads = 8 (n_threads_batch = 8) / 8".
var threadsLine = regexp.MustCompile(`\bn_threads = (\d+)`)

// threadsFromLog returns the first thread count llama-server logged and the
// line it came from.
func threadsFromLog(r io.Reader) (string, string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if m := threadsLine.FindStringSubmatch(sc.Text()); m != nil {
			return m[1], strings.TrimSpace(sc.Text())
		}
	}
	return "", ""
}

// llamaDeployment reads the llama-server Deployment, its one running pod and
// that pod's node. Server values are keyed "server.*", node values "node.*".
func llamaDeployment(ctx context.Context, cs kubernetes.Interface, ns, name string) (server, node map[string]string, err error) {
	d, err := cs.AppsV1().Deployments(ns).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	var c *corev1.Container
	for i := range d.Spec.Template.Spec.Containers {
		if d.Spec.Template.Spec.Containers[i].Name == name {
			c = &d.Spec.Template.Spec.Containers[i]
		}
	}
	if c == nil {
		return nil, nil, fmt.Errorf("deployment %s/%s has no container %q", ns, name, name)
	}
	server = map[string]string{
		"server.deployment": ns + "/" + name,
		"server.image":      c.Image,
		"server.args":       strings.Join(c.Args, " "),
	}
	for _, r := range []struct {
		kind string
		list corev1.ResourceList
	}{{"requests", c.Resources.Requests}, {"limits", c.Resources.Limits}} {
		for _, res := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			v := "none"
			if q, ok := r.list[res]; ok {
				v = q.String()
			}
			server["server.resources."+r.kind+"."+string(res)] = v
		}
	}
	sel := labels.SelectorFromSet(d.Spec.Selector.MatchLabels).String()
	pods, err := cs.CoreV1().Pods(ns).List(ctx, metav1.ListOptions{LabelSelector: sel})
	if err != nil {
		return nil, nil, err
	}
	var running []corev1.Pod
	for _, p := range pods.Items {
		if p.Status.Phase == corev1.PodRunning && p.DeletionTimestamp == nil {
			running = append(running, p)
		}
	}
	if len(running) != 1 {
		return nil, nil, fmt.Errorf("deployment %s/%s: %d running pods, want 1", ns, name, len(running))
	}
	p := running[0]
	server["server.pod"] = p.Name
	for _, st := range p.Status.ContainerStatuses {
		if st.Name == name {
			server["server.imageID"] = st.ImageID
			server["server.restarts"] = fmt.Sprint(st.RestartCount)
			if st.State.Running != nil {
				server["server.startedAt"] = st.State.Running.StartedAt.UTC().Format(time.RFC3339)
			}
		}
	}
	server["server.threads"] = "not found in the pod log"
	if rc, err := cs.CoreV1().Pods(ns).GetLogs(p.Name, &corev1.PodLogOptions{Container: name}).Stream(ctx); err == nil {
		if n, line := threadsFromLog(rc); n != "" {
			server["server.threads"] = n
			server["server.threadsLogLine"] = line
		}
		_ = rc.Close()
	} else {
		server["server.threads"] = "pod log not readable: " + err.Error()
	}
	n, err := cs.CoreV1().Nodes().Get(ctx, p.Spec.NodeName, metav1.GetOptions{})
	if err != nil {
		return nil, nil, err
	}
	node = map[string]string{
		"node.name":             n.Name,
		"node.capacity.cpu":     n.Status.Capacity.Cpu().String(),
		"node.capacity.memory":  n.Status.Capacity.Memory().String(),
		"node.kubelet":          n.Status.NodeInfo.KubeletVersion,
		"node.os":               n.Status.NodeInfo.OSImage + ", kernel " + n.Status.NodeInfo.KernelVersion + ", " + n.Status.NodeInfo.Architecture,
		"node.containerRuntime": n.Status.NodeInfo.ContainerRuntimeVersion,
	}
	return server, node, nil
}

// hostHardware describes the machine fleetops-eval runs on (and, for a kind
// cluster, the machine the node's VM runs on), keyed "host.*". Best effort:
// a value that cannot be read is left out.
func hostHardware() map[string]string {
	out := map[string]string{"host.goos": runtime.GOOS + "/" + runtime.GOARCH}
	read := func(key string, cmd *exec.Cmd) {
		if b, err := cmd.Output(); err == nil {
			if v := strings.TrimSpace(string(b)); v != "" {
				out[key] = v
			}
		}
	}
	switch runtime.GOOS {
	case "darwin":
		read("host.model", exec.Command("sysctl", "-n", "hw.model"))
		read("host.cpu", exec.Command("sysctl", "-n", "machdep.cpu.brand_string"))
		read("host.cpus", exec.Command("sysctl", "-n", "hw.ncpu"))
		read("host.memoryBytes", exec.Command("sysctl", "-n", "hw.memsize"))
		read("host.os", exec.Command("sw_vers", "-productVersion"))
	case "linux":
		read("host.cpus", exec.Command("nproc"))
		read("host.os", exec.Command("uname", "-sr"))
	}
	return out
}

// mergeInto copies src into dst.
func mergeInto(dst, src map[string]string) {
	for k, v := range src {
		dst[k] = v
	}
}
