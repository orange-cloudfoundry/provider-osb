package v1alpha1

import (
	"errors"
	"fmt"
	"reflect"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common"
	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
)

var (
	errFailedToParseServiceInstanceParameters    = errors.New("error while marshalling or parsing parameters to bytes from ServiceBinding")
	errFailedToMarshallServiceInstanceParameters = errors.New("failed to marshal ServiceInstance parameters")
	errFailedToMarshallServiceInstanceContext    = errors.New("failed to marshal ServiceInstance context")
	errInstanceIdIsEmpty                         = errors.New("instanceID is empty")
	errPlanIdEmpty                               = errors.New("planID is empty")
	errServiceIdEmpty                            = errors.New("serviceID is empty")
	errOrganizationGuidEmpty                     = errors.New("organizationGuid is empty")
	errSpaceGuidEmpty                            = errors.New("spaceGuid is empty")
	errCtxMapEmpty                               = errors.New("ctxMap is empty")
	errOidPlatformIsEmpty                        = errors.New("oid platform is empty")
	errOidValueIsEmpty                           = errors.New("oid value is empty")
	errOperationKeyEmpty                         = errors.New("operatiopn key is empty")
)

// GetProviderConfigReference of this ServiceInstance.
func (si *ServiceInstance) GetProviderConfigReference() *xpv1.ProviderConfigReference {
	if si.Spec.ForProvider.ApplicationData == nil {
		return nil
	}

	return si.Spec.ForProvider.ApplicationData.ProviderConfigReference
}

// SetLastOperationState sets the LastOperationState in the ServiceInstance status.
// This is used to track the state of the last operation performed on the instance.
func (si *ServiceInstance) SetLastOperationState(state osbClient.LastOperationState) {
	si.Status.AtProvider.LastOperationState = state
}

// SetLastOperationDescription sets the LastOperationDescription in the ServiceInstance status.
// This is used to store a human-readable description of the last operation performed.
func (si *ServiceInstance) SetLastOperationDescription(desc string) {
	si.Status.AtProvider.LastOperationDescription = desc
}

// SetLastOperationKey sets the LastOperationKey of the ServiceInstance's status.
//
// Parameters:
// - operationKey: a pointer to the OSB OperationKey returned by the broker
func (si *ServiceInstance) SetLastOperationKey(operationKey osbClient.OperationKey) {
	si.Status.AtProvider.LastOperationKey = operationKey
}

// IsDeletable returns true if the ServiceInstance is in deleting state
// and has no active bindings.
func (si *ServiceInstance) IsDeletable() bool {
	return si.Status.AtProvider.LastOperationState == osbClient.StateDeleting &&
		!si.Status.AtProvider.HasActiveBindings
}

// IsStateInProgress checks if the ServiceInstance's last operation state
// is "InProgress", indicating that an asynchronous operation is currently ongoing.
//
// Returns:
// - bool: true if the last operation is in progress, false otherwise
func (si *ServiceInstance) IsStateInProgress() bool {
	return si.Status.AtProvider.LastOperationState == osbClient.StateInProgress
}

