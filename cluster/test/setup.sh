#!/usr/bin/env bash
set -aeuo pipefail

echo "Running setup.sh"

echo "Creating the provider config with cluster admin permissions in cluster..."
SA=$(${KUBECTL} -n crossplane-system get sa -o name | grep provider-osb | sed -e 's|serviceaccount\/|crossplane-system:|g')
${KUBECTL} create clusterrolebinding provider-osb-admin-binding --clusterrole cluster-admin --serviceaccount="${SA}" --dry-run=client -o yaml | ${KUBECTL} apply -f -

echo "Creating the credentials secret to connect to the OSB broker"
CREDS=$(echo -n '{"user":"user","password":"pass"}' | base64 -w 0)
${KUBECTL} create secret generic osb-creds --from-literal=creds=${CREDS} -n crossplane-system --dry-run=client -o yaml | ${KUBECTL} apply -f -

cat <<EOF | ${KUBECTL} apply -f -
apiVersion: osb.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: osb-provider
  namespace: crossplane-system
spec:
  brokerUrl: "http://fake-broker-service.crossplane-system.svc.cluster.local:5000"
  osbVersion: "2.17"
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: osb-creds
      key: creds
EOF

cat <<EOF | ${KUBECTL} apply -f -
apiVersion: osb.m.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: osb-provider-async-disabled
  namespace: crossplane-system
spec:
  brokerUrl: "http://fake-broker-service.crossplane-system.svc.cluster.local:5000"
  osbVersion: "2.17"
  disableAsync: true
  credentials:
    source: Secret
    secretRef:
      namespace: crossplane-system
      name: osb-creds
      key: creds
EOF
