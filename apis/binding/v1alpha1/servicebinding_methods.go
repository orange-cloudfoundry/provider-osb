package v1alpha1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	kerrors "k8s.io/apimachinery/pkg/api/errors"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	osbClient "github.com/orange-cloudfoundry/go-open-service-broker-client/v2"
	"github.com/orange-cloudfoundry/provider-osb/apis/common"
	instance "github.com/orange-cloudfoundry/provider-osb/apis/instance/v1alpha1"
	"github.com/orange-cloudfoundry/provider-osb/internal/controller/util"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/uuid"
)

const (
	bindingMetadataPrefix = "binding." + util.MetadataPrefix

	referenceFinalizerName = bindingMetadataPrefix + "/service-binding"
	asyncDeletionFinalizer = bindingMetadataPrefix + "/async-deletion"
)

var (
	errMarshalParameters                          = errors.New("failed to marshal or parse binding parameters from osb response")
	errMarshalEndpoints                           = errors.New("failed to marshal or parse binding endpoints from osb response")
	errMarshalVolumeMounts                        = errors.New("failed to marshal or parse binding volume mounts binding from osb response")
	errFailedToParseFromBindingStatus             = errors.New("failed to parse parameters from binding status")
	errFailedToParseStatusParameters              = errors.New("failed to parse Status.AtProvider.Parameters JSON")
	errFailedToParseSpecParamaters                = errors.New("failed to parse Spec.ForProvider.Parameters JSON")
	errFailedToMarshalCredential                  = errors.New("failed to marshal credential")
	errFailedRequestOSBRotateBinding              = errors.New("failed to marshal credential")
	errFailedMarshallingContextFromServiceBinding = errors.New("failed to marshalling sb.Spec.ForProvider.Context")
	errFailedUnMarshallingRequestContext          = errors.New("failed to unmarshalling request context from ServiceBinding")
	errFailedUnMarshallingRequestParams           = errors.New("failed to unmarshalling request parameters from ServiceBinding")
	errStatusParametersIsEmpty                    = errors.New("failed to unmarshalling status parameters is empty")
	errInstanceIdIsEmpty                          = errors.New("instanceID is empty")
	errPlanIdEmpty                                = errors.New("planID is empty")
	errOidPlatformIsEmpty                         = errors.New("oid platform is empty")
	errOidValueIsEmpty                            = errors.New("oid value is empty")
	errServiceIdEmpty                             = errors.New("serviceID is empty")
	errBindingIdEmpty                             = errors.New("bindingID is empty")
	errNewBindingUUIDEmpty                        = errors.New("new binding uuid is empty")
	errFailedToBuildRotateBindingRequest          = errors.New("failed to build rotate binding request")
	errCannotGetBindingData                       = errors.New("cannot get data from ServiceBinding")
	errInstanceNotFound                           = errors.New("referenced service instance not found")
	errInstanceFetchFailed                        = errors.New("failed to retrieve referenced service instance")
	errMissingInstanceData                        = errors.New("missing instance data: no reference or inlined data provided")
	errInstanceRefEmpty                           = errors.New("instance ref is empty")
	errServiceBindingEmpty                        = errors.New("service binding is empty")
	errRetrieveBindingDataFailed                  = errors.New("failed to retrieve binding data")
	errFailedToBuildBindRequest                   = errors.New("failed to build bind request")
	errConvertBindingSpecFailed                   = errors.New("failed to convert binding spec data")
	errOSBBindRequestFailed                       = errors.New("OSB Bind request failed")
	errOSBBindongIdIsNil                          = errors.New("OSB Bind request returned nil response for bindingID")
	errOSBUnbindRequestFailed                     = errors.New("OSB Unbind request failed")
	errOSBUnbindNilResponse                       = errors.New("OSB Unbind returned nil response for request")
	errAddAsyncDeletionFinalizer                  = errors.New("failed to add async deletion finalizer")
	errFailedToBuildUnbindRequest                 = errors.New("failed to build unbind request")
	errFailedToHandleBindRequest                  = errors.New("failed to handle bind request")
	errTechnicalEncountered                       = errors.New("technical error encountered")
	errCannotRemoveFinalizer                      = errors.New("cannot remove finalizer from referenced resource")
	errFailedToBuildBindingLastOperationRequest   = errors.New("failed to build binding last operation request")
	errOSBPollBindingLastOperationRequestFailed   = errors.New("OSB PollBindingLastOperation request failed")
	errFailedToBuildGetBindingRequest             = errors.New("failed to build get binding request")
	errNilBindingResponse                         = errors.New("GetBindingResponse is nil for binding")
	errFailedToGetLastestKubeObject               = errors.New("failed to get lastest kube object")
	errUpdateBindingStatus                        = errors.New("failed to update binding status")
	errMarshalCredentials                         = errors.New("failed to marshal credentials from response")
	errFailedToHandleRenewalBindings              = errors.New("failed to handle renewal bindings")
	errStatusSpecCompareFailed                    = errors.New("failed to compare status and spec provider")
	errStatusSpecMismatch                         = errors.New("status and spec provider parameters differ")
	errCannotAddFinalizer                         = errors.New("cannot add finalizer to referenced resource")
	errCannotHandleUnknowAction                   = errors.New("cannot handle unknown action")
)

// SetConditions sets the Conditions field in the ServiceBinding status.
// This allows temporarily replacing the method generated by angryjet.
func (sb *ServiceBinding) SetConditions(c ...xpv1.Condition) {
	sb.Status.Conditions = c
}

// SetLastOperationState sets the LastOperationState in the ServiceBinding status.
// This is used to track the state of the last operation performed on the binding.
func (sb *ServiceBinding) SetLastOperationState(state osbClient.LastOperationState) {
	sb.Status.AtProvider.LastOperationState = state
}

// SetLastOperationDescription sets the LastOperationDescription in the ServiceBinding status.
// This is used to store a human-readable description of the last operation performed.
func (sb *ServiceBinding) SetLastOperationDescription(desc string) {
	sb.Status.AtProvider.LastOperationDescription = desc
}

