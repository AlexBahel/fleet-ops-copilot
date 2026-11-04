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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RequestType names the single Swarmada write an approved request leads to.
// +kubebuilder:validation:Enum=DraftTask;ActivateTask;ZoneMaintenance;ZoneStop
type RequestType string

const (
	// RequestDraftTask asks for a FleetTask to be created held as a draft (spec.draft: true).
	RequestDraftTask RequestType = "DraftTask"
	// RequestActivateTask asks a person to clear spec.draft on a draft FleetTask.
	RequestActivateTask RequestType = "ActivateTask"
	// RequestZoneMaintenance asks for a ZoneMaintenance to be created.
	RequestZoneMaintenance RequestType = "ZoneMaintenance"
	// RequestZoneStop asks for the swarmada.io/estop-triggered annotation on a FleetZone.
	RequestZoneStop RequestType = "ZoneStop"
)

// DraftAction is one member of a drafted FleetTask, limited to the
// FleetActionSpec fields the copilot may set.
type DraftAction struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// Type is a Swarmada FleetAction spec.type.
	// +kubebuilder:validation:MinLength=1
	Type string `json:"type"`
	// +optional
	Zone string `json:"zone,omitempty"`
	// +optional
	Priority string `json:"priority,omitempty"`
	// +optional
	RequiredCapabilities []string `json:"requiredCapabilities,omitempty"`
	// +optional
	PreferredRobot string `json:"preferredRobot,omitempty"`
	// +optional
	DependsOn []string `json:"dependsOn,omitempty"`
}

// DraftTaskParams is the FleetTask foc-approvals creates, held as a draft,
// after a named person approves.
type DraftTaskParams struct {
	// TaskName is the FleetTask name in the request's namespace.
	// +kubebuilder:validation:MinLength=1
	TaskName string `json:"taskName"`
	// Purpose is shown to the person who reviews the draft.
	// +optional
	Purpose string `json:"purpose,omitempty"`
	// +kubebuilder:validation:MinItems=1
	Actions []DraftAction `json:"actions"`
}

// ActivateTaskParams names the draft FleetTask to activate.
type ActivateTaskParams struct {
	// TaskName is the draft FleetTask in the request's namespace.
	// +kubebuilder:validation:MinLength=1
	TaskName string `json:"taskName"`
}

// ZoneMaintenanceParams are the ZoneMaintenance fields the copilot may set.
// autoResumeAfterMinutes is not a parameter: the copilot always writes 0.
type ZoneMaintenanceParams struct {
	// ScopeType is Zone or Namespace (Swarmada ZoneMaintenance spec.scope.type).
	// +kubebuilder:validation:Enum=Zone;Namespace
	ScopeType string `json:"scopeType"`
	// ZoneName is required when ScopeType is Zone.
	// +optional
	ZoneName string `json:"zoneName,omitempty"`
	// Mode is Graceful or Immediate (Swarmada ZoneMaintenance spec.mode).
	// +kubebuilder:validation:Enum=Graceful;Immediate
	Mode string `json:"mode"`
	// Reason is copied to the ZoneMaintenance spec.reason.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`
	// ScheduledStart is copied to spec.scheduledStart; empty means on creation.
	// +optional
	ScheduledStart *metav1.Time `json:"scheduledStart,omitempty"`
}

// ZoneStopParams names the FleetZone to stop.
type ZoneStopParams struct {
	// +kubebuilder:validation:MinLength=1
	ZoneName string `json:"zoneName"`
	// Reason becomes the swarmada.io/estop-triggered annotation value.
	// +kubebuilder:validation:MinLength=1
	Reason string `json:"reason"`
}

// PolicyRecord is the policy engine's decision when the request was created.
type PolicyRecord struct {
	// Effect is RequireApproval for every stored request.
	Effect string `json:"effect"`
	// Code is a stable reason code.
	Code string `json:"code"`
	// +optional
	Message string `json:"message,omitempty"`
}

// ApprovalRequestSpec is written once by foc-mcp and never changed.
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="spec is immutable"
// +kubebuilder:validation:XValidation:rule="(self.type == 'DraftTask') == has(self.draftTask)",message="draftTask is set exactly when type is DraftTask"
// +kubebuilder:validation:XValidation:rule="(self.type == 'ActivateTask') == has(self.activateTask)",message="activateTask is set exactly when type is ActivateTask"
// +kubebuilder:validation:XValidation:rule="(self.type == 'ZoneMaintenance') == has(self.zoneMaintenance)",message="zoneMaintenance is set exactly when type is ZoneMaintenance"
// +kubebuilder:validation:XValidation:rule="(self.type == 'ZoneStop') == has(self.zoneStop)",message="zoneStop is set exactly when type is ZoneStop"
type ApprovalRequestSpec struct {
	Type RequestType `json:"type"`
	// RequestedFor is the operator whose chat session produced the request.
	// +kubebuilder:validation:MinLength=1
	RequestedFor string `json:"requestedFor"`
	// RequestedBy is the principal that created the object (the copilot).
	// +kubebuilder:validation:MinLength=1
	RequestedBy string `json:"requestedBy"`
	// SessionID links the request to the copilot session and audit trail.
	// +optional
	SessionID string `json:"sessionID,omitempty"`
	// ExpiresAt is when the request stops being approvable.
	ExpiresAt metav1.Time  `json:"expiresAt"`
	Policy    PolicyRecord `json:"policy"`
	// +optional
	DraftTask *DraftTaskParams `json:"draftTask,omitempty"`
	// +optional
	ActivateTask *ActivateTaskParams `json:"activateTask,omitempty"`
	// +optional
	ZoneMaintenance *ZoneMaintenanceParams `json:"zoneMaintenance,omitempty"`
	// +optional
	ZoneStop *ZoneStopParams `json:"zoneStop,omitempty"`
}

