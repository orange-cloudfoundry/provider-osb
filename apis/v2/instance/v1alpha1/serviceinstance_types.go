/*
Copyright 2025 The Crossplane Authors.

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
	"reflect"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/v2/application/v1alpha1"
	common "github.com/orange-cloudfoundry/provider-osb/apis/v2/common"
)

type ServiceInstanceSpec struct {
	xpv2.ManagedResourceSpec `json:",inline"`

	ForProvider common.InstanceData `json:"forProvider"`

	InitProvider common.InstanceData `json:"initProvider,omitempty"`
}

type ServiceInstanceStatus struct {
	xpv1.ResourceStatus `json:",inline"`
	AtProvider          ServiceInstanceObservation `json:"atProvider,omitempty"`
}

type ServiceInstanceObservation struct {
	ApplicationRef           *common.NamespacedName    `json:"application,omitempty"`
	ApplicationData          *v1alpha1.ApplicationSpec `json:"applicationData,omitempty"`
	common.InstanceData      `json:",inline"`
	Context                  common.KubernetesOSBContext `json:"context,omitempty"`
	DashboardURL             *string                     `json:"dashboardURL,omitempty"`
	LastOperationState       osb.LastOperationState      `json:"last_operation_state,omitempty"`
	LastOperationKey         osb.OperationKey            `json:"last_operation_key,omitempty"`
	LastOperationDescription string                      `json:"last_operation_description,omitempty"`
	HasActiveBindings        bool                        `json:"hasActiveBindings,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:storageversion

// ServiceInstance est la ressource managée OSB
// +kubebuilder:printcolumn:name="READY",type="string",JSONPath=".status.conditions[?(@.type=='Ready')].status"
// +kubebuilder:printcolumn:name="SYNCED",type="string",JSONPath=".status.conditions[?(@.type=='Synced')].status"
// +kubebuilder:printcolumn:name="EXTERNAL-NAME",type="string",JSONPath=".metadata.annotations.crossplane\\.io/external-name"
// +kubebuilder:printcolumn:name="AGE",type="date",JSONPath=".metadata.creationTimestamp"
// +kubebuilder:resource:scope=Namespaced,categories={crossplane,managed,osb}
type ServiceInstance struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ServiceInstanceSpec   `json:"spec"`
	Status ServiceInstanceStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ServiceInstanceList contient une liste de ServiceInstance
type ServiceInstanceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ServiceInstance `json:"items"`
}

// ServiceInstance type metadata.
var (
	ServiceInstanceKind             = reflect.TypeOf(ServiceInstance{}).Name()
	ServiceInstanceGroupKind        = schema.GroupKind{Group: Group, Kind: ServiceInstanceKind}.String()
	ServiceInstanceKindAPIVersion   = ServiceInstanceKind + "." + SchemeGroupVersion.String()
	ServiceInstanceGroupVersionKind = SchemeGroupVersion.WithKind(ServiceInstanceKind)
)

func init() {
	SchemeBuilder.Register(&ServiceInstance{}, &ServiceInstanceList{})
}

// GetProviderConfigReference of this ServiceInstance.
func (mg *ServiceInstance) GetProviderConfigReference() *xpv1.ProviderConfigReference {
	return mg.Spec.ForProvider.ApplicationData.ProviderConfigReference
}