// SetLastOperationKey sets the LastOperationKey of the ServiceBinding's status.
//
// Parameters:
// - operationKey: a pointer to the OSB OperationKey returned by the broker
func (sb *ServiceBinding) SetLastOperationKey(operationKey *osbClient.OperationKey) {
	sb.Status.AtProvider.LastOperationKey = *operationKey
}

// IsStateInProgress checks if the ServiceBinding's last operation state
// is "InProgress", indicating that an asynchronous operation is currently ongoing.
//
// Returns:
// - bool: true if the last operation is in progress, false otherwise
func (sb *ServiceBinding) IsStateInProgress() bool {
	return sb.Status.AtProvider.LastOperationState == osbClient.StateInProgress
}

// IsExternalNameEmpty returns true if the ServiceBinding has no external name set.
// This can be used to check whether the binding has been fully created or not.
func (sb *ServiceBinding) IsExternalNameEmpty() bool {
	return meta.GetExternalName(sb) == ""
}

// GetExternalName returns the external name of the ServiceBinding.
// The external name is typically set by the user or the system to uniquely
// identify the binding in the external service broker.
func (sb *ServiceBinding) GetExternalName() string {
	return meta.GetExternalName(sb)
}

// SetExternalName sets the external name of the ServiceBinding resource.
// This typically assigns the external identifier (UUID) from the provider
// to the managed resource metadata in Kubernetes.
func (sb *ServiceBinding) SetExternalName(uuid string) {
	meta.SetExternalName(sb, uuid)
}

// CreateResponseData constructs a responseData object from an OSB GetBindingResponse.
// It extracts parameters, endpoints, volume mounts, and other metadata
// from the broker response and converts them into the internal responseData format.
func (sb *ServiceBinding) CreateResponseData(resp osbClient.GetBindingResponse) responseData {
	return responseData{
		Parameters:      resp.Parameters,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	}
}

// CreateResponseDataWithBindingParameters builds a responseData object from an OSB BindResponse,
// combining it with the existing parameters stored in the ServiceBinding status.
// It converts the serialized parameters back into a structured format.
// Returns an error if the parameters in the current status cannot be parsed.
func (sb *ServiceBinding) CreateResponseDataWithBindingParameters(resp osbClient.BindResponse) (responseData, error) {
	params, err := sb.Status.AtProvider.Parameters.ToParameters()
	if err != nil {
		return responseData{}, fmt.Errorf("%w: %s", errFailedToParseFromBindingStatus, fmt.Sprint(err))
	}

	return responseData{
		Parameters:      params,
		Endpoints:       resp.Endpoints,
		VolumeMounts:    resp.VolumeMounts,
		RouteServiceURL: resp.RouteServiceURL,
		SyslogDrainURL:  resp.SyslogDrainURL,
		Metadata:        resp.Metadata,
	}, nil
}

// setAtProvider updates the AtProvider field in the ServiceBinding status
// with the given observation.
// This method is typically used to synchronize the observed state from the
// external provider with the managed resource in Kubernetes.
func (sb *ServiceBinding) setAtProvider(observation ServiceBindingObservation) error {
	sb.Status.AtProvider = observation
	// Additional validation or checks can be added here.
	return nil
}

// SetResponseDataInStatus updates the ServiceBinding status with data received from a response.
// It serializes the Parameters, Endpoints, and VolumeMounts fields into JSON before storing them
// in the AtProvider status. This method ensures the observed state in Kubernetes reflects
// the current state from the external service.
//
// Note: Certain operational fields (e.g., LastOperationState, LastOperationKey, etc.)
// are intentionally preserved to avoid overwriting ongoing operation data.
func (sb *ServiceBinding) SetResponseDataInStatus(response responseData) error {
	params, err := json.Marshal(response.Parameters)
	if err != nil {
		return fmt.Errorf("%w: %s", errMarshalParameters, fmt.Sprint(err))
	}

	endpoints, err := json.Marshal(response.Endpoints)
	if err != nil {
		return fmt.Errorf("%w: %s", errMarshalEndpoints, fmt.Sprint(err))
	}

	volumeMounts, err := json.Marshal(response.VolumeMounts)
	if err != nil {
		return fmt.Errorf("%w: %s", errMarshalVolumeMounts, fmt.Sprint(err))
	}

	return sb.setAtProvider(ServiceBindingObservation{
		// Update attributes from response data
		Parameters:      common.SerializableParameters(params),
		RouteServiceURL: response.RouteServiceURL,
		Endpoints:       SerializableEndpoints(endpoints),
		VolumeMounts:    SerializableVolumeMounts(volumeMounts),
		SyslogDrainURL:  response.SyslogDrainURL,
		Metadata:        response.Metadata,
		// Do not change these attributes
		LastOperationState:       sb.Status.AtProvider.LastOperationState,
		LastOperationKey:         sb.Status.AtProvider.LastOperationKey,
		LastOperationDescription: sb.Status.AtProvider.LastOperationDescription,
		LastOperationPolledTime:  sb.Status.AtProvider.LastOperationPolledTime,
	})
}

// isStatusParametersEmpty checks whether the ServiceBinding's status parameters
// in AtProvider are empty. Returns true if Parameters is an empty string, false otherwise.
func (sb *ServiceBinding) isStatusParametersEmpty() bool {
	return sb.Status.AtProvider.Parameters == ""
}

// We do not compare credentials, as this logic is managed by creds rotation.
// We do not compare bindingmetadata either, since the only metadata in binding objects
// is related to binding rotation (renew_before and expires_at)
// TODO add context and route to test if these were updated and return false
func (sb *ServiceBinding) IsStatusParametersNotLikeSpecParameters() (bool, error) {
	var statusMap, specMap map[string]interface{}

	if sb.isStatusParametersEmpty() {
		return true, errStatusParametersIsEmpty
	}

	if err := json.Unmarshal([]byte(sb.Status.AtProvider.Parameters), &statusMap); err != nil {
		return true, fmt.Errorf("%w: %s", errFailedToParseStatusParameters, fmt.Sprint(err))
	}

	if err := json.Unmarshal([]byte(sb.Spec.ForProvider.Parameters), &specMap); err != nil {
		return true, fmt.Errorf("%w: %s", errFailedToParseSpecParamaters, fmt.Sprint(err))
	}

	return !reflect.DeepEqual(statusMap, specMap), nil
}

