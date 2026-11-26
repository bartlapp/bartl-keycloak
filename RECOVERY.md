# Keycloak recovery and replacement

Use this runbook for the **v1alpha2 development checkout awaiting exact-candidate qualification**, on independent empty storage, replacement and supported replacement upgrades. Use the [README](README.md) for artifact availability, installation and routine operation; [DESIGN](DESIGN.md#package-and-qualification-scope) distinguishes the preceding `0.9.0-dev.6` evidence from the unqualified corrections. Stable 0.8.0 retains its release-specific procedures; historical results do not qualify changed artifacts or instructions.

## Before starting

Use workstation Bash and the [README prerequisites](README.md#prerequisites): retained installation/cluster context, operator tools and compatible backup access. Have the independently retained `installation.yaml`, protected database/repository inputs, administrator access to the recovered realm, backup history and encryption key available. The target cluster must already be prepared and independently accessible through its retained selector.

Run the sequence in one workstation Bash session; wait for a new shell prompt after each directory change so direnv selects the recorded context. Do not run the reachable-source branch against a lost source. The examples use the ordinary ACME paths; select the actual retained directories and recorded keys for the installation.

```bash
set -euo pipefail
umask 077
```

## Recover or replace a deployment

Recover or replace on independent storage with the selected exact v1alpha2 package. The package restores the latest matching complete point and performs supported native migration automatically. Before interruption, have target capacity, the target artifact, protected inputs, usable backup history and a way to retire the source. The installation's operating agreement supplies interruption, data-loss and acceptance limits.

| Recovery input | Supported contract |
| --- | --- |
| Point format | Format 1: one verified native PostgreSQL custom archive and its integrity/identity manifest. |
| Keycloak source data | 26.6.1 or 26.6.2, with the matching immutable image identity below. |
| PostgreSQL source | 17.6 with the package-pinned PostgreSQL image; PostgreSQL major changes are unsupported. |
| Destination procedure | Native Keycloak 26.6.2 startup migrates 26.6.1 directly or starts 26.6.2 state. No intermediate runtime image or operator-selected migration hop is needed for this pair. Downgrades are unsupported. |

The format-1 image identities accepted by this package are:

```text
quay.io/keycloak/keycloak:26.6.1@sha256:dea26401d06341095cc4ea9d66896200b55de5ca1daa1d2fcbe58493afa6e0ad
quay.io/keycloak/keycloak:26.6.2@sha256:f9ba7b2af90db8dc749a57ca9aedca51e840cb9224441ab546a968da941da900
docker.io/library/postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929
```

Source package/API versions are provenance, not migration hops or management state to adopt. The selected destination chart pins its required runtime/worker images; retain those immutable artifacts independently.

Source and target keep the same `VALUES`, logical instance and hostname, but use independent namespaces, PVCs, database passwords and deployment intent. Another namespace on the same host does not establish host/provider-loss protection.

Choose the starting mode:

| Starting condition | Source handling |
| --- | --- |
| Reachable source | Resume it as described below, capture final writes with PostgreSQL running, then retire all source processes. |
| Lost or already retired source | Use independently retained inputs and a verified recovery point; prevent a returning predecessor from resuming production work. |

Unreachability and DNS changes do not retire a source. This procedure covers planned replacement under exclusive Kubernetes administrative control and confirmed process termination, with ordinary users denied Kubernetes workload/CR mutation. Before starting, disable or pause every external reconciler, GitOps pipeline or scheduled script that could restart either placement; retain that exclusion until disposal. If these privileges or controls are unavailable, stop activation. This package does not supply provider-loss fencing: lost-source activation requires an already documented and independently enforceable dependency/infrastructure retirement interface. If none exists, target preparation is possible but activation is blocked.

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
  if [ -n "${CHART_ARCHIVE:-}" ]; then
    test "$(helm show chart "$CHART_ARCHIVE" | yq -er '.version')" = "$PACKAGE_VERSION"
    tar -xzf "$CHART_ARCHIVE" -C "$CHART_STAGE"
  else
    helm pull "$CHART_REF" --version "$PACKAGE_VERSION" --untar --untardir "$CHART_STAGE"
  fi
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
SOURCE_CONTEXT=$KUBE_CONTEXT
SOURCE_KUBECONFIG=$KUBECONFIG
SOURCE_NAMESPACE=$NS
SOURCE_API=$(kubectl --context "$KUBE_CONTEXT" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
SOURCE_CR_UID=$(kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o jsonpath='{.metadata.uid}')
```

**Lost or already retired source.** Skip the reachable-source commands. Select the independently retained installation root and source deployment key without contacting it. Set `SOURCE_DIR` to the recovered database/repository inputs and backup receipt; it need not contain a usable source context. For the ordinary example:

```bash
export WORK_DIR="${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation"
export VALUES="$WORK_DIR/installation.yaml"
SOURCE_DEPLOYMENT=keycloak
SOURCE_DIR="$WORK_DIR/clusters/cluster-1/applications/keycloak"
SOURCE_INSTANCE=$(jq -er '.instance' "$SOURCE_DIR/backup-result.json")
test "$(jq -er '.installation' "$SOURCE_DIR/backup-result.json")" = "$(yq -er '.installation' "$VALUES")"
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -e '.services[strenv(SOURCE_INSTANCE)] != null' "$VALUES" >/dev/null
```

Use retained source PVC/PV identities where available. A return is possible only after restoring the source directory's approved selector and independent cluster access; a protected-input copy alone is not a source context.

**Choose the target.** Choose an unused deployment key and an unoccupied blue/green namespace on the supplied target cluster. Slots exchange roles only after a retention decision releases the previous placement; never delete an occupied slot to make creation pass. A source using a pre-v1alpha2 CRD requires a **new target cluster**. No CRD conversion, stored-object migration or adoption is part of this procedure. Acquire the candidate as described in [Obtain the release](README.md#obtain-the-release); normal latest stable acquisition does not yet select it. Keep the source package entry unchanged:

For the supplied candidate, set `TARGET_CHART_ARCHIVE` to its checksum-verified archive. This selects the destination package independently of the source's retained chart.

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
if [ -n "${TARGET_CHART_ARCHIVE:-}" ]; then
  tar -xzf "$TARGET_CHART_ARCHIVE" -C "$TARGET_CHART_STAGE"
else
  helm pull oci://ghcr.io/bartlapp/bartl-keycloak --untar --untardir "$TARGET_CHART_STAGE"
fi
helm show chart "$TARGET_CHART_STAGE/bartl-keycloak" |
  yq -e '.annotations."keycloak.bartl.sh/supported-api" == "keycloak.bartl.sh/v1alpha2"' >/dev/null
TARGET_PACKAGE_VERSION=$(helm show chart "$TARGET_CHART_STAGE/bartl-keycloak" | yq -er '.version')
export TARGET_PACKAGE_VERSION
MATCHING_PACKAGE=$(yq -o=json '.packages // {}' "$VALUES" |
  jq -r --arg version "$TARGET_PACKAGE_VERSION" '[to_entries[] |
    select(.value.chart == "oci://ghcr.io/bartlapp/bartl-keycloak" and .value.version == $version) | .key][0] // empty')
if [ -n "$MATCHING_PACKAGE" ]; then TARGET_PACKAGE="$MATCHING_PACKAGE"; fi
yq -e '.packages[strenv(TARGET_PACKAGE)] == null or
  (.packages[strenv(TARGET_PACKAGE)].version == strenv(TARGET_PACKAGE_VERSION) and
   .packages[strenv(TARGET_PACKAGE)].chart == "oci://ghcr.io/bartlapp/bartl-keycloak")' "$VALUES" >/dev/null
yq -e '.deployments[strenv(TARGET_DEPLOYMENT)] == null' "$VALUES" >/dev/null
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '
  .packages[strenv(TARGET_PACKAGE)] = (.packages[strenv(TARGET_PACKAGE)] // {
    "version": strenv(TARGET_PACKAGE_VERSION), "chart": "oci://ghcr.io/bartlapp/bartl-keycloak"
  }) |
  .deployments[strenv(TARGET_DEPLOYMENT)] = {
    "instance": strenv(SOURCE_INSTANCE), "cluster": strenv(TARGET_CLUSTER),
    "namespace": strenv(TARGET_NAMESPACE), "release": "keycloak", "package": strenv(TARGET_PACKAGE)
  } |
  .operations.deployments[strenv(TARGET_DEPLOYMENT)] = {
    "ingressEnabled": false, "maintenance": true, "backupSuspend": true
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

The target selector is prepared and approved, but not entered. A reachable source remains selected for final capture and retirement. Package entries are immutable release identities: reuse an exact match, and give a different artifact a new package key rather than rewriting the source's entry.

For an already v1alpha2 source, inspect both installed and target CRD definitions before interruption and require compatibility for every remaining controller. Never let a new Helm install overwrite an incompatible cluster-wide definition. If compatibility cannot be established, choose a new target cluster. A separate namespace does not isolate the CRD.

For a replacement on the source's supplied cluster, compare the installed definition with a server-normalized dry run of the retained target definition. This includes Kubernetes defaults without changing the shared CRD:

```bash
if [ "$TARGET_CLUSTER" = "$CLUSTER_ID" ]; then
  kubectl --context "$KUBE_CONTEXT" get crd keycloakapplications.keycloak.bartl.sh -o json |
    jq -S '.spec' > "$TARGET_DIR/installed-crd-spec.json"
  helm show crds "$TARGET_CHART" |
    kubectl --context "$KUBE_CONTEXT" apply --server-side --dry-run=server -f - -o json |
    jq -S '.spec' > "$TARGET_DIR/target-crd-spec.json"
  cmp "$TARGET_DIR/installed-crd-spec.json" "$TARGET_DIR/target-crd-spec.json"
fi
```

An identical spec supports these package/controller versions on the existing shared definition. A difference requires an explicit compatibility assessment; without that evidence, use a new target cluster rather than changing the shared schema during cutover.

Choose the administrator's actual source CIDR for restricted acceptance. Ingress must preserve client IPs and enforce the allowlist without direct-service, NodePort or alternate-ingress bypass; maintenance restricts both login and administration.

```bash
export ADMIN_CIDR=203.0.113.10/32  # Replace with the authorized administrator's actual source CIDR.
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '.services[strenv(SOURCE_INSTANCE)].ingress.administratorSourceRanges = [strenv(ADMIN_CIDR)]' "$VALUES"
```

The supported package's native NetworkPolicy limits Keycloak egress to its own PostgreSQL, Keycloak peers and cluster DNS. It therefore isolates restored SMTP, LDAP/federation and remote custom-listener endpoints before startup, provided NetworkPolicy is enforced and no additional selecting policy grants egress. Do not add production dependency grants during acceptance. The executable policy inspection and probe below are mandatory; a policy object alone is not proof of enforcement. Extensions requiring additional network or persistent state need their own supported inventory and procedure before this run can proceed.

#### 2. Capture final writes and retire a reachable source

For a lost or retired source with independently enforced retirement, continue at [Prepare independent target access](#3-prepare-independent-target-access-storage-and-credentials). Otherwise, exclude concurrent Helm and manual backup operations during handover.

The source is still selected. For a v1alpha2 source, suspend schedules/withdraw access using its durable static CR settings, then reduce only Keycloak:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak --type=merge \
  -p '{"spec":{"backup":{"suspend":true},"ingress":{"enabled":false},"application":{"replicas":0}}}'
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Quiescent --timeout=60m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak \
  --for=jsonpath='{.status.application.activePods}'=0 --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deployment/keycloak --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete ingress/keycloak --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status statefulset/keycloak-db --timeout=5m
```

For a pre-v1alpha2 source, the patch above uses only its already supported replica/ingress/backup fields; do not send it `spec.running`. With released 0.8.0, Keycloak-only zero may report its historical Stopped condition; still require Quiescent, no Keycloak/worker processes and ready PostgreSQL. Keep its exact retained package/API unchanged; its [release-specific instructions](https://github.com/bartlapp/bartl-keycloak/blob/v0.8.0/README.md) remain authoritative. Target reconstruction uses backup data, never source Kubernetes state.

Confirm graceful drain, current-generation `Quiescent=True`, no Keycloak process or unresolved native Job and failed public login, including previously established connections. PostgreSQL must remain ready and `spec.running` true on v1alpha2 for final capture. Whole-application Stopped is deliberately not the condition at this stage. Resolve uncertain Jobs first; do not force-delete Pods to pass.

```bash
FINAL="keycloak-final-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$KUBE_CONTEXT" -n "$NS" create job "$FINAL" --from=cronjob/keycloak-backup
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "job/$FINAL" --for=condition=complete --timeout=60m
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs "job/$FINAL" -c backup > "$SOURCE_DIR/backup-result.json"
jq -e '.snapshotID | test("^[a-f0-9]{64}$")' "$SOURCE_DIR/backup-result.json"
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc -o json > "$SOURCE_DIR/source-pvc.json"
```

After a verified final backup and completed Jobs, stop the **whole v1alpha2 source** and retain durable stop intent:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak \
  --type=merge -p '{"spec":{"running":false}}'
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Stopped --timeout=60m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs,ingresses
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale deployment/keycloak-controller --replicas=0
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete pod \
  -l app.kubernetes.io/name=keycloak-controller --timeout=5m
```

For a pre-v1alpha2 source, use **only** this branch after its final verified capture, not the `spec.running` patch above:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale deployment/keycloak-controller --replicas=0
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete pod \
  -l app.kubernetes.io/name=keycloak-controller --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale statefulset/keycloak-db --replicas=0
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete pod/keycloak-db-0 --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs,ingresses
```

Keep the original API/package unchanged and record those release-specific observations.

Require no source component or native worker still running. Keep source PVCs intact and all restart automation disabled. Retain the source context/API/namespace, CR UID, PVC/PV identities, final snapshot and confirmed retirement with the intended active deployment in the installation's operating record. This record supports resumption; the stopped processes and excluded restart paths enforce retirement.

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

After retiring a legacy source, remove only that logical service's obsolete runtime inputs before target rendering. Do not copy them into the target's new runtime intent:

```bash
SOURCE_INSTANCE="$SOURCE_INSTANCE" yq -i '
  del(.services[strenv(SOURCE_INSTANCE)].application,
      .services[strenv(SOURCE_INSTANCE)].running,
      .services[strenv(SOURCE_INSTANCE)].replicas) |
  .deployments as $deployments |
  del(.operations.deployments[] | select(key as $id |
    $deployments[$id].instance == strenv(SOURCE_INSTANCE)) | .running) |
  del(.operations.deployments[] | select(key as $id |
    $deployments[$id].instance == strenv(SOURCE_INSTANCE)) | .replicas)
' "$VALUES"
```

Enter the prepared target **deployment** directory, keeping the same `VALUES`:

```bash
cd "$TARGET_DIR"
```

Wait for the next shell prompt: direnv selects target cluster access, placement and the already acquired version-specific `CHART`.

Check the target API endpoint before creating runtime resources:

```bash
ACTUAL_API=$(kubectl --context "$KUBE_CONTEXT" config view --minify -o jsonpath='{.clusters[0].cluster.server}')
EXPECTED_API=$(yq -r '.clusters[strenv(CLUSTER_ID)].observed.api.endpoint // ""' "$VALUES")
if [ -n "$EXPECTED_API" ]; then test "$ACTUAL_API" = "$EXPECTED_API"; fi
printf 'Selected target API: %s\n' "$ACTUAL_API"
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

Before any controller executes, inspect the target namespace for **all** egress policies. The package's Keycloak policy must remain the only egress grant selecting Keycloak; policies combine by union. Supplied clusters must provide enforced NetworkPolicy, CoreDNS's documented `kube-system`/`k8s-app=kube-dns` labels, and no host-network or admission mutation bypass. Otherwise stop and establish those prerequisites.

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
```

Create the target CR with explicitly selected **new** intent before controller execution. Do not copy the source CR or its stopped settings. Follow [Select initial runtime intent before controller execution](README.md#select-initial-runtime-intent-before-controller-execution), choosing `INITIAL_RUNNING=true INITIAL_REPLICAS=1`; its native `create` rejects an occupied CR. Keep the selected target's static ingress disabled, maintenance true and schedules suspended as recorded above, then run its Helm install command.

Continue after Helm installation:

```bash
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
  --force-conflicts --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
curl --fail --silent --show-error --retry 10 --retry-all-errors --retry-delay 2 \
  --retry-max-time 60 --max-time 10 "$KC_URL/realms/master/.well-known/openid-configuration" \
  -o "$INSTALL_DIR/restored-discovery.json"
jq -e --arg issuer "$KC_URL/realms/master" '.issuer == $issuer' "$INSTALL_DIR/restored-discovery.json"
```

The HTTPS probe allows the ingress controller to apply the new route after application reconciliation. It still requires trusted TLS and HTTP success; persistent failure blocks acceptance.

Require current-generation Ready, trusted TLS and unchanged issuer. From the allowed address, log in with the retained administrator in a fresh browser session. From an independent ordinary address, verify ordinary login and administration denial, also with forged forwarding headers. Check restored realm/client/user IDs and agreed login behavior including MFA/OIDC/PKCE where configured. Check ClusterIP-only service exposure, no alternate Ingress/NodePort path and denied direct Pod/Service access from the independent client.

Inspect outbound isolation using native resources:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" get networkpolicy -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get service/keycloak -o json | jq -e '.spec.type == "ClusterIP"'
kubectl --context "$KUBE_CONTEXT" get ingress --all-namespaces -o wide
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods -l app.kubernetes.io/name=keycloak -o wide
```

Check the Keycloak egress rules: only its own database on 5432, peers on 7800/57800 and cluster DNS on TCP/UDP 53 may be allowed. Check every other policy that selects these Pods; any broader egress grant blocks acceptance until its owner removes it. Use a known reachable production SMTP/LDAP/custom-listener endpoint from the installation's dependency inventory for an enforced-denial probe; choose its actual host/port once:

```bash
export EFFECT_HOST=smtp.example.net EFFECT_PORT=587
KC_POD=$(kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods \
  -l app.kubernetes.io/name=keycloak -o jsonpath='{.items[0].metadata.name}')
if kubectl --context "$KUBE_CONTEXT" -n "$NS" exec "$KC_POD" -c keycloak -- \
  /bin/bash -c 'timeout 5 bash -c "exec 3<>/dev/tcp/$1/$2"' bash "$EFFECT_HOST" "$EFFECT_PORT"; then
  printf 'Outbound isolation failed; do not activate\n' >&2
  exit 1
fi
```

Confirm that the failure is timeout/denial, not absent shell tooling or failed DNS, and that this endpoint is reachable from the authorized independent client. Repeat for each distinct production effect path. No configured external effects means an empty dependency inventory, not permission to invent a test endpoint. This stock network-isolated profile does not support enabling SMTP/federation/custom outbound integrations: such a change requires a separately supported, scoped policy and qualification, not deletion of isolation at activation.

Create an authorized disposable user in a restored realm, reopen its details to verify a new write/read, then remove it. Produce a new complete point from the recovered application:

```bash
JOB="keycloak-recovered-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$KUBE_CONTEXT" -n "$NS" create job "$JOB" --from=cronjob/keycloak-backup
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "job/$JOB" --for=condition=complete --timeout=60m
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs "job/$JOB" -c backup > "$INSTALL_DIR/recovered-backup-result.json"
jq -e --arg previous "$EXPECTED_SNAPSHOT" \
  '(.snapshotID | test("^[a-f0-9]{64}$")) and .snapshotID != $previous' \
  "$INSTALL_DIR/recovered-backup-result.json"
```

Check the receipt for successful readback and target application/PVC identities distinct from the source. The target is now accepted under restricted access, with ordinary access closed and scheduled backups suspended. In a composed installation, pause here until the coordinator has accepted the dependent services; keep outbound isolation and source retirement enforced.

#### 6. Open service

After standalone acceptance or the coordinator's activation decision, open ordinary access and schedules. Keep stock outbound isolation; no production egress policy is removed by this step.

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].maintenance = false |
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = false
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --force-conflicts --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

Verify current-generation Ready and normal client access. Record the active deployment, resolved package version, recovery time, selected point, storage identities and acceptance results in the installation record. Retain recovery-point identities and protected target inputs independently; the JSON/API output files used for these checks are disposable working files, not a permanent execution diary. Keep the source stopped until retention or destruction is authorized.

#### Interrupted cutover and a returning source

After any lost acknowledgement, do not infer which generation is active from DNS or a local receipt. Re-enter each reachable deployment's approved selector and inspect the live CR UID, `spec.running`, replica intent, ingress, component Pods and native Jobs; compare its recorded API and PVC/PV identities. Inspect authoritative DNS and actual allowed/denied HTTPS paths. Keep the old source stopped and its controller at zero. Enter `TARGET_DIR` and wait for its selector to load at the next prompt.

If the shell was lost, select the already recorded target deployment, not a new placement. Derive its directory from the retained installation record:

```bash
export WORK_DIR="${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation"
export VALUES="$WORK_DIR/installation.yaml"
export TARGET_DEPLOYMENT=keycloak-recovery
TARGET_CLUSTER=$(yq -er '.deployments[strenv(TARGET_DEPLOYMENT)].cluster' "$VALUES")
TARGET_INSTANCE=$(yq -er '.deployments[strenv(TARGET_DEPLOYMENT)].instance' "$VALUES")
TARGET_DIR="$WORK_DIR/clusters/$TARGET_CLUSTER/applications/$TARGET_INSTANCE"
if [ "$TARGET_DEPLOYMENT" != "$TARGET_INSTANCE" ]; then
  TARGET_DIR="$TARGET_DIR/deployments/$TARGET_DEPLOYMENT"
fi
cd "$TARGET_DIR"
```

Wait for the next prompt before continuing. Keep the recorded source identity and independent access; if those were lost too, recover them from the retained operating record before using a returning-source command.

From the selected target, inspect the native release and live authority before any new state-changing command:

```bash
test "$INSTALL_DIR" = "$TARGET_DIR"
helm status "$RELEASE" --kube-context "$KUBE_CONTEXT" -n "$NS" -o json |
  jq '{revision:.version,status:.info.status}'
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get deployments,statefulsets,pods,jobs,ingresses -o wide
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc -o json |
  jq '.items[] | {name:.metadata.name,uid:.metadata.uid,volume:.spec.volumeName}'
if [ -f "$INSTALL_DIR/recovery-result.json" ]; then
  test "$(kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o jsonpath='{.metadata.uid}')" = \
    "$(jq -er '.metadata.uid' "$INSTALL_DIR/recovery-result.json")"
fi
```

If target production writes/effects cannot be excluded, treat it as potentially authoritative, keep the source retired and withdraw only the selected target's ingress while resolving state:

```bash
test "$INSTALL_DIR" = "$TARGET_DIR"
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak \
  --type=merge -p '{"spec":{"ingress":{"enabled":false}}}'
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait --for=delete ingress/keycloak --timeout=5m
```

Resolve actual state with the responsible operator before reopening either generation. No DNS reversal, automatic fallback or restore replay resolves ambiguous writes.

When inspection establishes the recorded target UID and independent volume, current-generation Ready, the selected reconstruction point and enforced source retirement, that target remains authoritative. If its route is still restricted, resume [Open service](#6-open-service) with the same recorded package. If ordinary access is already enabled, verify actual ordinary HTTPS/application use and its backup schedule instead of replaying reconstruction or reverting to the source. A `pending-*`/failed Helm release is not a committed activation acknowledgement: keep its route withdrawn, retain the native release/CR/Job diagnostics and use the independent-target retry procedure when safe completion cannot be established. Do not edit Helm release Secrets or clear native execution markers.

A reachable returning v1alpha2 source is re-retired through its recorded context, without starting its controller or database first:

```bash
test "$(kubectl --kubeconfig "$SOURCE_KUBECONFIG" --context "$SOURCE_CONTEXT" config view --minify -o jsonpath='{.clusters[0].cluster.server}')" = "$SOURCE_API"
test "$(kubectl --kubeconfig "$SOURCE_KUBECONFIG" --context "$SOURCE_CONTEXT" -n "$SOURCE_NAMESPACE" get keycloakapplication/keycloak -o jsonpath='{.metadata.uid}')" = "$SOURCE_CR_UID"
kubectl --kubeconfig "$SOURCE_KUBECONFIG" --context "$SOURCE_CONTEXT" -n "$SOURCE_NAMESPACE" patch keycloakapplication/keycloak \
  --type=merge -p '{"spec":{"running":false,"ingress":{"enabled":false},"backup":{"suspend":true}}}'
kubectl --kubeconfig "$SOURCE_KUBECONFIG" --context "$SOURCE_CONTEXT" -n "$SOURCE_NAMESPACE" get pods,jobs,ingresses
```

Its retained whole-stop intent must already exclude component/worker execution even if the host/controller returns. If any source execution or effect is observed, isolate its production dependency credentials/access through the already approved infrastructure/dependency interfaces and stop target activation until authority is resolved. For an older source, do not restart its controller: repeat the recorded controller-then-database retirement commands and keep all restart automation excluded. Without usable source access or independently prepared enforcement, stop activation and report the missing retirement capability; this runbook does not manufacture provider-loss authority.

#### Failure and bounded return to the source

On failed or ambiguous native work, keep the target isolated and retain diagnostics. Correct inputs and repeat on another independent empty target; do not replay the failed target. See [troubleshooting](README.md#troubleshooting-and-operations).

Return to the source only if its state remains untouched and the target produced no consequential production writes or external effects. Otherwise establish authoritative state and reconcile it; DNS reversal or Helm rollback cannot merge writes.

For an authorized return, with the target still selected:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak \
  --type=merge -p '{"spec":{"running":false,"ingress":{"enabled":false},"backup":{"suspend":true}}}'
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Stopped --timeout=60m
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

Wait for the next shell prompt. Require the retained selector and independent cluster access; no chart acquisition is needed to return a v1alpha2 source:

```bash
test "$DEPLOYMENT" = "$SOURCE_DEPLOYMENT"
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale deployment/keycloak-controller --replicas=1
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deployment/keycloak-controller --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak --type=merge \
  -p "$(jq -nc --arg cidr "$ADMIN_CIDR" '{spec:{running:true,application:{replicas:1},ingress:{enabled:true,maintenance:true,administratorSourceRanges:[$cidr]},backup:{suspend:true}}}')"
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

For an authorized pre-v1alpha2 source return, use this branch **instead of** the running-intent block above:

```bash
test "$DEPLOYMENT" = "$SOURCE_DEPLOYMENT"
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale statefulset/keycloak-db --replicas=1
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status statefulset/keycloak-db --timeout=5m
kubectl --context "$KUBE_CONTEXT" -n "$NS" patch keycloakapplication/keycloak --type=merge \
  -p "$(jq -nc --arg cidr "$ADMIN_CIDR" '{spec:{application:{replicas:1},ingress:{enabled:true,maintenance:true,administratorSourceRanges:[$cidr]},backup:{suspend:true}}}')"
kubectl --context "$KUBE_CONTEXT" -n "$NS" scale deployment/keycloak-controller --replicas=1
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

An incompatible source CRD remains on its original cluster.

Return the hostname to the source ingress address if different. Verify trusted TLS, current-generation Ready and useful behavior under restriction before reopening access and schedules. A lost source without independent access is not a fallback.

