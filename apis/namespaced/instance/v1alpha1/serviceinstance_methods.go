package v1alpha1

import (
	"fmt"
	"reflect"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	osb "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/namespaced/common"
)

// GetProviderConfigReference of this ServiceInstance.
func (mg *ServiceInstance) GetProviderConfigReference() *xpv1.ProviderConfigReference {
	if mg.Spec.ForProvider.ApplicationData == nil {
		return nil
	}

	return mg.Spec.ForProvider.ApplicationData.ProviderConfigReference
}

// SetLastOperationState sets the LastOperationState in the ServiceInstance status.
// This is used to track the state of the last operation performed on the instance.
func (mg *ServiceInstance) SetLastOperationState(state osb.LastOperationState) {
	mg.Status.AtProvider.LastOperationState = state
}

// SetLastOperationDescription sets the LastOperationDescription in the ServiceInstance status.
// This is used to store a human-readable description of the last operation performed.
func (mg *ServiceInstance) SetLastOperationDescription(desc string) {
	mg.Status.AtProvider.LastOperationDescription = desc
}

// SetLastOperationKey sets the LastOperationKey of the ServiceInstance's status.
//
// Parameters:
// - operationKey: a pointer to the OSB OperationKey returned by the broker
func (mg *ServiceInstance) SetLastOperationKey(operationKey *osb.OperationKey) {
	mg.Status.AtProvider.LastOperationKey = *operationKey
}

// IsDeletable returns true if the ServiceInstance is in deleting state
// and has no active bindings.
func (mg *ServiceInstance) IsDeletable() bool {
	return mg.Status.AtProvider.LastOperationState == osb.StateDeleting &&
		!mg.Status.AtProvider.HasActiveBindings
}

// IsStateInProgress checks if the ServiceInstance's last operation state
// is "InProgress", indicating that an asynchronous operation is currently ongoing.
//
// Returns:
// - bool: true if the last operation is in progress, false otherwise
func (mg *ServiceInstance) IsStateInProgress() bool {
	return mg.Status.AtProvider.LastOperationState == osb.StateInProgress
}

// IsStateDeleting checks if the ServiceInstance's last operation state
// indicates that the instance is being deleted.
//
// Returns:
// - bool: true if the last operation state is "deleting", false otherwise
func (mg *ServiceInstance) IsStateDeleting() bool {
	return mg.Status.AtProvider.LastOperationState == osb.StateDeleting
}

// IsAlreadyDeleted checks if the given ServiceInstance has no InstanceId set,
// indicating that the resource does not exist in the external system and can be
// considered already deleted.
//
// Parameters:
// - instance: the ServiceInstance to check
//
// Returns:
// - bool: true if the instance is considered already deleted, false otherwise
func (mg *ServiceInstance) IsAlreadyDeleted() bool {
	// If the InstanceId is empty, the resource does not exist externally
	return mg.Spec.ForProvider.InstanceId == ""
}

// HasActiveBindings checks if the ServiceInstance currently has active bindings.
//
// If true, the ServiceInstance cannot be deleted because bindings are still referencing it.
//
// Returns:
// - bool: true if there are active bindings, false otherwise
func (mg *ServiceInstance) HasActiveBindings() bool {
	return mg.Status.AtProvider.HasActiveBindings
}

// HasPlanID checks if the ServiceInstance has a PlanID set.
//
// Returns:
// - bool: true if PlanID is not empty, false otherwise
func (mg *ServiceInstance) HasPlanID() bool {
	return mg.Spec.ForProvider.PlanId != ""
}

// IsPlanIDDifferent checks if the ServiceInstance's PlanID is different
// from the PlanID of the given OSB instance.
//
// Parameters:
// - osbPlanID: the PlanID from the OSB instance to compare against
//
// Returns:
// - bool: true if PlanIDs are different, false if they match
func (mg *ServiceInstance) IsPlanIDDifferent(osbPlanID string) bool {
	return mg.Spec.ForProvider.PlanId != osbPlanID
}

