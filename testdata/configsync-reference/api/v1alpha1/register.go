package v1alpha1

import "k8s.io/apimachinery/pkg/runtime"

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ConfigSync{}, &ConfigSyncList{})
		return nil
	})
}