// CreateGetBindingRequest constructs an OSB GetBindingRequest for the ServiceBinding.
// It uses the external name of the binding as the BindingID and the associated
// ServiceInstance's InstanceID.
func (sb *ServiceBinding) buildGetBindingRequest(bindingData BindingData) (*osbClient.GetBindingRequest, error) {
	if bindingData.InstanceData.InstanceId == "" {
		return &osbClient.GetBindingRequest{}, errInstanceIdIsEmpty
	}

	bindingId := sb.GetExternalName()
	if bindingId == "" {
		return &osbClient.GetBindingRequest{}, errBindingIdEmpty
	}

	return &osbClient.GetBindingRequest{
		InstanceID: bindingData.InstanceData.InstanceId,
		BindingID:  bindingId,
	}, nil
}

// buildRotateBindingRequest constructs an OSB RotateBindingRequest for a binding.
func (sb *ServiceBinding) buildRotateBindingRequest(bindingData BindingData, oid osbClient.OriginatingIdentity, newUUID string) (*osbClient.RotateBindingRequest, error) {
	if oid.Platform == "" {
		return &osbClient.RotateBindingRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value == "" {
		return &osbClient.RotateBindingRequest{}, errOidValueIsEmpty
	}

	if bindingData.InstanceData.InstanceId == "" {
		return &osbClient.RotateBindingRequest{}, errInstanceIdIsEmpty
	}

	predecessorBindingID := sb.GetExternalName()
	if predecessorBindingID == "" {
		return &osbClient.RotateBindingRequest{}, errBindingIdEmpty
	}

	if newUUID == "" {
		return &osbClient.RotateBindingRequest{}, errNewBindingUUIDEmpty
	}

	return &osbClient.RotateBindingRequest{
		InstanceID:           bindingData.InstanceData.InstanceId,
		BindingID:            newUUID,
		AcceptsIncomplete:    true, // TODO: make configurable
		PredecessorBindingID: predecessorBindingID,
		OriginatingIdentity:  &oid,
	}, nil
}

// removeRefFinalizer adds removes the reference finalizer to the ServiceInstance object referenced by the binding
// (if there is one)
func (sb *ServiceBinding) removeRefFinalizer(ctx context.Context, kube client.Client) error {
	// Remove finalizer from referenced ServiceInstance
	instanceRef := sb.Spec.ForProvider.InstanceRef
	if instanceRef != nil {
		finalizerName := fmt.Sprintf("%s-%s", referenceFinalizerName, sb.Name)
		return util.HandleFinalizer(ctx, kube, sb, finalizerName, util.RemoveFinalizerIfExists)
	}
	return nil
}

// handlePollError processes errors returned during polling of a ServiceBinding's last operation.
//
// Parameters:
//   - ctx: the context for controlling cancellation and timeouts.
//   - kube: the Kubernetes client used to update resources.
//   - err: the error returned by polling the last operation.
//
// Returns three values:
//  1. A boolean indicating whether the error was handled.
//  2. An Action indicating the next step (common.NothingToDo or common.NeedToCreate).
//  3. An error if any step of finalizer removal fails.
//
// Behavior:
//   - Checks if the error indicates the resource is gone and if the ServiceBinding was deleted.
//   - If the resource is gone:
//     1. Removes the async deletion finalizer from the ServiceBinding.
//     2. Removes the reference finalizer from the associated ServiceInstance.
//     3. Returns (true, NeedToCreate, nil) to indicate the resource no longer exists and the error was handled.
//   - If the error does not indicate a gone resource, returns (false, NeedToCreate, nil) to indicate it was not handled.
//   - Returns an error if any finalizer removal fails.
func (sb *ServiceBinding) handlePollError(ctx context.Context, kube client.Client, err error) (bool, common.Action, error) {
	if util.IsResourceGone(err) && meta.WasDeleted(sb) {
		// Remove async finalizer from the binding
		if err := util.HandleFinalizer(ctx, kube, sb, asyncDeletionFinalizer, util.RemoveFinalizerIfExists); err != nil {
			return true, common.NothingToDo, fmt.Errorf("%w: %s", errTechnicalEncountered, fmt.Sprint(err))
		}

		// Remove reference finalizer from the related ServiceInstance
		if err := sb.removeRefFinalizer(ctx, kube); err != nil {
			return true, common.NothingToDo, fmt.Errorf("%w: %s", errCannotRemoveFinalizer, fmt.Sprint(err))
		}

		return true, common.NeedToCreate, nil
	}

	// Resource no longer exists — nothing more to do
	return false, common.NeedToCreate, nil
}

// handleLastOperationInProgress handles polling of a ServiceBinding's last operation
// when the binding is in an "in-progress" state.
//
// Parameters:
//   - ctx: the context for controlling cancellation and timeouts.
//   - kube: the Kubernetes client used to fetch and update the ServiceBinding.
//   - osb: the OSB client used to poll the last operation status.
//   - bindingData: the data required to identify and operate on the binding.
//   - oid: the originating identity to include in the last operation request.
//
// Returns two values:
//  1. An Action indicating the next step (common.NothingToDo or other relevant action from handlePollError).
//  2. An error if any step fails during polling or status update.
//
// Behavior:
//  1. Builds a last operation request using buildBindingLastOperationRequest.
//  2. Polls the broker using PollBindingLastOperation.
//     - If polling returns an error, attempts to handle it via handlePollError.
//     - Returns the action and error from handlePollError if the error is handled.
//     - Otherwise returns NothingToDo with an error.
//  3. Fetches the latest version of the ServiceBinding from Kubernetes.
//  4. Updates the status of the ServiceBinding using util.UpdateStatusFromLastOp.
func (sb *ServiceBinding) handleLastOperationInProgress(
	ctx context.Context,
	kube client.Client,
	osb osbClient.Client,
	bindingData BindingData,
	oid osbClient.OriginatingIdentity) (common.Action, error) {
	req, err := sb.buildBindingLastOperationRequest(bindingData, oid)
	if err != nil {
		return common.NothingToDo, fmt.Errorf("%w: %s", errFailedToBuildBindingLastOperationRequest, fmt.Sprint(err))
	}

	resp, err := osb.PollBindingLastOperation(req)
	if err != nil {
		isHandled, action, handleErr := sb.handlePollError(ctx, kube, err)
		if isHandled {
			return action, handleErr
		}
		return common.NothingToDo, fmt.Errorf("%w: %s", errOSBPollBindingLastOperationRequestFailed, fmt.Sprint(err))
	}

	latest, err := util.GetLatestKubeObject(ctx, kube, sb)
	if err != nil {
		return common.NothingToDo, fmt.Errorf("%w, %s", errFailedToGetLastestKubeObject, fmt.Sprint(err))
	}

	util.UpdateStatusFromLastOp(latest, resp)

	return common.NothingToDo, nil
}

// ObserveState retrieves the current state of the ServiceBinding from the OSB (Open Service Broker) service.
//
// Parameters:
//   - ctx: the context for controlling cancellation and timeouts.
//   - kube: the Kubernetes client used to fetch and update resources.
//   - osb: the OSB client used to perform the GetBinding request.
//   - rotate: whether binding rotation should be considered.
//   - oid: the originating identity to include in the request.
//
// Returns three values:
//  1. An Action indicating the next step (common.NothingToDo, common.NeedToCreate, or common.NeedToUpdate).
//  2. The marshaled binding credentials as a map[string][]byte (nil if not available).
//  3. An error if any step fails.
//
// Behavior:
//  1. Retrieves binding data using GetDataFromServiceBinding.
//  2. If the ServiceBinding is in "in-progress" state, polls the last operation via handleLastOperationInProgress.
//  3. Returns early if the resource has no external name or if the broker indicates the binding is gone (NeedToCreate).
//  4. Builds a GetBinding request and sends it to the OSB broker using osb.GetBinding.
//  5. Updates the binding status and marshals credentials.
//  6. Handles binding rotation via HandleRenewalBindings (returns NeedToUpdate if rotation is required).
//  7. Validates that observed parameters match spec parameters (returns error if mismatch).
//  8. Adds finalizer if required.
//
//nolint:gocyclo // Ignore high cyclomatic complexity for this function
func (sb *ServiceBinding) ObserveState(ctx context.Context,
	kube client.Client,
	osb osbClient.Client,
	rotate bool,
	oid osbClient.OriginatingIdentity) (common.Action, map[string][]byte, error) {

	bindingData, err := sb.GetDataFromServiceBinding(ctx, kube)
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errCannotGetBindingData, fmt.Sprint(err))
	}

	// Manage pending async operations (poll only for "in progress" state)
	if sb.IsStateInProgress() {
		action, err := sb.handleLastOperationInProgress(ctx, kube, osb, bindingData, oid)
		if err != nil {
			return common.NothingToDo, nil, err
		}

		return action, nil, nil
	}

	// If the resource has no external name, it does not exist
	if sb.IsExternalNameEmpty() {
		return common.NeedToCreate, nil, nil
	}

	// get binding from broker
	req, err := sb.buildGetBindingRequest(bindingData)
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errFailedToBuildGetBindingRequest, fmt.Sprint(err))
	}

	resp, err := osb.GetBinding(req)
	if err != nil {
		if util.IsResourceGone(err) {
			return common.NeedToCreate, nil, nil
		}
		// Other errors are unexpected
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errOSBBindRequestFailed, fmt.Sprint(err))
	}

	err = sb.UpdateBindingStatusFromResponse(resp)
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errUpdateBindingStatus, fmt.Sprint(err))
	}

	credentialsJson, err := util.MarshalBindingCredentials(resp)
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errMarshalCredentials, fmt.Sprint(err))
	}

	// Manage binding rotation
	action, err := sb.HandleRenewalBindings(resp, rotate)
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errFailedToHandleRenewalBindings, fmt.Sprint(err))
	}

	switch action {
	case RenewalData:
		return common.NothingToDo, credentialsJson, nil
	case RotateBinding:
		return common.NeedToUpdate, credentialsJson, nil
	case NothingToDo:
		// Broker response is valid but missing metadata; cannot process renewal
		// or
		// Metadata present, but no renewal data
		// break
	default:
		return common.NothingToDo, nil, errCannotHandleUnknowAction
	}

	// Compare observed to response
	// If there is a diff, return an error, since bindings are not updatable
	isStatusParametersNotLikeSpecParameters, err := sb.IsStatusParametersNotLikeSpecParameters()
	if err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errStatusSpecCompareFailed, fmt.Sprint(err))
	}

	if isStatusParametersNotLikeSpecParameters {
		return common.NothingToDo, nil, errStatusSpecMismatch
	}

	if err = sb.AddRefFinalizer(ctx, kube); err != nil {
		return common.NothingToDo, nil, fmt.Errorf("%w: %s", errCannotAddFinalizer, fmt.Sprint(err))
	}

	return common.NothingToDo, credentialsJson, nil
}

