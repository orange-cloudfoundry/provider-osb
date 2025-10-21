/*
Copyright 2020 The Crossplane Authors.

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

package controller

import (
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"

	//applicationv1 "github.com/orange-cloudfoundry/provider-osb/internal/v1/controller/application"
	//configv1 "github.com/orange-cloudfoundry/provider-osb/internal/v1/controller/config"
	//servicebindingv1 "github.com/orange-cloudfoundry/provider-osb/internal/v1/controller/servicebinding"
	//serviceinstancev1 "github.com/orange-cloudfoundry/provider-osb/internal/v1/controller/serviceinstance"

	"github.com/orange-cloudfoundry/provider-osb/internal/namespaced/controller/application"
	"github.com/orange-cloudfoundry/provider-osb/internal/namespaced/controller/config"
	"github.com/orange-cloudfoundry/provider-osb/internal/namespaced/controller/servicebinding"
	"github.com/orange-cloudfoundry/provider-osb/internal/namespaced/controller/serviceinstance"
)

// Setup creates all OSB controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	for _, setup := range []func(ctrl.Manager, controller.Options) error{
		//configv1.Setup,
		//applicationv1.Setup,
		//servicebindingv1.Setup,
		//serviceinstancev1.Setup,
		config.Setup,
		application.Setup,
		servicebinding.Setup,
		serviceinstance.Setup,
	} {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}
