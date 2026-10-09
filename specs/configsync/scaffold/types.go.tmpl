// API types given to agents in setups S2–S4 (copied over the Kubebuilder stub).
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SourceRef points at the ConfigMap to copy.
type SourceRef struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// ConfigSyncSpec defines the desired state of ConfigSync.
type ConfigSyncSpec struct {
	Source SourceRef `json:"source"`
	// NamespaceSelector selects the namespaces that receive a copy.
	NamespaceSelector metav1.LabelSelector `json:"namespaceSelector"`
}

// ConfigSyncStatus defines the observed state of ConfigSync.
type ConfigSyncStatus struct {
	// SyncedNamespaces is sorted.
	// +optional
	SyncedNamespaces []string `json:"syncedNamespaces,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ConfigSync is the Schema for the configsyncs API.
type ConfigSync struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ConfigSyncSpec   `json:"spec,omitempty"`
	Status ConfigSyncStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ConfigSyncList contains a list of ConfigSync.
type ConfigSyncList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ConfigSync `json:"items"`
}

// Registration lives in register.go, written by runner/scaffold.sh to match the
// SchemeBuilder style of your Kubebuilder version.