// Bind performs the Service Binding creation (bind) operation.
// It retrieves the related instance and application data, converts the binding
// specifications into an Open Service Broker (OSB) request format, and sends
// the Bind request to the broker. It returns the broker’s BindResponse,
// a boolean indicating whether the operation is asynchronous, and any error encountered.
func (sb *ServiceBinding) Bind(ctx context.Context, kube client.Client, osb osbClient.Client, oid osbClient.OriginatingIdentity) (*osbClient.BindResponse, bool, error) {
	// Retrieve instance and application data for the binding.
	bindingData, err := sb.GetDataFromServiceBinding(ctx, kube)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s", errRetrieveBindingDataFailed, fmt.Sprint(err))
	}

	// Convert OSB context and parameters from the spec.
	requestContext, requestParams, err := sb.ConvertSpecsData()
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s", errConvertBindingSpecFailed, fmt.Sprint(err))
	}

	// Build OSB BindRequest.
	req, err := sb.buildBindRequest(bindingData, oid, requestContext, requestParams)
	if err != nil {
		return nil, false, fmt.Errorf("%w, %s", errFailedToBuildBindRequest, fmt.Sprint(err))
	}

	// Execute the OSB bind request and handle async responses.
	resp, async, err := sb.handleBindRequest(osb, req)
	if err != nil {
		return nil, false, err
	}

	return resp, async, err
}

