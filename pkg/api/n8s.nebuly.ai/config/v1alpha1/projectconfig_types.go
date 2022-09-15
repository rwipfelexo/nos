package v1alpha1

import (
	"github.com/nebuly-ai/nebulnetes/pkg/constant"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	cfg "sigs.k8s.io/controller-runtime/pkg/config/v1alpha1"
)

//+kubebuilder:object:root=true

type CustomControllerManagerConfig struct {
	metav1.TypeMeta                        `json:",inline"`
	cfg.ControllerManagerConfigurationSpec `json:",inline"`
	NvidiaGPUResourceMemoryGB              *int64 `json:"nvidiaGPUResourceMemoryGB,omitempty"`
}

func (c *CustomControllerManagerConfig) FillDefaultValues() {
	if c.NvidiaGPUResourceMemoryGB == nil {
		var defaultValue int64 = constant.DefaultNvidiaGPUResourceMemory
		c.NvidiaGPUResourceMemoryGB = &defaultValue
	}
}

func init() {
	SchemeBuilder.Register(&CustomControllerManagerConfig{})
}