// IsStateDeleting checks if the ServiceInstance's last operation state
// indicates that the instance is being deleted.
//
// Returns:
// - bool: true if the last operation state is "deleting", false otherwise
func (si *ServiceInstance) IsStateDeleting() bool {
	return si.Status.AtProvider.LastOperationState == osbClient.StateDeleting
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
func (si *ServiceInstance) IsAlreadyDeleted() bool {
	// If the InstanceId is empty, the resource does not exist externally
	return si.Spec.ForProvider.InstanceId == ""
}

// HasActiveBindings checks if the ServiceInstance currently has active bindings.
//
// If true, the ServiceInstance cannot be deleted because bindings are still referencing it.
//
// Returns:
// - bool: true if there are active bindings, false otherwise
func (si *ServiceInstance) HasActiveBindings() bool {
	return si.Status.AtProvider.HasActiveBindings
}

// HasPlanID checks if the ServiceInstance has a PlanID set.
//
// Returns:
// - bool: true if PlanID is not empty, false otherwise
func (si *ServiceInstance) HasPlanID() bool {
	return si.Spec.ForProvider.PlanId != ""
}

// IsPlanIDDifferent checks if the ServiceInstance's PlanID is different
// from the PlanID of the given OSB instance.
//
// Parameters:
// - osbPlanID: the PlanID from the OSB instance to compare against
//
// Returns:
// - bool: true if PlanIDs are different, false if they match
func (si *ServiceInstance) IsPlanIDDifferent(osbPlanID string) bool {
	return si.Spec.ForProvider.PlanId != osbPlanID
}

// compareParametersWithOSB compares parameters between a ServiceInstance spec
// and its OSB instance response.
func (si *ServiceInstance) compareParametersWithOSB(osbInstance *osbClient.GetInstanceResponse) (bool, error) {
	if len(si.Spec.ForProvider.Parameters) == 0 {
		return true, nil
	}

	params, err := si.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return false, fmt.Errorf("%w: %s", errFailedToParseServiceInstanceParameters, fmt.Sprint(err))
	}

	if !reflect.DeepEqual(params, osbInstance.Parameters) {
		return false, nil
	}

	return true, nil
}

// CompareSpecWithOSB compares a ServiceInstance spec with the corresponding OSB instance response.
// It returns true if both representations are consistent, false otherwise.
func (si *ServiceInstance) CompareSpecWithOSB(osbInstance *osbClient.GetInstanceResponse) (bool, error) {
	if osbInstance == nil {
		return false, nil
	}

	// Compare PlanID
	if si.HasPlanID() && si.IsPlanIDDifferent(osbInstance.PlanID) {
		return false, nil
	}

	// Compare parameters
	if ok, err := si.compareParametersWithOSB(osbInstance); err != nil || !ok {
		return ok, err
	}

	// Compare context
	if !reflect.DeepEqual(si.Spec.ForProvider.Context, si.Status.AtProvider.Context) {
		return false, nil
	}

	return true, nil
}

// BuildProvisionRequest creates an OSB ProvisionRequest from a ServiceInstance spec.
func (si *ServiceInstance) BuildProvisionRequest(params map[string]any, ctxMap map[string]any) (*osbClient.ProvisionRequest, error) {
	if si.Spec.ForProvider.InstanceId == "" {
		return &osbClient.ProvisionRequest{}, errInstanceIdIsEmpty
	}

	if si.Spec.ForProvider.PlanId == "" {
		return &osbClient.ProvisionRequest{}, errPlanIdEmpty
	}

	if si.Spec.ForProvider.ServiceId == "" {
		return &osbClient.ProvisionRequest{}, errServiceIdEmpty
	}

	if si.Spec.ForProvider.OrganizationGuid == "" {
		return &osbClient.ProvisionRequest{}, errOrganizationGuidEmpty
	}

	if si.Spec.ForProvider.SpaceGuid == "" {
		return &osbClient.ProvisionRequest{}, errSpaceGuidEmpty
	}

	if len(ctxMap) == 0 {
		return &osbClient.ProvisionRequest{}, errCtxMapEmpty
	}

	provisionRequest := &osbClient.ProvisionRequest{
		InstanceID:        si.Spec.ForProvider.InstanceId,
		ServiceID:         si.Spec.ForProvider.ServiceId,
		PlanID:            si.Spec.ForProvider.PlanId,
		OrganizationGUID:  si.Spec.ForProvider.OrganizationGuid,
		SpaceGUID:         si.Spec.ForProvider.SpaceGuid,
		AcceptsIncomplete: true,
		Context:           ctxMap,
	}

	if params != nil {
		provisionRequest.Parameters = params
	}

	return provisionRequest, nil
}