// compareParametersWithOSB compares parameters between a ServiceInstance spec
// and its OSB instance response.
func (mg *ServiceInstance) compareParametersWithOSB(osbInstance *osb.GetInstanceResponse) (bool, error) {
	if len(mg.Spec.ForProvider.Parameters) == 0 {
		return true, nil
	}

	params, err := mg.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return false, fmt.Errorf("failed to parse ServiceInstance parameters: %w", err)
	}

	if !reflect.DeepEqual(params, osbInstance.Parameters) {
		return false, nil
	}

	return true, nil
}

// CompareSpecWithOSB compares a ServiceInstance spec with the corresponding OSB instance response.
// It returns true if both representations are consistent, false otherwise.
func (mg *ServiceInstance) CompareSpecWithOSB(osbInstance *osb.GetInstanceResponse) (bool, error) {
	if osbInstance == nil {
		return false, nil
	}

	// Compare PlanID
	if mg.HasPlanID() && mg.IsPlanIDDifferent(osbInstance.PlanID) {
		return false, nil
	}

	// Compare parameters
	if ok, err := mg.compareParametersWithOSB(osbInstance); err != nil || !ok {
		return ok, err
	}

	// Compare context
	if !reflect.DeepEqual(mg.Spec.ForProvider.Context, mg.Status.AtProvider.Context) {
		return false, nil
	}

	return true, nil
}

// buildOSBProvisionRequest creates an OSB ProvisionRequest from a ServiceInstance spec.
func (mg *ServiceInstance) BuildOSBProvisionRequest(params map[string]interface{}, ctxMap map[string]interface{}) *osb.ProvisionRequest {
	return &osb.ProvisionRequest{
		InstanceID:        mg.Spec.ForProvider.InstanceId,
		ServiceID:         mg.Spec.ForProvider.ServiceId,
		PlanID:            mg.Spec.ForProvider.PlanId,
		OrganizationGUID:  mg.Spec.ForProvider.OrganizationGuid,
		SpaceGUID:         mg.Spec.ForProvider.SpaceGuid,
		Parameters:        params,
		AcceptsIncomplete: true,
		Context:           ctxMap,
	}
}

// buildUpdateRequest constructs an OSB UpdateInstanceRequest from the given ServiceInstance.
// It converts the ServiceInstance spec parameters and context into the format expected by the OSB client.
// Returns the prepared request or an error if the conversion fails.
func (mg *ServiceInstance) BuildOSBUpdateRequest() (*osb.UpdateInstanceRequest, error) {
	params, err := mg.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ServiceInstance parameters: %w", err)
	}

	ctxMap, err := mg.Spec.ForProvider.Context.ToMap()
	if err != nil {
		return nil, fmt.Errorf("failed to marshal ServiceInstance context: %w", err)
	}

	return &osb.UpdateInstanceRequest{
		InstanceID:        mg.Spec.ForProvider.InstanceId,
		ServiceID:         mg.Spec.ForProvider.ServiceId,
		PlanID:            &mg.Spec.ForProvider.PlanId,
		Parameters:        params,
		AcceptsIncomplete: true,
		Context:           ctxMap,
	}, nil
}

