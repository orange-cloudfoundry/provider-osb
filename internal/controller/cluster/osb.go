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

package cluster

import (
	"errors"
	"fmt"

	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/orange-cloudfoundry/provider-osb/internal/controller/cluster/application"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/cluster/config"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/cluster/servicebinding"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/cluster/serviceinstance"
)

var errFailedToSetupClusterController = errors.New("failed to set up cluster controller ")

// Setup creates all OSB controllers with the supplied logger and adds them to
// the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	controllers := map[string]func(ctrl.Manager, controller.Options) error{
		"config":          config.Setup,
		"application":     application.Setup,
		"servicebinding":  servicebinding.Setup,
		"serviceinstance": serviceinstance.Setup,
	}

	for name, setup := range controllers {
		if err := setup(mgr, o); err != nil {
			return fmt.Errorf("%w '%s': %s", errFailedToSetupClusterController, name, fmt.Sprint(err))
		}
	}

	return nil
}
