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

package harness

import (
	"encoding/json"
	"io"

	"github.com/AlexBahel/fleet-ops-copilot/internal/agent"
)

func writeTrace(w io.Writer, id string, run int, tr agent.Trace, v Verdict) error {
	b, err := json.Marshal(struct {
		Scenario string      `json:"scenario"`
		Run      int         `json:"run"`
		Trace    agent.Trace `json:"trace"`
		Verdict  Verdict     `json:"verdict"`
	}{id, run, tr, v})
	if err != nil {
		return err
	}
	_, err = w.Write(append(b, '\n'))
	return err
}