// handleUnBindRequest sends an unbind request to the OSB (Open Service Broker) client
// for the current ServiceBinding and handles the response.
//
// It returns a boolean indicating whether the unbind operation is asynchronous,
// and an error if the request fails or the response is invalid.
//
// If the response indicates an asynchronous operation, it:
//  1. Updates the async status on the ServiceBinding using util.HandleAsyncStatus.
//  2. Ensures the async deletion finalizer is added to the ServiceBinding using util.HandleFinalizer.
//
// For synchronous unbind responses, it simply returns false with no error.
func (sb *ServiceBinding) handleUnBindRequest(
	ctx context.Context,
	kube client.Client,
	osb osbClient.Client,
	unbindRequest *osbClient.UnbindRequest,
) (bool, error) {
	resp, err := osb.Unbind(unbindRequest)
	if err != nil {
		return false, fmt.Errorf("%w: %s", errOSBUnbindRequestFailed, fmt.Sprint(err))
	}
	if resp == nil {
		return false, fmt.Errorf("%w: %s", errOSBUnbindNilResponse, fmt.Sprint(err))
	}

	if resp.Async {
		util.HandleAsyncStatus(sb, resp.OperationKey)

		if err := util.HandleFinalizer(ctx, kube, sb, asyncDeletionFinalizer, util.AddFinalizerIfNotExists); err != nil {
			return false, fmt.Errorf("%w: %s", errAddAsyncDeletionFinalizer, fmt.Sprint(err))
		}

		return true, nil
	}

	return false, nil
}

// UnBind handles the unbinding of a ServiceBinding from the OSB (Open Service Broker) service.
//
// Parameters:
//   - ctx: the context for controlling cancellation and timeouts.
//   - kube: the Kubernetes client used for fetching and updating resources.
//   - osb: the OSB client used to perform the unbind request.
//   - oid: the originating identity to include in the unbind request.
//
// Returns:
//   - A boolean indicating whether the unbind operation is asynchronous.
//   - An error if any step fails.
//
// Behavior:
//  1. Retrieves binding data (instance and application info) using GetDataFromServiceBinding.
//  2. Builds the unbind request using BuildUnbindRequest.
//  3. Executes the unbind request via handleUnBindRequest, handling asynchronous responses if necessary.
//  4. Returns true if the operation is asynchronous, false otherwise, along with any errors encountered.
func (sb *ServiceBinding) UnBind(ctx context.Context, kube client.Client, osb osbClient.Client, oid osbClient.OriginatingIdentity) (bool, error) {
	// Fetch binding data (instance & application)
	bindingData, err := sb.GetDataFromServiceBinding(ctx, kube)
	if err != nil {
		return false, fmt.Errorf("%w: %s", errRetrieveBindingDataFailed, fmt.Sprint(err))
	}

	req, err := sb.BuildUnbindRequest(bindingData, oid)
	if err != nil {
		return false, fmt.Errorf("%w: %s", errFailedToBuildUnbindRequest, fmt.Sprint(err))
	}

	// Execute the OSB bind request and handle async responses.
	isAsync, err := sb.handleUnBindRequest(ctx, kube, osb, req)
	if err != nil {
		return false, fmt.Errorf("%w: %s", errFailedToHandleBindRequest, fmt.Sprint(err))
	}

	return isAsync, nil
}

// extractCredentials marshals OSB BindResponse credentials into map[string][]byte.
func extractCredentials(resp *osbClient.BindResponse) (map[string][]byte, error) {
	creds := make(map[string][]byte, len(resp.Credentials))
	for key, value := range resp.Credentials {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("%w: '%s' from response: %s", errFailedToMarshalCredential, key, fmt.Sprint(err))
		}
		creds[key] = data
	}
	return creds, nil
}

// ResolveServiceInstance retrieves a ServiceInstance object from Kubernetes
// based on the InstanceRef provided in the ServiceBindingParameters spec.
// It returns an error if the referenced ServiceInstance does not exist
// or if any other Kubernetes client error occurs.
func (sbp *ServiceBindingParameters) resolveServiceInstance(ctx context.Context, kube client.Client) (instance.ServiceInstance, error) {
	if sbp.InstanceRef == nil {
		return instance.ServiceInstance{}, errInstanceRefEmpty
	}

	serviceInstance := instance.ServiceInstance{}
	if err := kube.Get(ctx, sbp.InstanceRef.ToObjectKey(), &serviceInstance); err != nil {
		if kerrors.IsNotFound(err) {
			return instance.ServiceInstance{}, err
		}
		return instance.ServiceInstance{}, err
	}
	return serviceInstance, nil
}

// resolveInstanceData handles fetching instance data either from reference or inlined.
func (sb *ServiceBinding) resolveInstanceData(
	ctx context.Context,
	kube client.Client,
	sbp ServiceBindingParameters,
) (*common.InstanceData, error) {
	if sbp.HasInstanceRef() {
		instance, err := sbp.resolveServiceInstance(ctx, kube)
		if err != nil {
			if kerrors.IsNotFound(err) {
				return nil, fmt.Errorf("%w: %s/%s", errInstanceNotFound, sb.Namespace, sb.Name)
			}
			return nil, fmt.Errorf("%w: %s", errInstanceFetchFailed, fmt.Sprint(err))
		}

		instanceData := &common.InstanceData{}
		instanceData.Set(instance.GetSpecForProvider())

		return instanceData, nil
	}

	if sbp.HasInstanceData() {
		return sbp.InstanceData, nil
	}

	return nil, errMissingInstanceData
}

