package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// EDIT THIS FILE!  THIS IS SCAFFOLDING FOR YOU TO OWN!
// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

type ComplianceCheckResult string

const (
	// The check ran to completion and passed
	CheckResultPass ComplianceCheckResult = "PASS"
	// The check ran to completion and failed
	CheckResultFail ComplianceCheckResult = "FAIL"
	// The check ran to completion and found something not severe enough to be considered error
	CheckResultInfo ComplianceCheckResult = "INFO"
	// The check ran, but could not complete properly
	CheckResultError ComplianceCheckResult = "ERROR"
	// The check didn't run because it is not applicable or not selected
	CheckResultSkipped ComplianceCheckResult = "SKIP"
	// The check didn't yield a usable result
	CheckResultNoResult ComplianceCheckResult = ""
)

// ComplianceCheckSpec defines the desired state of ComplianceCheck
type ComplianceCheckSpec struct {
	ID          string                `json:"id"`
	Result      ComplianceCheckResult `json:"result"`
	Description string                `json:"description,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ComplianceCheck is the Schema for the compliancechecks API
// +kubebuilder:resource:path=compliancechecks,scope=Namespaced
// +kubebuilder:printcolumn:name="Result",type="string",JSONPath=`.spec.result`
type ComplianceCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ComplianceCheckSpec `json:"spec,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// ComplianceCheckList contains a list of ComplianceCheck
type ComplianceCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ComplianceCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ComplianceCheck{}, &ComplianceCheckList{})
}