// RequestState is the lifecycle state of an ApprovalRequest.
// +kubebuilder:validation:Enum=Pending;Approved;Rejected;Expired;Executed;Failed
type RequestState string

const (
	StatePending  RequestState = "Pending"
	StateApproved RequestState = "Approved"
	StateRejected RequestState = "Rejected"
	StateExpired  RequestState = "Expired"
	StateExecuted RequestState = "Executed"
	StateFailed   RequestState = "Failed"
)

// IsTerminal reports whether no further transition is allowed.
func (s RequestState) IsTerminal() bool {
	switch s {
	case StateRejected, StateExpired, StateExecuted, StateFailed:
		return true
	}
	return false
}

// TargetRef identifies the Swarmada object an executed request wrote.
type TargetRef struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
}

// ApprovalRequestStatus is written only by foc-approvals.
type ApprovalRequestStatus struct {
	// +optional
	State RequestState `json:"state,omitempty"`
	// +optional
	DecidedBy string `json:"decidedBy,omitempty"`
	// +optional
	DecisionRef string `json:"decisionRef,omitempty"`
	// +optional
	DecidedAt *metav1.Time `json:"decidedAt,omitempty"`
	// +optional
	ExecutedAt *metav1.Time `json:"executedAt,omitempty"`
	// +optional
	Target *TargetRef `json:"target,omitempty"`
	// Instruction is the exact command the approver runs with their own
	// credentials, for a write Swarmada refuses to every ServiceAccount
	// (activation). The copilot then observes the result.
	// +optional
	Instruction string `json:"instruction,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// RefusedDecisions lists ApprovalDecisions that were not accepted.
	// +optional
	// +listType=set
	RefusedDecisions []string `json:"refusedDecisions,omitempty"`
}

// ApprovalRequest is a proposed Swarmada write that a named person must approve.
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=areq
// +kubebuilder:printcolumn:name="Type",type=string,JSONPath=`.spec.type`
// +kubebuilder:printcolumn:name="For",type=string,JSONPath=`.spec.requestedFor`
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="DecidedBy",type=string,JSONPath=`.status.decidedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ApprovalRequest struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ApprovalRequestSpec   `json:"spec"`
	Status ApprovalRequestStatus `json:"status,omitempty"`
}

// ApprovalRequestList contains a list of ApprovalRequest.
// +kubebuilder:object:root=true
type ApprovalRequestList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalRequest `json:"items"`
}

// Verdict is the approver's answer.
// +kubebuilder:validation:Enum=Approve;Reject
type Verdict string

const (
	VerdictApprove Verdict = "Approve"
	VerdictReject  Verdict = "Reject"
)

// ApprovalDecisionSpec is created by a person with their own credentials.
// An admission policy (config/admission) binds decidedBy to the authenticated
// username and refuses ServiceAccounts and updates.
type ApprovalDecisionSpec struct {
	// RequestRef is the ApprovalRequest name in the same namespace.
	// +kubebuilder:validation:MinLength=1
	RequestRef string  `json:"requestRef"`
	Verdict    Verdict `json:"verdict"`
	// DecidedBy must equal the creating user's authenticated username.
	// +kubebuilder:validation:MinLength=1
	DecidedBy string `json:"decidedBy"`
	// DecidedByGroups must equal the creating user's authenticated groups, so
	// the approver's authority can be checked with SubjectAccessReview.
	// +optional
	DecidedByGroups []string `json:"decidedByGroups,omitempty"`
	// +optional
	Comment string `json:"comment,omitempty"`
}

// ApprovalDecision records one person's approve or reject of one ApprovalRequest.
// +kubebuilder:object:root=true
// +kubebuilder:resource:scope=Namespaced,shortName=adec
// +kubebuilder:printcolumn:name="Request",type=string,JSONPath=`.spec.requestRef`
// +kubebuilder:printcolumn:name="Verdict",type=string,JSONPath=`.spec.verdict`
// +kubebuilder:printcolumn:name="DecidedBy",type=string,JSONPath=`.spec.decidedBy`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type ApprovalDecision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ApprovalDecisionSpec `json:"spec"`
}

// ApprovalDecisionList contains a list of ApprovalDecision.
// +kubebuilder:object:root=true
type ApprovalDecisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ApprovalDecision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ApprovalRequest{}, &ApprovalRequestList{}, &ApprovalDecision{}, &ApprovalDecisionList{})
}
