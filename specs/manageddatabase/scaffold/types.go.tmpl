// API types given to agents in setups S2–S4 (copied over the Kubebuilder stub).
package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ManagedDatabaseSpec defines the desired state of ManagedDatabase.
type ManagedDatabaseSpec struct {
	// Engine is the database engine. Immutable after creation.
	// +kubebuilder:validation:Enum=postgres;mysql
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="engine is immutable"
	Engine string `json:"engine"`

	// SizeGB is the allocated storage.
	// +kubebuilder:validation:Minimum=10
	// +kubebuilder:validation:Maximum=1000
	SizeGB int32 `json:"sizeGB"`

	// DeletionPolicy says what happens to the cloud database when this object is deleted.
	// +kubebuilder:validation:Enum=Delete;Retain
	// +kubebuilder:default=Delete
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
}

// ManagedDatabaseStatus defines the observed state of ManagedDatabase.
type ManagedDatabaseStatus struct {
	// +optional
	DatabaseID string `json:"databaseID,omitempty"`
	// +optional
	Endpoint string `json:"endpoint,omitempty"`
	// Phase is one of Creating, Available, Deleting, Failed.
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ManagedDatabase is the Schema for the manageddatabases API.
type ManagedDatabase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ManagedDatabaseSpec   `json:"spec,omitempty"`
	Status ManagedDatabaseStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ManagedDatabaseList contains a list of ManagedDatabase.
type ManagedDatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ManagedDatabase `json:"items"`
}

// Registration lives in register.go, written by runner/scaffold.sh to match the
// SchemeBuilder style of your Kubebuilder version.