// buildUpdateInstanceRequest constructs an OSB UpdateInstanceRequest from the given ServiceInstance.
// It converts the ServiceInstance spec parameters and context into the format expected by the OSB client.
// Returns the prepared request or an error if the conversion fails.
func (si *ServiceInstance) BuildUpdateInstanceRequest(oid osbClient.OriginatingIdentity) (*osbClient.UpdateInstanceRequest, error) {
	params, err := si.Spec.ForProvider.Parameters.ToParameters()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errFailedToMarshallServiceInstanceParameters, fmt.Sprint(err))
	}

	ctxMap, err := si.Spec.ForProvider.Context.ToMap()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errFailedToMarshallServiceInstanceContext, fmt.Sprint(err))
	}

	if si.Spec.ForProvider.InstanceId == "" {
		return &osbClient.UpdateInstanceRequest{}, errInstanceIdIsEmpty
	}

	if si.Spec.ForProvider.ServiceId == "" {
		return &osbClient.UpdateInstanceRequest{}, errServiceIdEmpty
	}

	if oid.Platform != "" {
		return &osbClient.UpdateInstanceRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value != "" {
		return &osbClient.UpdateInstanceRequest{}, errOidValueIsEmpty
	}

	updateInstanceRequest := &osbClient.UpdateInstanceRequest{
		InstanceID:          si.Spec.ForProvider.InstanceId,
		ServiceID:           si.Spec.ForProvider.ServiceId,
		AcceptsIncomplete:   true,
		OriginatingIdentity: &oid,
	}
	// TODO oid

	if si.Spec.ForProvider.PlanId != "" {
		updateInstanceRequest.PlanID = &si.Spec.ForProvider.PlanId
	}

	if len(ctxMap) > 0 {
		updateInstanceRequest.Context = ctxMap
	}

	if params != nil {
		updateInstanceRequest.Parameters = params
	}

	return updateInstanceRequest, nil
}

// updateStatus updates the ServiceInstance status based on the OSB ProvisionInstance response.
func (si *ServiceInstance) UpdateStatus(resp *osbClient.ProvisionResponse) {
	si.Status.AtProvider.Context = si.Spec.ForProvider.Context
	si.Status.AtProvider.DashboardURL = resp.DashboardURL

	if resp.Async {
		si.Status.SetConditions(xpv1.Creating())
		si.Status.AtProvider.LastOperationState = osbClient.StateInProgress
		if resp.OperationKey != nil {
			si.Status.AtProvider.LastOperationKey = *resp.OperationKey
		}
		return
	}

	si.Status.SetConditions(xpv1.Available())
	si.Status.AtProvider.LastOperationState = osbClient.StateSucceeded
}

// SetStatusContextFromProviderContext copies the context from the ServiceInstance's
// spec (ForProvider) into its status (AtProvider). This keeps the observed state
// in sync with the desired configuration.
func (si *ServiceInstance) SetStatusContextFromProviderContext() {
	si.Status.AtProvider.Context = si.Spec.ForProvider.Context
}

// SetDashboardURL updates the ServiceInstance status with the provided dashboard URL.
// This method is typically called after receiving a response from the service broker.
func (si *ServiceInstance) SetDashboardURL(url *string) {
	si.Status.AtProvider.DashboardURL = url
}

// updateInstanceStatusFromUpdate updates the status of the ServiceInstance
// based on the OSB UpdateInstanceResponse. It sets the dashboard URL, context,
// and last operation state. If the update is asynchronous, it also sets the
// last operation key and marks the operation as in progress.
func (si *ServiceInstance) UpdateStatusFromOSB(resp osbClient.OSBAsyncResponse) {
	si.SetStatusContextFromProviderContext()
	si.SetDashboardURL(resp.GetDashboardURL())

	if resp.IsAsync() {
		si.Status.SetConditions(xpv1.Creating())
		si.SetLastOperationState(osbClient.StateInProgress)
		if resp.GetOperationKey() != nil {
			si.SetLastOperationKey(*resp.GetOperationKey())
		}
		return
	}

	si.Status.SetConditions(xpv1.Available())
	si.SetLastOperationState(osbClient.StateSucceeded)
}