// updateStatus updates the ServiceInstance status based on the OSB ProvisionInstance response.
func (mg *ServiceInstance) UpdateStatus(resp *osb.ProvisionResponse) {
	mg.Status.AtProvider.Context = mg.Spec.ForProvider.Context
	mg.Status.AtProvider.DashboardURL = resp.DashboardURL

	if resp.Async {
		mg.Status.SetConditions(xpv1.Creating())
		mg.Status.AtProvider.LastOperationState = osb.StateInProgress
		if resp.OperationKey != nil {
			mg.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		return
	}

	mg.Status.SetConditions(xpv1.Available())
	mg.Status.AtProvider.LastOperationState = osb.StateSucceeded
}

// SetStatusContextFromProviderContext copies the context from the ServiceInstance's
// spec (ForProvider) into its status (AtProvider). This keeps the observed state
// in sync with the desired configuration.
func (mg *ServiceInstance) SetStatusContextFromProviderContext() {
	mg.Status.AtProvider.Context = mg.Spec.ForProvider.Context
}

// SetDashboardURL updates the ServiceInstance status with the provided dashboard URL.
// This method is typically called after receiving a response from the service broker.
func (mg *ServiceInstance) SetDashboardURL(url *string) {
	mg.Status.AtProvider.DashboardURL = url
}

// updateInstanceStatusFromUpdate updates the status of the ServiceInstance
// based on the OSB UpdateInstanceResponse. It sets the dashboard URL, context,
// and last operation state. If the update is asynchronous, it also sets the
// last operation key and marks the operation as in progress.
func (mg *ServiceInstance) UpdateStatusFromOSB(resp osb.OSBAsyncResponse) {
	mg.SetStatusContextFromProviderContext()
	mg.SetDashboardURL(resp.GetDashboardURL())

	if resp.IsAsync() {
		mg.Status.SetConditions(xpv1.Creating())
		mg.SetLastOperationState(osb.StateInProgress)
		if resp.GetOperationKey() != nil {
			mg.SetLastOperationKey(resp.GetOperationKey())
		}
		return
	}

	mg.Status.SetConditions(xpv1.Available())
	mg.SetLastOperationState(osb.StateSucceeded)
}

// IsInstanceIDEmpty returns true if the ServiceInstance has no InstanceId set
// in its spec. This can be used to check whether the instance has been
// provisioned or not.
func (mg *ServiceInstance) IsInstanceIDEmpty() bool {
	return mg.Spec.ForProvider.InstanceId == ""
}

// createRequestPollLastOperation builds and returns an OSB LastOperationRequest
// for polling the current status of an asynchronous operation on a service mg.
func (mg *ServiceInstance) CreateRequestPollLastOperation(originatingIdentity osb.OriginatingIdentity) *osb.LastOperationRequest {

	return &osb.LastOperationRequest{
		// mgID identifies the service mg whose operation is being polled.
		InstanceID: mg.Spec.ForProvider.InstanceId,

		// ServiceID identifies the service offering (from the broker catalog).
		ServiceID: &mg.Spec.ForProvider.ServiceId,

		// PlanID identifies the plan of the service offering used by this mg.
		PlanID: &mg.Spec.ForProvider.PlanId,

		// OriginatingIdentity contains information about the user or system
		// making the OSB request (useful for auditing and multi-tenancy).
		OriginatingIdentity: &originatingIdentity,

		// OperationKey uniquely identifies the asynchronous operation being checked.
		// It is provided by the broker when the operation was initiated asynchronously.
		OperationKey: &mg.Status.AtProvider.LastOperationKey,
	}
}

// updateInstanceStatusForAsyncDeletion updates the ServiceInstance status when
// a deletion is performed asynchronously.
func (mg *ServiceInstance) UpdateStatusForAsyncDeletion(resp *osb.DeprovisionResponse) {
	mg.SetLastOperationState(osb.StateDeleting)
	if resp.OperationKey != nil {
		mg.SetLastOperationKey(resp.OperationKey)
	}
}

// buildDeprovisionRequest constructs an OSB DeprovisionRequest from a ServiceInstance.
func (mg *ServiceInstance) BuildDeprovisionRequest(originatingIdentity osb.OriginatingIdentity) *osb.DeprovisionRequest {
	return &osb.DeprovisionRequest{
		InstanceID:          mg.Spec.ForProvider.InstanceId,
		ServiceID:           mg.Spec.ForProvider.ServiceId,
		PlanID:              mg.Spec.ForProvider.PlanId,
		AcceptsIncomplete:   true,
		OriginatingIdentity: &originatingIdentity,
	}
}

// GetSpecForProvider returns a copy of the ForProvider spec.
// Useful for read-only operations or when you do not want to modify the original.
func (mg *ServiceInstance) GetSpecForProvider() common.InstanceData {
	return mg.Spec.ForProvider
}

// GetSpecForProviderPtr returns a pointer to the ForProvider spec.
// Useful for passing to functions that expect a pointer receiver
// or an interface implemented on *InstanceData.
func (mg *ServiceInstance) GetSpecForProviderPtr() *common.InstanceData {
	return &mg.Spec.ForProvider
}
