# provider-osb

## Overview

The **provider-osb** is a [Crossplane](https://crossplane.io/) provider that enables interaction with brokers compliant with the [Open Service Broker API (OSB API)](https://github.com/cloudfoundry/servicebroker) [specification](https://github.com/cloudfoundry/servicebroker/blob/v2.17/spec.md) to manage external services.
It declaratively manages, within Kubernetes, the lifecycle of **ServiceInstances** (provisioning, updating, deprovisioning) and **ServiceBindings** (binding, rotation and unbinding) through this provider's managed resources (instead of "through the Custom Resource Definitions (CRDs) provided by the provider").

## Features

* Declarative management of services through brokers compliant with the OSB specification
* Provisioning, updating, binding, deprovisioning and add unbinding
* Support for both synchronous and asynchronous operations
* Automatic injection of credentials into Kubernetes Secrets, matching those provided during the binding process

## Concrete Usage Examples

### Example ProviderConfig for Connecting to an OSB Broker

**ProviderConfig**: A configuration resource that defines the connection and authentication parameters for the OSB broker. It is referenced by all other provider resources.

```yaml
apiVersion: osb.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: my-osb-provider-config
spec:
  broker_url: http://0.0.0.0:5000
  osb_version: "2.13"
  credentials:
    source: Secret
    secretRef:
      namespace: my-osb-provider
      name: osb-creds
      key: creds
  disable_async: false
```

### Provisioning a Service (Example: Database)

**ServiceInstance**: A resource representing a provisioned instance of an external service, such as a database, cache, or other cloud service.

```yaml
apiVersion: instance.osb.m.crossplane.io/v1alpha1
kind: ServiceInstance
metadata:
  name: my-db-instance
  namespace: my-osb-provider
spec:
  providerConfigRef:
    name: my-osb-provider
    kind: ProviderConfig
  forProvider:
    appGuid: my-app-guid
    instanceId: 123e4567-e89b-12d3-a456-426614174000
    serviceId: mysql-service-id
    planId: 123e4567-e89b-12d3-a456-426614174000
    organizationGuid: 123e4567-e89b-12d3-a456-426614174000
    spaceGuid: 123e4567-e89b-12d3-a456-426614174000
    parameters: |
      {
        "version": "2.13",
        "configuration": {
          "worker_processes": "string",
          "worker_connections": 0
        }
      }
    context:
      platform: kubernetes
      clusterId: my-cluster-id
      namespace: my-osb-provider
      instanceName: my-db-instance
```

### Creating a Binding to Access the Service

**ServiceBinding**: A resource that establishes a connection between an Application and a ServiceInstance. It provides the application with the necessary information (such as credentials or secrets) to access the external service.

```yaml
apiVersion:  binding.osb.m.crossplane.io/v1alpha1
kind: ServiceBinding
metadata:
  name: my-db-binding
  namespace: my-osb-provider
spec:
  providerConfigRef:
    name: my-osb-provider
    kind: ProviderConfig
  forProvider:
    parameters: |
      {
        "backend_ip": "10.0.0.5",
        "server_name": "example.com",
        "ssl_certificate": "-----BEGIN CERTIFICATE----- ... -----END CERTIFICATE-----",
        "ssl_certificate_key": "-----BEGIN PRIVATE KEY----- ... -----END PRIVATE KEY-----"
      }
    context:
      clusterId: my-cluster-id
      instanceName: my-db-instance
      namespace: my-app-namespace
      platform: kubernetes
    appGuid: my-app-guid
    instanceId: 123e4567-e89b-12d3-a456-426614174000
    serviceId: mysql-service-id
```

## Installation

### Installation Prerequisites

Before installing **provider-osb**, ensure you have:

* A Kubernetes cluster (v1.20+ recommended)
* [Crossplane](https://crossplane.io/) installed
* Access to an OSB-compliant broker
* `kubectl` configured to access your cluster
* `make` installed on your system
* Access to the necessary Git repositories

### Clone the provider-osb Repository

```bash
git clone git@github.com:orange-cloudfoundry/provider-osb.git
cd provider-osb
```

### Initialize Submodules and Build the Provider

```bash
# Initialize the "build" submodule used for CI/CD
make submodules

# Build the provider
make build
```

### Development Installation

For local development with kind:

```bash
# Start a local Kubernetes cluster
make dev

# To clean up and restart
make dev-clean && make dev
```

## Configuration

After installation, you need to configure the provider so it can communicate with your OSB broker. Configuration is done via specific Kubernetes resources.

### Authentication

Create a secret containing the credentials for broker authentication:

```bash
kubectl create secret generic osb-creds \
  --from-literal=creds="your-broker-credentials" \
  -n my-osb-provider
```

Or in YAML:

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: osb-creds
  namespace: my-osb-provider
type: Opaque
stringData:
  creds: "your-broker-credentials"
```

### ProviderConfig

The `ProviderConfig` defines the connection parameters to the OSB broker and **must reference the secret created above** for authentication:

```yaml
apiVersion: osb.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: my-osb-provider-config
spec:
  broker_url: http://your-broker-url:5000
  osb_version: "2.13"
  credentials:
    source: Secret
    secretRef:
      namespace: my-osb-provider  # Same namespace as the secret
      name: osb-creds             # Name of the secret created above
      key: creds                  # Key containing the credentials
  disable_async: false
```

## Architecture Diagrams

### System Overview

The **provider-osb** integrates into the Crossplane ecosystem to enable management of external services via the Open Service Broker API (OSB API). It acts as a bridge between Kubernetes resources and OSB-compliant brokers.

#### Overall System Architecture

The following diagram illustrates the overall architecture and interactions between components:

<img src="/docs/images/overview.png" alt="System Overview Architecture" style="width: 70%; max-width: 800px;">

#### Interaction Sequence Diagram

This diagram shows the detailed sequence of interactions between Crossplane, the provider-osb, and the OSB broker:

<img src="/docs/images/sequence_diagram.png" alt="Interaction Sequence Diagram" style="width: 90%; max-width: 1000px;">

### OSB Resource Lifecycles

#### ServiceInstance – Full Lifecycle

The following diagram shows the complete lifecycle of a ServiceInstance, from creation to deletion:

<img src="/docs/images/ServiceInstance_life_cycle.png" alt="ServiceInstance Lifecycle" style="width: 80%; max-width: 900px;">

### Operations on ServiceInstances

The following diagrams detail each possible operation on a ServiceInstance:

#### Provisioning (Creation)

Process of creating a new service instance via the OSB API:

<img src="/docs/images/ServiceInstance_create.png" alt="ServiceInstance Creation" style="width: 65%; max-width: 700px;">

#### Update

Process of modifying the parameters of an existing instance:

<img src="/docs/images/ServiceInstance_update.png" alt="ServiceInstance Update" style="width: 65%; max-width: 700px;">

#### Deprovisioning (Deletion)

Process of fully deleting a service instance:

<img src="/docs/images/ServiceInstance_delete.png" alt="ServiceInstance Deletion" style="width: 65%; max-width: 700px;">

### Operations on ServiceBindings

The following diagrams illustrate the management of bindings for service access:

#### Binding Creation

Process of creating a binding to connect an application to a service:

<img src="/docs/images/ServiceBinding_create.png" alt="ServiceBinding Creation" style="width: 65%; max-width: 700px;">

#### Credentials Rotation

Process of renewing access credentials for the service:

<img src="/docs/images/ServiceBinding_rotate.png" alt="Credentials Rotation" style="width: 65%; max-width: 700px;">

#### Binding Deletion

Process of deleting an existing binding:

<img src="/docs/images/ServiceBinding_delete.png" alt="ServiceBinding Deletion" style="width: 65%; max-width: 700px;">

## Contribution Guidelines

Refer to Crossplane's [CONTRIBUTING.md] file for more information on how the Crossplane community prefers to work. The [Provider Development][provider-dev] guide may also be of use.

[CONTRIBUTING.md]: https://github.com/crossplane/crossplane/blob/master/CONTRIBUTING.md
[provider-dev]: https://github.com/crossplane/crossplane/blob/master/contributing/guide-provider-development.md
