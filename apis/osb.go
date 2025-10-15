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

// Package apis contains Kubernetes API for the Osb provider.
package apis

import (
	"k8s.io/apimachinery/pkg/runtime"

	bindingv1 "github.com/orange-cloudfoundry/provider-osb/apis/v1/binding/v1alpha1"
	instancev1 "github.com/orange-cloudfoundry/provider-osb/apis/v1/instance/v1alpha1"
	templatev1 "github.com/orange-cloudfoundry/provider-osb/apis/v1/v1alpha1"
	applicationv2 "github.com/orange-cloudfoundry/provider-osb/apis/v2/application/v1alpha1"
	bindingv2 "github.com/orange-cloudfoundry/provider-osb/apis/v2/binding/v1alpha1"
	instancev2 "github.com/orange-cloudfoundry/provider-osb/apis/v2/instance/v1alpha1"
	templatev2 "github.com/orange-cloudfoundry/provider-osb/apis/v2/v1alpha1"
)

func init() {
	// Register the types with the Scheme so the components can map objects to GroupVersionKinds and back
	AddToSchemes = append(AddToSchemes,
		instancev1.SchemeBuilder.AddToScheme,
		bindingv1.SchemeBuilder.AddToScheme,
		templatev1.SchemeBuilder.AddToScheme,
		applicationv2.SchemeBuilder.AddToScheme,
		instancev2.SchemeBuilder.AddToScheme,
		bindingv2.SchemeBuilder.AddToScheme,
		templatev2.SchemeBuilder.AddToScheme,
	)
}

// AddToSchemes may be used to add all resources defined in the project to a Scheme
var AddToSchemes runtime.SchemeBuilder

// AddToScheme adds all Resources to the Scheme
func AddToScheme(s *runtime.Scheme) error {
	return AddToSchemes.AddToScheme(s)
}