// IsInstanceIDEmpty returns true if the ServiceInstance has no InstanceId set
// in its spec. This can be used to check whether the instance has been
// provisioned or not.
func (si *ServiceInstance) IsInstanceIDEmpty() bool {
	return si.Spec.ForProvider.InstanceId == ""
}

// createRequestPollLastOperation builds and returns an OSB LastOperationRequest
// for polling the current status of an asynchronous operation on a service mg.
func (si *ServiceInstance) BuildPollLastOperationRequest(oid osbClient.OriginatingIdentity) (*osbClient.LastOperationRequest, error) {
	if si.Spec.ForProvider.InstanceId == "" {
		return &osbClient.LastOperationRequest{}, errInstanceIdIsEmpty
	}

	if oid.Platform != "" {
		return &osbClient.LastOperationRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value != "" {
		return &osbClient.LastOperationRequest{}, errOidValueIsEmpty
	}

	lastOperationRequest := &osbClient.LastOperationRequest{
		InstanceID:          si.Spec.ForProvider.InstanceId,
		OriginatingIdentity: &oid,
	}

	if si.Spec.ForProvider.ServiceId != "" {
		lastOperationRequest.ServiceID = &si.Spec.ForProvider.ServiceId
	}

	if si.Spec.ForProvider.PlanId != "" {
		lastOperationRequest.PlanID = &si.Spec.ForProvider.PlanId
	}

	if si.Status.AtProvider.LastOperationKey != "" {
		lastOperationRequest.OperationKey = &si.Status.AtProvider.LastOperationKey
	}

	return lastOperationRequest, nil
}

// updateInstanceStatusForAsyncDeletion updates the ServiceInstance status when
// a deletion is performed asynchronously.
func (si *ServiceInstance) UpdateStatusForAsyncDeletion(resp *osbClient.DeprovisionResponse) {
	si.SetLastOperationState(osbClient.StateDeleting)
	if resp.OperationKey != nil {
		si.SetLastOperationKey(*resp.OperationKey)
	}
}

// buildDeprovisionRequest constructs an OSB DeprovisionRequest from a ServiceInstance.
func (si *ServiceInstance) BuildDeprovisionRequest(oid osbClient.OriginatingIdentity) (*osbClient.DeprovisionRequest, error) {
	if si.Spec.ForProvider.InstanceId == "" {
		return &osbClient.DeprovisionRequest{}, errInstanceIdIsEmpty
	}

	if si.Spec.ForProvider.ServiceId == "" {
		return &osbClient.DeprovisionRequest{}, errServiceIdEmpty
	}

	if si.Spec.ForProvider.PlanId == "" {
		return &osbClient.DeprovisionRequest{}, errPlanIdEmpty
	}

	if oid.Platform != "" {
		return &osbClient.DeprovisionRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value != "" {
		return &osbClient.DeprovisionRequest{}, errOidValueIsEmpty
	}

	return &osbClient.DeprovisionRequest{
		InstanceID:          si.Spec.ForProvider.InstanceId,
		ServiceID:           si.Spec.ForProvider.ServiceId,
		PlanID:              si.Spec.ForProvider.PlanId,
		AcceptsIncomplete:   true,
		OriginatingIdentity: &oid,
	}, nil
}

// GetSpecForProvider returns a copy of the ForProvider spec.
// Useful for read-only operations or when you do not want to modify the original.
func (si *ServiceInstance) GetSpecForProvider() common.InstanceData {
	return si.Spec.ForProvider
}

// GetSpecForProviderPtr returns a pointer to the ForProvider spec.
// Useful for passing to functions that expect a pointer receiver
// or an interface implemented on *InstanceData.
func (si *ServiceInstance) GetSpecForProviderPtr() *common.InstanceData {
	return &si.Spec.ForProvider
}