// GetDataFromServiceBinding retrieves instance data required
// for handling a ServiceBinding. It supports fetching from either a direct reference
// (InstanceRef) or inlined InstanceData in the spec, and similarly for application data.
// Returns a BindingData struct containing both instance and application data,
// or an error if any required data is missing or cannot be retrieved.
func (sb *ServiceBinding) GetDataFromServiceBinding(
	ctx context.Context,
	kube client.Client,
) (BindingData, error) {

	if sb == nil {
		return BindingData{}, errServiceBindingEmpty
	}

	spec := sb.Spec.ForProvider

	instanceData, err := sb.resolveInstanceData(ctx, kube, spec)
	if err != nil {
		return BindingData{}, err
	}

	return BindingData{
		InstanceData: *instanceData,
	}, nil
}

// TriggerRotation initiates a rotation of the ServiceBinding's credentials with the OSB (Open Service Broker) service.
//
// Parameters:
//   - ctx: the context for controlling cancellation and timeouts.
//   - kube: the Kubernetes client used to fetch and update resources.
//   - osb: the OSB client used to perform the RotateBinding request.
//   - oid: the originating identity to include in the rotation request.
//
// Returns:
//   - A map of new credentials if the rotation is synchronous, or nil if the rotation is asynchronous.
//   - An error if any step of the rotation fails.
//
// Behavior:
//  1. Generates a new UUID to serve as the external name for the rotated binding.
//  2. Retrieves binding data from Kubernetes using GetDataFromServiceBinding.
//  3. Builds a RotateBinding request using buildRotateBindingRequest.
//  4. Sends the rotation request to the OSB broker using RotateBinding.
//     - If the request fails, returns an error.
//  5. Updates the binding's external name only if the rotation request succeeds.
//  6. If the rotation is synchronous, extracts credentials from the response and returns them.
//  7. If the rotation is asynchronous, updates the async status and last operation fields in the binding's status.
func (sb *ServiceBinding) TriggerRotation(ctx context.Context, kube client.Client, osb osbClient.Client, oid osbClient.OriginatingIdentity) (map[string][]byte, error) {
	newUUID := string(uuid.NewUUID())

	// Prepare binding rotation request
	bindingData, err := sb.GetDataFromServiceBinding(ctx, kube)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errCannotGetBindingData, fmt.Sprint(err))
	}

	req, err := sb.buildRotateBindingRequest(bindingData, oid, newUUID)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errFailedToBuildRotateBindingRequest, fmt.Sprint(err))
	}

	resp, err := osb.RotateBinding(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", errFailedRequestOSBRotateBinding, fmt.Sprint(err))
	}

	// Update the binding's external name only if the request succeeded
	sb.SetExternalName(newUUID)

	if !resp.Async {
		creds, err := extractCredentials(resp)
		if err != nil {
			return nil, err
		}
		return creds, nil
	}

	util.HandleAsyncStatus(sb, resp.OperationKey)

	sb.Status.AtProvider.LastOperationPolledTime = *util.TimeNow()
	sb.Status.AtProvider.LastOperationDescription = ""
	return nil, nil
}

// HasInstanceRef returns true if the ServiceBindingParameters has an associated InstanceRef.
func (sbp *ServiceBindingParameters) HasInstanceRef() bool {
	return sbp.InstanceRef != nil
}

// HasNoInstanceRef returns true if the ServiceBindingParameters does NOT have an associated InstanceRef.
func (sb *ServiceBinding) HasNoInstanceRef() bool {
	return sb.Spec.ForProvider.InstanceRef == nil
}

// GetInstanceRef returns the reference to the ServiceInstance that this
// ServiceBinding is associated with. The returned value is a pointer to a
// NamespacedName, containing the name and namespace of the referenced instance.
func (sbp *ServiceBindingParameters) GetInstanceRef() *common.NamespacedName {
	return sbp.InstanceRef
}

// IsBoundToInstance returns true if the ServiceBinding is linked to the ServiceInstance
// with the given name.
func (sb *ServiceBinding) IsBoundToInstance(instanceName string) bool {
	if sb.Spec.ForProvider.InstanceRef == nil {
		return false
	}
	return sb.Spec.ForProvider.InstanceRef.Name == instanceName
}

// IsNotBeingDeleted returns true if the ServiceBinding has not been marked for deletion.
func (sb *ServiceBinding) IsNotBeingDeleted() bool {
	return sb.DeletionTimestamp.IsZero()
}

// HasInstanceData returns true if the ServiceBindingParameters contains InstanceData.
// This indicates whether the binding has been associated with a ServiceInstance
// and has stored instance-specific information.
func (sbp *ServiceBindingParameters) HasInstanceData() bool {
	return sbp.InstanceData != nil
}

// ensureBindingUUID returns the existing external name or generates a new UUID if missing.
func (sb *ServiceBinding) EnsureBindingUUID() string {
	if id := meta.GetExternalName(sb); id != "" {
		return id
	}
	newID := string(uuid.NewUUID())
	meta.SetExternalName(sb, newID)
	return newID
}

// buildBindRequest creates an OSB BindRequest from the ServiceBinding spec and related data.
func (sb *ServiceBinding) buildBindRequest(
	bindingData BindingData,
	oid osbClient.OriginatingIdentity,
	ctxMap, params map[string]any,
) (osbClient.BindRequest, error) {

	if err := validateInputs(bindingData, oid); err != nil {
		return osbClient.BindRequest{}, err
	}

	bindingID := sb.EnsureBindingUUID()
	bindRequest := osbClient.BindRequest{
		BindingID:           bindingID,
		OriginatingIdentity: &oid,
		InstanceID:          bindingData.InstanceData.InstanceId,
		AcceptsIncomplete:   true, // TODO: make configurable
		PlanID:              bindingData.InstanceData.PlanId,
		ServiceID:           bindingData.InstanceData.ServiceId,
	}

	sb.addBindResource(&bindRequest)
	addContextAndParams(&bindRequest, ctxMap, params)

	return bindRequest, nil
}

