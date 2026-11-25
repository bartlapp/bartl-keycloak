# Keycloak recovery and replacement

Use this runbook for reconstruction on independent empty storage, replacement and supported replacement upgrades. For installation and routine operation, use the [README](README.md).

## Before starting

Use workstation Bash and the [README prerequisites](README.md#prerequisites): retained installation/cluster context, operator tools and compatible backup access. Have the independently retained `installation.yaml`, protected database/repository inputs, administrator access to the recovered realm, backup history and encryption key available. The target cluster must already be prepared and independently accessible through its retained selector.

Run the sequence in one workstation Bash session; wait for a new shell prompt after each directory change so direnv selects the recorded context. Do not run the reachable-source branch against a lost source. The examples use the ordinary ACME paths; select the actual retained directories and recorded keys for the installation.

```bash
set -euo pipefail
umask 077
```

## Recover or replace a deployment

Recover or replace the deployment on independent storage with the latest release. The package restores the latest matching backup and performs supported native migration automatically. Unsupported transitions stop without activating the target. Before interruption, have target capacity, the downloaded target package, protected inputs, usable backup history and a way to retire the source. The installation's operating agreement supplies interruption, data-loss and acceptance limits.

Source and target keep the same `VALUES`, logical instance and hostname, but use independent namespaces, PVCs, database passwords and deployment intent. Another namespace on the same host does not establish host/provider-loss protection.

Choose the starting mode:

| Starting condition | Source handling |
| --- | --- |
| Reachable source | Resume it as described below, capture final writes with PostgreSQL running, then retire all source processes. |
| Lost or already retired source | Use independently retained inputs and a verified recovery point; prevent a returning predecessor from resuming production work. |

Unreachability and DNS changes do not retire a source. Without enforceable retirement, do not activate the target.

For secondary recovery, use that repository's URL, credentials, encryption key and full **destination** snapshot ID (also after `restic copy`). Verify the matching point before reconstruction; lookup or integrity errors stop startup.

### Recover onto fresh storage

#### 1. Retain source inputs and choose the target

Choose exactly one source branch below, then continue at **Choose the target**.

**Reachable source.** Enter its actual deployment directory; for the ordinary example:

```bash
cd "${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation/clusters/cluster-1/applications/keycloak"
```

For a nested replacement, enter its `applications/<instance>/deployments/<deployment>` directory instead. Wait for the next shell prompt so direnv loads its approved deployment selector and cluster access. Acquire its recorded chart and render connections without changing the recorded version:

```bash
set -euo pipefail
umask 077
if [ ! -d "$CHART" ]; then
  mkdir -p "$(dirname "$CHART")"
  CHART_STAGE=$(mktemp -d "$WORK_DIR/packages/.keycloak.XXXXXX")
  helm pull "$CHART_REF" --version "$PACKAGE_VERSION" --untar --untardir "$CHART_STAGE"
  mv "$CHART_STAGE/bartl-keycloak" "$CHART"
  rm -r "$CHART_STAGE"
fi
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
KC_URL=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.url' "$INSTALL_DIR/connections.yaml")
KC_HOST=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.hostname' "$INSTALL_DIR/connections.yaml")
TLS_SECRET=$(yq -er '.credentials.bindings[strenv(INSTANCE)].tls.secretName' "$INSTALL_DIR/connections.yaml")
INGRESS_NAMESPACE=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.controllerNamespace' "$INSTALL_DIR/connections.yaml")
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
```

Retain the selected source context:

```bash
SOURCE_DEPLOYMENT=$DEPLOYMENT
SOURCE_INSTANCE=$INSTANCE
SOURCE_DIR=$INSTALL_DIR
```

**Lost or already retired source.** Skip the reachable-source commands. Select the independently retained installation root and source deployment key without contacting it. Set `SOURCE_DIR` to the recovered database/repository inputs and backup receipt; it need not contain a usable source context. For the ordinary example:

```bash
export WORK_DIR="${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation"
export VALUES="$WORK_DIR/installation.yaml"
SOURCE_DEPLOYMENT=keycloak
SOURCE_DIR="$WORK_DIR/clusters/cluster-1/applications/keycloak"
SOURCE_INSTANCE=$(SOURCE_DEPLOYMENT="$SOURCE_DEPLOYMENT" yq -er '.deployments[strenv(SOURCE_DEPLOYMENT)].instance' "$VALUES")
```

Use retained source PVC/PV identities where available. A return is possible only after restoring the source directory's approved selector and independent cluster access; a protected-input copy alone is not a source context.

**Choose the target.** From either branch, choose unused deployment and package keys, a supplied cluster and a fresh namespace. Download the latest release before interrupting a reachable source; keep its selected chart and package entry unchanged:

```bash
set -euo pipefail
umask 077
export TARGET_DEPLOYMENT=keycloak-recovery
export TARGET_CLUSTER=cluster-1
export TARGET_NAMESPACE=keycloak-green
export SOURCE_DEPLOYMENT
export TARGET_PACKAGE=keycloak-recovery
mkdir -p "$WORK_DIR/packages"
TARGET_CHART_STAGE=$(mktemp -d "$WORK_DIR/packages/.keycloak.XXXXXX")
helm pull oci://ghcr.io/bartlapp/bartl-keycloak --untar --untardir "$TARGET_CHART_STAGE"
TARGET_PACKAGE_VERSION=$(helm show chart "$TARGET_CHART_STAGE/bartl-keycloak" | yq -er '.version')
export TARGET_PACKAGE_VERSION
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '
  .packages[strenv(TARGET_PACKAGE)] = {
    "version": strenv(TARGET_PACKAGE_VERSION), "chart": "oci://ghcr.io/bartlapp/bartl-keycloak"
  } |
  .deployments[strenv(TARGET_DEPLOYMENT)] = {
    "instance": strenv(SOURCE_INSTANCE), "cluster": strenv(TARGET_CLUSTER),
    "namespace": strenv(TARGET_NAMESPACE), "release": "keycloak", "package": strenv(TARGET_PACKAGE)
  } |
  .operations.deployments[strenv(TARGET_DEPLOYMENT)] = {
    "replicas": 1, "ingressEnabled": false, "maintenance": true, "backupSuspend": true
  }
' "$VALUES"
TARGET_DIR="$WORK_DIR/clusters/$TARGET_CLUSTER/applications/$SOURCE_INSTANCE"
if [ "$TARGET_DEPLOYMENT" = "$SOURCE_INSTANCE" ]; then
  TARGET_CLUSTER_ENV=../../.envrc
else
  TARGET_DIR="$TARGET_DIR/deployments/$TARGET_DEPLOYMENT"
  TARGET_CLUSTER_ENV=../../../../.envrc
fi
mkdir -p "$TARGET_DIR" "$WORK_DIR/packages/$TARGET_PACKAGE"
(
  set -o noclobber
  printf 'source_env %s\nload_deployment "%s"\n' "$TARGET_CLUSTER_ENV" "$TARGET_DEPLOYMENT" > "$TARGET_DIR/.envrc"
)
TARGET_CHART="$WORK_DIR/packages/$TARGET_PACKAGE/$TARGET_PACKAGE_VERSION"
if [ ! -d "$TARGET_CHART" ]; then
  mv "$TARGET_CHART_STAGE/bartl-keycloak" "$TARGET_CHART"
fi
rm -r "$TARGET_CHART_STAGE"
direnv allow "$TARGET_DIR"
```

The target selector is prepared and approved, but not entered. A reachable source remains selected for final capture and retirement. Package entries are immutable release identities: a replacement gets a new package key rather than rewriting the source's entry.

On a shared cluster, source and target must support the CRD that will remain installed. Helm 4.3.2's server-side-apply install can update an existing cluster-wide definition; a separate namespace does not isolate this change. Inspect installed and target CRD revisions before interruption. An older package using a removed field may be rejected by schema admission before native startup; do not disable validation to bypass that refusal.

Choose the administrator's actual source CIDR for restricted acceptance. Ingress must preserve client IPs and enforce the allowlist without direct-service, NodePort or alternate-ingress bypass; maintenance restricts both login and administration.

```bash
export ADMIN_CIDR=203.0.113.10/32  # Replace with the authorized administrator's actual source CIDR.
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '.services[strenv(SOURCE_INSTANCE)].ingress.administratorSourceRanges = [strenv(ADMIN_CIDR)]' "$VALUES"
```

Before restored startup, isolate SMTP, federation, custom listeners and other production outbound effects using native controls or enforced network policy. Keep that isolation through testing; ingress restriction alone does not suppress those effects.

#### 2. Capture final writes and retire a reachable source

For a lost or retired source with independently enforced retirement, continue at [Prepare independent target access](#3-prepare-independent-target-access-storage-and-credentials). Otherwise, exclude concurrent Helm and manual backup operations during handover.

The source is still selected. Suspend schedules and withdraw access, then stop Keycloak:

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = true |
  .operations.deployments[strenv(DEPLOYMENT)].ingressEnabled = false |
  .operations.deployments[strenv(DEPLOYMENT)].replicas = 0
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Stopped --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete ingress/keycloak --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs
```

Confirm graceful drain, `Quiescent=True`, no remaining Keycloak process and failed public login. PostgreSQL must remain ready for final capture. Resolve uncertain Jobs first; do not force-delete Pods to pass this check.

```bash
FINAL="keycloak-final-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$KUBE_CONTEXT" -n "$NS" create job "$FINAL" --from=cronjob/keycloak-backup
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "job/$FINAL" --for=condition=complete --timeout=30m
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs "job/$FINAL" -c backup > "$SOURCE_DIR/backup-result.json"
jq -e '.snapshotID | test("^[a-f0-9]{64}$")' "$SOURCE_DIR/backup-result.json"
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc -o json > "$SOURCE_DIR/source-pvc.json"
```

After a verified final backup and completed Jobs, stop the source controller before PostgreSQL. A failed final backup blocks replacement:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale deployment/keycloak-controller --replicas=0
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete pod \
  -l app.kubernetes.io/name=keycloak-controller --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale statefulset/keycloak-db --replicas=0
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete pod/keycloak-db-0 --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods
```

Keep the source stopped and its PVC intact; disable automation that could restart it. Record retirement and the active generation in the installation's operating record.

#### 3. Prepare independent target access, storage and credentials

Select the full ID of the latest verified matching snapshot in the target repository. For the final primary capture:

```bash
EXPECTED_SNAPSHOT=$(jq -er '.snapshotID' "$SOURCE_DIR/backup-result.json")
```

For secondary recovery, select its verified destination snapshot ID as `EXPECTED_SNAPSHOT`, and update the configured repository URL and reader/writer Secret bindings in `installation.yaml` to that destination before continuing. Do not reuse a primary repository's snapshot ID or access credentials. The target connections below must describe the selected destination.

If the installation uses the optional service-version assertion, retain its source value and update it for the selected target package:

```bash
SOURCE_SERVICE_RELEASE=$(SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -r '.services[strenv(SOURCE_INSTANCE)].release // ""' "$VALUES")
if [ -n "$SOURCE_SERVICE_RELEASE" ]; then
  SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '.services[strenv(SOURCE_INSTANCE)].release = strenv(TARGET_PACKAGE_VERSION)' "$VALUES"
fi
```

Enter the prepared target **deployment** directory, keeping the same `VALUES`:

```bash
cd "$TARGET_DIR"
```

Wait for the next shell prompt: direnv selects target cluster access, placement and the already acquired version-specific `CHART`.

Check the target API endpoint before creating runtime resources:

```bash
EXPECTED_API=$(yq -er '.clusters[strenv(CLUSTER_ID)].observed.api.endpoint' "$VALUES")
test "$(kubectl --context "$KUBE_CONTEXT" config view --minify -o jsonpath='{.clusters[0].cluster.server}')" = "$EXPECTED_API"
test "$CHART" = "$TARGET_CHART"
kubectl --context "$KUBE_CONTEXT" get nodes
kubectl --context "$KUBE_CONTEXT" create namespace "$NS"
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
KC_URL=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.url' "$INSTALL_DIR/connections.yaml")
KC_HOST=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.hostname' "$INSTALL_DIR/connections.yaml")
TLS_SECRET=$(yq -er '.credentials.bindings[strenv(INSTANCE)].tls.secretName' "$INSTALL_DIR/connections.yaml")
INGRESS_NAMESPACE=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.controllerNamespace' "$INSTALL_DIR/connections.yaml")
```

Use a different source/target cluster-namespace tuple and empty target storage. Do not copy PVC annotations, application status or database-binding metadata. Keep database names and the recovered administrator; generate new database passwords:

```bash
mkdir -p "$INSTALL_DIR/credentials/database" "$INSTALL_DIR/credentials/database-admin"
cp "$SOURCE_DIR/credentials/database/username" "$INSTALL_DIR/credentials/database/username"
cp "$SOURCE_DIR/credentials/database/database" "$INSTALL_DIR/credentials/database/database"
printf postgres > "$INSTALL_DIR/credentials/database-admin/username"
(
  set -o noclobber
  openssl rand -hex 32 | tr -d '\n' > "$INSTALL_DIR/credentials/database/password"
  openssl rand -hex 32 | tr -d '\n' > "$INSTALL_DIR/credentials/database-admin/password"
)
```

Supply `INSTALL_DIR/repository/reader/` and `writer/` for the selected repository. Reuse retained source inputs only when the repository survives unchanged; secondary recovery needs its own access. Keep administrator access to the recovered realm independently available for browser acceptance; it is not copied into the target or injected into the controller.

```bash
for pair in credentials/database:database credentials/database-admin:databaseAdmin repository/reader:backupReader repository/writer:backupWriter; do
  directory=${pair%%:*}; role=${pair#*:}
  name=$(ROLE="$role" yq -er '.credentials.bindings[strenv(INSTANCE)][strenv(ROLE)].secretName' "$INSTALL_DIR/connections.yaml")
  kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret generic "$name" \
    --from-file="$INSTALL_DIR/$directory/"
done
```

For external TLS, supply the target certificate and private key:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret tls "$TLS_SECRET" \
  --cert="$INSTALL_DIR/tls/tls.crt" --key="$INSTALL_DIR/tls/tls.key"
```

Retain the new target credentials independently. With managed TLS, cert-manager creates the TLS Secret.

The target ingress must preserve real client addresses for restricted acceptance. Base establishes this during provisioning; an externally supplied cluster must provide equivalent behavior. Verify allowed and denied access through the application route before activation.

#### 4. Route DNS, install and inspect reconstruction

With source retirement enforced, discover the target ingress address:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$INGRESS_NAMESPACE" get service traefik -o wide
INGRESS_IPV4=$(kubectl --context "$KUBE_CONTEXT" -n "$INGRESS_NAMESPACE" get service traefik \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
printf '%s -> %s\n' "$KC_HOST" "$INGRESS_IPV4"
```

Point the authorized hostname at the target ingress if its address changed. Verify authoritative and public answers before HTTP-01 certificate waits; application ingress remains disabled until acceptance.

For deSEC, inspect the existing A record. Patch only a record owned by this service that still points at the retired source:

```bash
export DNS_ZONE
DNS_ZONE=${DNS_ZONE:-$(yq -er '.providers[.dns.provider].scope.zone // .dns.root' "$VALUES")}
: "${DESEC_TOKEN:?Supply the authorized DNS token in the environment}"
DNS_API="https://desec.io/api/v1/domains/$DNS_ZONE"
DNS_SUBNAME=${KC_HOST%."$DNS_ZONE"}
test "$DNS_SUBNAME.$DNS_ZONE" = "$KC_HOST"
printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error "$DNS_API/rrsets/$DNS_SUBNAME/A/" | jq .
```

If the address differs, patch the inspected record:

```bash
printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error --request PATCH \
    "$DNS_API/rrsets/$DNS_SUBNAME/A/" \
    --json "$(jq -nc --arg ip "$INGRESS_IPV4" '{records: [$ip]}')"
for resolver in ns1.desec.io ns2.desec.org; do
  dig @"$resolver" "$KC_HOST" A +norecurse +noall +comments +answer
done
```

Check authoritative `aa` answers for the target address, then public resolution before installation:

```bash
dig @1.1.1.1 +short "$KC_HOST" A
dig @8.8.8.8 +short "$KC_HOST" A
helm install "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 30m
if yq -e '.services[strenv(INSTANCE)].endpoints.https.tlsManaged' "$INSTALL_DIR/connections.yaml" >/dev/null; then
  kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "certificate/$TLS_SECRET" --for=condition=Ready --timeout=600s
fi
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=30m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o json > "$INSTALL_DIR/recovery-result.json"
jq -e --arg snapshot "$EXPECTED_SNAPSHOT" '
  .metadata.generation as $g | .status.observedGeneration == $g and
  any(.status.conditions[]; .type == "Ready" and .status == "True" and .observedGeneration == $g) and
  .status.operation.snapshot.id == $snapshot
' "$INSTALL_DIR/recovery-result.json"
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc -o json > "$INSTALL_DIR/target-pvc.json"
kubectl --context "$KUBE_CONTEXT" -n "$NS" get deployments,statefulsets,pods
```

Compare source and target PVC UIDs and backing PVs; they must be independent. Check the operation's repository and snapshot identity. A different snapshot is failed recovery even if the target reports Ready: keep it isolated. Reconstruction retains native Keycloak session behavior without forced logout or a new revocation cutoff. Authentication and authorization changes after the selected backup may be lost; security-incident recovery requires reconciliation of affected access before activation.

#### 5. Accept under restricted access

Confirm retirement of every source write path, including a returning predecessor. Then enable the target's administrator-restricted route:

```bash
yq -i '.operations.deployments[strenv(DEPLOYMENT)].ingressEnabled = true' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
curl --fail --silent --show-error --retry 10 --retry-all-errors --retry-delay 2 \
  --retry-max-time 60 --max-time 10 "$KC_URL/realms/master/.well-known/openid-configuration" \
  -o "$INSTALL_DIR/restored-discovery.json"
jq -e --arg issuer "$KC_URL/realms/master" '.issuer == $issuer' "$INSTALL_DIR/restored-discovery.json"
```

The HTTPS probe allows the ingress controller to apply the new route after application reconciliation. It still requires trusted TLS and HTTP success; persistent failure blocks acceptance.

Require current-generation Ready, trusted TLS and unchanged issuer. From the allowed address, log in with the retained administrator in a fresh browser session. From an independent ordinary address, verify that ordinary login and administration are denied. Check restored realm/client/user IDs and the agreed login behavior, including MFA/OIDC/PKCE where configured. Keep production outbound effects isolated; allow only dependencies needed for the test.

Create an authorized disposable user in a restored realm, reopen its details to verify a new write/read, then remove it. Produce a new complete point from the recovered application:

```bash
JOB="keycloak-recovered-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$KUBE_CONTEXT" -n "$NS" create job "$JOB" --from=cronjob/keycloak-backup
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "job/$JOB" --for=condition=complete --timeout=30m
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs "job/$JOB" -c backup > "$INSTALL_DIR/recovered-backup-result.json"
jq -e --arg previous "$EXPECTED_SNAPSHOT" \
  '(.snapshotID | test("^[a-f0-9]{64}$")) and .snapshotID != $previous' \
  "$INSTALL_DIR/recovered-backup-result.json"
```

Check the receipt for successful readback and target application/PVC identities distinct from the source. The target is now accepted under restricted access, with ordinary access closed and scheduled backups suspended. In a composed installation, pause here until the coordinator has accepted the dependent services; keep outbound isolation and source retirement enforced.

#### 6. Open service

After standalone acceptance or the coordinator's activation decision, restore approved dependencies and open ordinary access and schedules:

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].maintenance = false |
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = false
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

Verify current-generation Ready and normal client access. Record the active deployment, resolved package version, recovery time, selected point, storage identities and acceptance results in the installation record. Retain recovery-point identities and protected target inputs independently; the JSON/API output files used for these checks are disposable working files, not a permanent execution diary. Keep the source stopped until retention or destruction is authorized.

#### Failure and bounded return to the source

On failed or ambiguous native work, keep the target isolated and retain diagnostics. Correct inputs and repeat on another independent empty target; do not replay the failed target. See [troubleshooting](README.md#troubleshooting-and-operations).

Return to the source only if its state remains untouched and the target produced no consequential production writes or external effects. Otherwise establish authoritative state and reconcile it; DNS reversal or Helm rollback cannot merge writes.

For an authorized return, with the target still selected:

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].replicas = 0 |
  .operations.deployments[strenv(DEPLOYMENT)].ingressEnabled = false |
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = true
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Stopped --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete ingress/keycloak --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs
```

Confirm target stop, no unresolved work and withdrawal of its production effects before starting the source.

Restore the source's service-version assertion, if present:

```bash
if [ -n "$SOURCE_SERVICE_RELEASE" ]; then
  SOURCE_SERVICE_RELEASE="$SOURCE_SERVICE_RELEASE" yq -i \
    '.services[strenv(INSTANCE)].release = strenv(SOURCE_SERVICE_RELEASE)' "$VALUES"
fi
```

Explicitly enter the retained source deployment directory, not the example default deployment:

```bash
cd "$SOURCE_DIR"
```

Wait for the next shell prompt. Require the restored source selector and independent cluster access, then acquire its recorded chart and render connections without changing directory again:

```bash
set -euo pipefail
test "$DEPLOYMENT" = "$SOURCE_DEPLOYMENT"
umask 077
if [ ! -d "$CHART" ]; then
  mkdir -p "$(dirname "$CHART")"
  CHART_STAGE=$(mktemp -d "$WORK_DIR/packages/.keycloak.XXXXXX")
  helm pull "$CHART_REF" --version "$PACKAGE_VERSION" --untar --untardir "$CHART_STAGE"
  mv "$CHART_STAGE/bartl-keycloak" "$CHART"
  rm -r "$CHART_STAGE"
fi
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
KC_URL=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.url' "$INSTALL_DIR/connections.yaml")
KC_HOST=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.hostname' "$INSTALL_DIR/connections.yaml")
TLS_SECRET=$(yq -er '.credentials.bindings[strenv(INSTANCE)].tls.secretName' "$INSTALL_DIR/connections.yaml")
INGRESS_NAMESPACE=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.controllerNamespace' "$INSTALL_DIR/connections.yaml")
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
```

Retain maintenance while restoring its run/routing intent:

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].replicas = 1 |
  .operations.deployments[strenv(DEPLOYMENT)].ingressEnabled = true |
  .operations.deployments[strenv(DEPLOYMENT)].maintenance = true |
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = true
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
```

Return the hostname to the source ingress address if different. Verify trusted TLS, current-generation Ready and useful behavior under restriction before reopening access and schedules. A lost source without independent access is not a fallback.