// validateInputs handles all the simple validation checks.
func validateInputs(bindingData BindingData, oid osbClient.OriginatingIdentity) error {
	if oid.Platform == "" {
		return errOidPlatformIsEmpty
	}
	if oid.Value == "" {
		return errOidValueIsEmpty
	}
	if bindingData.InstanceData.InstanceId == "" {
		return errInstanceIdIsEmpty
	}
	if bindingData.InstanceData.PlanId == "" {
		return errPlanIdEmpty
	}
	if bindingData.InstanceData.ServiceId == "" {
		return errServiceIdEmpty
	}
	return nil
}

// addBindResource sets the BindResource and AppGUID.
func (sb *ServiceBinding) addBindResource(bindRequest *osbClient.BindRequest) {
	bindResource := &osbClient.BindResource{}

	if sb.Spec.ForProvider.AppGuid != "" {
		bindResource.AppGUID = &sb.Spec.ForProvider.AppGuid
		bindRequest.AppGUID = &sb.Spec.ForProvider.AppGuid
	}

	bindResource.Route = &sb.Spec.ForProvider.Route

	bindRequest.BindResource = bindResource
}

// addContextAndParams adds optional context and parameters.
func addContextAndParams(bindRequest *osbClient.BindRequest, ctxMap, params map[string]any) {
	if ctxMap != nil {
		bindRequest.Context = ctxMap
	}
	if params != nil {
		bindRequest.Parameters = params
	}
}

// buildUnbindRequest constructs an OSB UnbindRequest for the given ServiceBinding.
func (sb *ServiceBinding) BuildUnbindRequest(bindingData BindingData, oid osbClient.OriginatingIdentity) (*osbClient.UnbindRequest, error) {
	if oid.Platform == "" {
		return &osbClient.UnbindRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value == "" {
		return &osbClient.UnbindRequest{}, errOidValueIsEmpty
	}

	if bindingData.InstanceData.InstanceId == "" {
		return &osbClient.UnbindRequest{}, errInstanceIdIsEmpty
	}

	if bindingData.InstanceData.PlanId == "" {
		return &osbClient.UnbindRequest{}, errPlanIdEmpty
	}

	if bindingData.InstanceData.ServiceId == "" {
		return &osbClient.UnbindRequest{}, errServiceIdEmpty
	}

	bindingId := meta.GetExternalName(sb)
	if bindingId == "" {
		return &osbClient.UnbindRequest{}, errBindingIdEmpty
	}

	return &osbClient.UnbindRequest{
		InstanceID:          bindingData.InstanceData.InstanceId,
		BindingID:           bindingId,
		AcceptsIncomplete:   true, // TODO: make configurable
		ServiceID:           bindingData.InstanceData.ServiceId,
		PlanID:              bindingData.InstanceData.PlanId,
		OriginatingIdentity: &oid,
	}, nil
}

// buildBindingLastOperationRequest constructs an OSB BindingLastOperationRequest for polling.
func (sb *ServiceBinding) buildBindingLastOperationRequest(bindingData BindingData, oid osbClient.OriginatingIdentity) (*osbClient.BindingLastOperationRequest, error) {
	if oid.Platform == "" {
		return &osbClient.BindingLastOperationRequest{}, errOidPlatformIsEmpty
	}

	if oid.Value == "" {
		return &osbClient.BindingLastOperationRequest{}, errOidValueIsEmpty
	}

	if bindingData.InstanceData.InstanceId == "" {
		return &osbClient.BindingLastOperationRequest{}, errInstanceIdIsEmpty
	}

	if bindingData.InstanceData.PlanId == "" {
		return &osbClient.BindingLastOperationRequest{}, errPlanIdEmpty
	}

	if bindingData.InstanceData.ServiceId == "" {
		return &osbClient.BindingLastOperationRequest{}, errServiceIdEmpty
	}

	bindingId := meta.GetExternalName(sb)
	if bindingId == "" {
		return &osbClient.BindingLastOperationRequest{}, errBindingIdEmpty
	}

	return &osbClient.BindingLastOperationRequest{
		InstanceID:          bindingData.InstanceData.InstanceId,
		BindingID:           bindingId,
		ServiceID:           &bindingData.InstanceData.ServiceId,
		PlanID:              &bindingData.InstanceData.PlanId,
		OriginatingIdentity: &oid,
		OperationKey:        &sb.Status.AtProvider.LastOperationKey,
	}, nil
}

// markBindingIfExpired sets a false healthy condition if the binding has expired.
// Returns true if expired, false otherwise.
func (sb *ServiceBinding) MarkBindingIfExpired(expireAt time.Time) bool {
	if expireAt.Before(time.Now()) {
		cond := xpv1.Condition{
			Type:    xpv1.TypeHealthy,
			Status:  v1.ConditionFalse,
			Message: fmt.Sprintf("warning: the binding has expired %s", expireAt.Format(util.Iso8601dateFormat)),
		}
		sb.SetConditions(cond)
		return true
	}
	return false
}

// markBindingAsExpiringSoon sets a false healthy condition if the binding is about to expire.
func (sb *ServiceBinding) MarkBindingAsExpiringSoon(expireAt time.Time) {
	cond := xpv1.Condition{
		Type:    xpv1.TypeHealthy,
		Status:  v1.ConditionFalse,
		Message: fmt.Sprintf("warning: the binding will expire soon %s", expireAt.Format(util.Iso8601dateFormat)),
	}
	sb.SetConditions(cond)
}

// convertSpecsData converts the ServiceBinding spec's Context and Parameters
// from their Kubernetes types into plain map[string]any structures suitable for OSB requests.
// - Context is marshaled to JSON and then unmarshaled into a map.
// - Parameters, stored as raw JSON bytes, are unmarshaled into a map.
// Returns the converted context and parameters maps, or an error if conversion fails.
func (sb *ServiceBinding) ConvertSpecsData() (map[string]any, map[string]any, error) {
	requestContextBytes, err := json.Marshal(sb.Spec.ForProvider.Context)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: { values: %v: error: %s }", errFailedMarshallingContextFromServiceBinding, sb.Spec.ForProvider.Context, fmt.Sprint(err))
	}
	var requestContext map[string]any
	if err = json.Unmarshal(requestContextBytes, &requestContext); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", errFailedUnMarshallingRequestContext, fmt.Sprint(err))
	}

	// Convert spec.Parameters of type *apiextensions.JSON to map[string]any
	var requestParams map[string]any
	if err = json.Unmarshal([]byte(sb.Spec.ForProvider.Parameters), &requestParams); err != nil {
		return nil, nil, fmt.Errorf("%w: %s", errFailedUnMarshallingRequestParams, fmt.Sprint(err))
	}
	return requestContext, requestParams, nil
}

// handleBindRequest sends a bind request to the OSB (Open Service Broker) client
// for the current ServiceBinding and handles the response.
//
// Parameters:
//   - osb: the OSB client used to perform the bind request.
//   - bindRequest: the bind request object containing binding parameters.
//
// Returns three values:
//  1. A pointer to the BindResponse if the operation is synchronous; nil if asynchronous or on error.
//  2. A boolean indicating whether the bind operation is asynchronous (true if async).
//  3. An error if the request fails or the response is invalid.
//
// Behavior:
//   - Sends the bind request using osb.Bind.
//   - Returns an error if the request fails or the response is nil.
//   - If the response is asynchronous, updates the async status on the ServiceBinding
//     using util.HandleAsyncStatus and returns (nil, true, nil).
//   - For synchronous responses, returns the BindResponse for further processing.
func (sb *ServiceBinding) handleBindRequest(
	osb osbClient.Client,
	bindRequest osbClient.BindRequest,
) (*osbClient.BindResponse, bool, error) {

	resp, err := osb.Bind(&bindRequest)
	if err != nil {
		return nil, false, fmt.Errorf("%w: %s", errOSBBindRequestFailed, fmt.Sprint(err))
	}

	if resp == nil {
		return nil, false, fmt.Errorf("%w: %s", errOSBBindongIdIsNil, fmt.Sprint(err))
	}

	if resp.Async {
		util.HandleAsyncStatus(sb, resp.OperationKey)

		return nil, true, nil
	}

	// Synchronous response: return the OSB response for further processing
	return resp, false, nil
}

// UpdateBindingStatusFromResponse updates the ServiceBinding's status
// fields based on the data from the given OSB GetBindingResponse.
// It generates the binding data using CreateResponseData and applies it
// to the status using SetResponseDataInStatus. Returns an error if
// updating the status fails.
func (sb *ServiceBinding) UpdateBindingStatusFromResponse(resp *osbClient.GetBindingResponse) error {
	bindingData := sb.CreateResponseData(*resp)
	if err := sb.SetResponseDataInStatus(bindingData); err != nil {
		return err
	}
	return nil
}

type BindingAction int

const (
	RenewalData BindingAction = iota
	RotateBinding
	NothingToDo
)

// HandleRenewalBindings evaluates the renewal and expiration status of a ServiceBinding
// based on the provided GetBindingResponse from the OSB broker.
//
// Parameters:
//   - resp: the response from the OSB broker containing metadata about the binding.
//   - rotateBinding: a boolean indicating whether the binding should be rotated immediately
//     if it is expired.
//
// Returns three values:
//  1. A boolean indicating whether processing can continue (true if metadata is present but no renewal info).
//  2. A boolean indicating whether the binding should be rotated (true if expired and rotateBinding is set).
//  3. An error if there is any problem processing the response.
//
// Behavior:
//   - If resp or resp.Metadata is nil, returns an error to prevent panics.
//   - If Metadata.RenewBefore is empty, returns (true, false, nil) indicating no renewal action needed.
//   - Parses the RenewBefore and ExpiresAt times from ISO8601 strings.
//   - If the binding is expired:
//   - Marks the binding as expired.
//   - Returns (false, true, nil) if rotateBinding is true.
//   - Otherwise, marks the binding as expiring soon.
//   - Returns (false, false, nil) if no action is required.
func (sb *ServiceBinding) HandleRenewalBindings(resp *osbClient.GetBindingResponse, rotateBinding bool) (BindingAction, error) {
	if resp == nil {
		// Defensive: should never happen, but prevents future panics
		return NothingToDo, fmt.Errorf("%w:  %s/%s", errNilBindingResponse, sb.GetNamespace(), sb.GetName())
	}

	if resp.Metadata == nil {
		// Broker response is valid but missing metadata; cannot process renewal
		return NothingToDo, nil
	}

	if resp.Metadata.RenewBefore == "" {
		// Metadata present, but no renewal data
		return RenewalData, nil
	}

	renewBeforeTime, err := util.ParseISO8601Time(resp.Metadata.RenewBefore, "renewBefore")
	if err != nil {
		return NothingToDo, err
	}

	if renewBeforeTime.Before(time.Now()) {
		expireAtTime, err := util.ParseISO8601Time(resp.Metadata.ExpiresAt, "expiresAt")
		if err != nil {
			return NothingToDo, err
		}

		expired := sb.MarkBindingIfExpired(expireAtTime)

		if rotateBinding {
			return RotateBinding, nil
		} else if !expired {
			sb.MarkBindingAsExpiringSoon(expireAtTime)
		}
	}

	return NothingToDo, nil
}

// addRefFinalizer adds the reference finalizer to the ServiceInstance object referenced by the binding
// (if there is one)
func (sb *ServiceBinding) AddRefFinalizer(ctx context.Context, kube client.Client) error {
	// Add finalizer to referenced ServiceInstance
	instanceRef := sb.Spec.ForProvider.InstanceRef
	if instanceRef != nil {
		finalizerName := fmt.Sprintf("%s-%s", referenceFinalizerName, sb.Name)
		return util.HandleFinalizer(ctx, kube, sb, finalizerName, util.AddFinalizerIfNotExists)
	}
	return nil
}
