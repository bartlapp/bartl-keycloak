# Bartlized Keycloak

Keycloak over HTTPS with its own PostgreSQL database. The package initializes empty state, reconstructs from a configured backup and performs supported native migrations.

These instructions target stable release [0.8.0](https://github.com/bartlapp/bartl-keycloak/releases/tag/v0.8.0). Earlier packages retain their release-specific recovery behavior and inputs.

- [Prerequisites](#prerequisites)
- [Install a new deployment](#install-a-new-deployment)
- [Resume and operate an existing deployment](#resume-an-existing-deployment)
- [Enable backups and trigger one now](#enable-backups-and-trigger-one-now)
- [Recover or replace a deployment](RECOVERY.md)
- [Troubleshooting](#troubleshooting-and-operations)
- [Remove a deployment](#removal)
- [Input reference](#helm-inputs-and-connections)

## Prerequisites

Use workstation Bash with the [operator tools](https://github.com/bartlapp/bartl-app/blob/main/docs/operator-workstation.md#check-the-workstation). Run the commands on the workstation unless a step names another location.

Start with a cluster prepared through the [K3s base installation](https://github.com/bartlapp/base-k3s/blob/main/README.md).

When adopting deployment selectors in an existing installation, first [refresh its Base context](https://github.com/bartlapp/base-k3s#refresh-an-existing-installations-context). Directory entry selects recorded context; chart acquisition remains explicit.

For backup, supply a compatible HTTPS Restic repository and its protected inputs.

## Select cluster access

```bash
cd "${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation/clusters/cluster-1"
```

```bash
kubectl --context "$KUBE_CONTEXT" get nodes
```

## Install a new deployment

For a new instance in a new namespace, [select cluster access](#select-cluster-access), then follow this path. For an existing instance, [resume](#resume-an-existing-deployment) or [recover](#recover-or-replace-a-deployment) instead.

```bash
set -euo pipefail
umask 077
```

### Obtain the release

Download the latest release into disposable staging and read its version. Helm's default latest selection excludes prereleases:

```bash
mkdir -p "$WORK_DIR/packages"
CHART_STAGE=$(mktemp -d "$WORK_DIR/packages/.keycloak.XXXXXX")
helm pull oci://ghcr.io/bartlapp/bartl-keycloak --untar --untardir "$CHART_STAGE"
export PACKAGE_VERSION
PACKAGE_VERSION=$(helm show chart "$CHART_STAGE/bartl-keycloak" | yq -er '.version')
```

After choosing the package key below, promote the complete chart to its version-specific cache. The registry supplies charts and images; cached files need no independent archive.

### Configure the installation

Choose unused deployment, instance and package keys and a new namespace. `INSTANCE` identifies the logical Keycloak service; `DEPLOYMENT` identifies this placement. The supported Helm release name is `keycloak`, one per namespace.

The compound command records the choices, creates the selector and enters the deployment directory before direnv reloads at the next prompt. An ordinary deployment uses the instance directory; a different deployment key uses a nested replacement directory. The selector contains no copied configuration.

```bash
{
export DEPLOYMENT=keycloak
export INSTANCE=keycloak
export NS=keycloak-blue
export RELEASE=keycloak
export PACKAGE=keycloak

yq -i '
  .deployments[strenv(DEPLOYMENT)] = {
    "instance": strenv(INSTANCE), "cluster": strenv(CLUSTER_ID),
    "namespace": strenv(NS), "release": strenv(RELEASE), "package": strenv(PACKAGE)
  } |
  .services[strenv(INSTANCE)] = {} |
  .packages[strenv(PACKAGE)] = {
    "version": strenv(PACKAGE_VERSION), "chart": "oci://ghcr.io/bartlapp/bartl-keycloak"
  }
' "$VALUES"
DEPLOYMENT_DIR="$WORK_DIR/clusters/$CLUSTER_ID/applications/$INSTANCE"
if [ "$DEPLOYMENT" = "$INSTANCE" ]; then
  CLUSTER_ENV=../../.envrc
else
  DEPLOYMENT_DIR="$DEPLOYMENT_DIR/deployments/$DEPLOYMENT"
  CLUSTER_ENV=../../../../.envrc
fi
mkdir -p "$DEPLOYMENT_DIR" "$WORK_DIR/packages/$PACKAGE"
(
  set -o noclobber
  printf 'source_env %s\nload_deployment "%s"\n' "$CLUSTER_ENV" "$DEPLOYMENT" > "$DEPLOYMENT_DIR/.envrc"
)
if [ ! -d "$WORK_DIR/packages/$PACKAGE/$PACKAGE_VERSION" ]; then
  mv "$CHART_STAGE/bartl-keycloak" "$WORK_DIR/packages/$PACKAGE/$PACKAGE_VERSION"
fi
rm -r "$CHART_STAGE"
direnv allow "$DEPLOYMENT_DIR"
cd "$DEPLOYMENT_DIR"
}
```

Wait for the next shell prompt: direnv loads the deployment, including `INSTALL_DIR` and the normalized chart root `CHART`.

Set hostname, storage, TLS or backup overrides under `services.<instance>`; see the [input reference](#helm-inputs-and-connections). The default hostname is `login.<dns.root>`. For external certificates set `tls.managed: false`. Derive the hostname and credential names from the selected chart:

```bash
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
KC_URL=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.url' "$INSTALL_DIR/connections.yaml")
KC_HOST=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.hostname' "$INSTALL_DIR/connections.yaml")
TLS_SECRET=$(yq -er '.credentials.bindings[strenv(INSTANCE)].tls.secretName' "$INSTALL_DIR/connections.yaml")
INGRESS_NAMESPACE=$(yq -er '.services[strenv(INSTANCE)].endpoints.https.controllerNamespace' "$INSTALL_DIR/connections.yaml")
```

Supply credentials under `INSTALL_DIR` at the paths below; `installation.yaml` is the values file.

### Prepare DNS

Inspect the selected ingress Service and use its public IPv4. If the environment supplies an address independently, use that authoritative address instead.

```bash
kubectl --context "$KUBE_CONTEXT" -n "$INGRESS_NAMESPACE" get service traefik -o wide
INGRESS_IPV4=$(kubectl --context "$KUBE_CONTEXT" -n "$INGRESS_NAMESPACE" get service traefik \
  -o jsonpath='{.status.loadBalancer.ingress[0].ip}')
printf '%s -> %s\n' "$KC_HOST" "$INGRESS_IPV4"
```

Point `KC_HOST` to `INGRESS_IPV4` at the authorized DNS provider. Reuse a matching record and resolve any conflicting AAAA record. Managed TLS needs this route before certificate issuance.

For deSEC, supply `DESEC_TOKEN` and select the zone. Inspect its current records:

```bash
export DNS_ZONE=acme.fke.systems
: "${DESEC_TOKEN:?Supply the authorized DNS token in the environment}"
DNS_API="https://desec.io/api/v1/domains/$DNS_ZONE"
DNS_SUBNAME=${KC_HOST%."$DNS_ZONE"}
test "$DNS_SUBNAME.$DNS_ZONE" = "$KC_HOST"
printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error "$DNS_API/"
printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error "$DNS_API/rrsets/" | jq .
```

For an absent, authorized A record only, create it with the zone's minimum TTL:

```bash
DNS_TTL=$(printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error "$DNS_API/" | jq -er '.minimum_ttl')
printf 'header = "Authorization: Token %s"\n' "$DESEC_TOKEN" |
  curl --config - --fail --silent --show-error --request POST "$DNS_API/rrsets/" \
    --json "$(jq -nc --arg subname "$DNS_SUBNAME" --arg ip "$INGRESS_IPV4" \
      --argjson ttl "$DNS_TTL" '{subname: $subname, type: "A", ttl: $ttl, records: [$ip]}')"
for resolver in ns1.desec.io ns2.desec.org; do
  dig @"$resolver" "$KC_HOST" A +norecurse +noall +comments +answer
done
```

Check both authoritative `aa` answers for the selected address, then check public resolution:

```bash
dig "$KC_HOST" A +trace
dig @1.1.1.1 +short "$KC_HOST" A
dig @8.8.8.8 +short "$KC_HOST" A
```

### Prepare the installation files

For a new instance, generate credentials below or supply them at the same paths. Do not regenerate credentials after partial creation.

```bash
mkdir -p "$INSTALL_DIR/credentials/database" "$INSTALL_DIR/credentials/database-admin" \
  "$INSTALL_DIR/credentials/bootstrap"
(
  set -o noclobber
  printf keycloak > "$INSTALL_DIR/credentials/database/username"
  printf keycloak > "$INSTALL_DIR/credentials/database/database"
  openssl rand -hex 32 | tr -d '\n' > "$INSTALL_DIR/credentials/database/password"
  printf postgres > "$INSTALL_DIR/credentials/database-admin/username"
  openssl rand -hex 32 | tr -d '\n' > "$INSTALL_DIR/credentials/database-admin/password"
  printf admin > "$INSTALL_DIR/credentials/bootstrap/username"
  openssl rand -hex 32 | tr -d '\n' > "$INSTALL_DIR/credentials/bootstrap/password"
)
```

Retain credentials independently before relying on recovery; Kubernetes Secrets are runtime copies.

#### External backup inputs

Without a repository or resolved binding, backup is disabled. If none is selected, continue at [Install and inspect](#install-and-inspect).

For backup, supply a per-instance initialized HTTPS Restic repository meeting the [backup boundary](https://github.com/bartlapp/bartl-app/blob/main/docs/application-contract.md#portable-recovery-artifact-and-storage-boundary). For a fresh instance it must contain no Keycloak history matching this installation and instance. An untagged synthetic endpoint-verification point does not match. Existing matching application history belongs in recovery, with its retained recovery administrator.

Supply the repository URL and access credentials; backup-service provisioning is separate:

```bash
export REPOSITORY='rest:https://backup.example.net/keycloak/'
yq -i '.services[strenv(INSTANCE)].backup.repository = strenv(REPOSITORY)' "$VALUES"
```

Supply `repository/reader/` and `repository/writer/` under `INSTALL_DIR`, each containing:

| File | Content |
| --- | --- |
| `password` | The retained Restic encryption key, identical for reader and writer |
| `username` | The role's repository access username |
| `restPassword` | That user's access password |
| `ca.crt` | The repository's private CA certificate, only when needed |

Reader access needs GET/HEAD and POST/DELETE of native `locks/<64-lowercase-hex>` objects, not backup-content mutation. Writer access is read/append. Keep destructive maintenance credentials separate. Use native exclusive locking, never `--no-lock` or automatic unlock.

Retain history, keys and access outside the covered failure domain. Provider-loss recovery requires independently usable secondary history. Allocate space for a complete native dump plus Restic working data.

When local application state is absent, the package consults the configured repository. Lookup or integrity errors stop startup; they do not authorize fresh initialization. Existing state starts without backup access.

### Install and inspect

Refresh declarations after selecting inputs, then create the namespace and runtime Secrets:

```bash
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
TLS_SECRET=$(yq -er '.credentials.bindings[strenv(INSTANCE)].tls.secretName' "$INSTALL_DIR/connections.yaml")
kubectl --context "$KUBE_CONTEXT" create namespace "$NS"
for pair in database:database database-admin:databaseAdmin bootstrap:bootstrapAdmin; do
  directory=${pair%%:*}; role=${pair#*:}
  name=$(ROLE="$role" yq -er '.credentials.bindings[strenv(INSTANCE)][strenv(ROLE)].secretName' "$INSTALL_DIR/connections.yaml")
  kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret generic "$name" \
    --from-file="$INSTALL_DIR/credentials/$directory/"
done
```

With backup configured, create its runtime Secrets:

```bash
for pair in reader:backupReader writer:backupWriter; do
  directory=${pair%%:*}; role=${pair#*:}
  name=$(ROLE="$role" yq -er '.credentials.bindings[strenv(INSTANCE)][strenv(ROLE)].secretName' "$INSTALL_DIR/connections.yaml")
  kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret generic "$name" \
    --from-file="$INSTALL_DIR/repository/$directory/"
done
```

For external TLS, supply a certificate chain covering `KC_HOST` and its private key:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret tls "$TLS_SECRET" \
  --cert="$INSTALL_DIR/tls/tls.crt" --key="$INSTALL_DIR/tls/tls.key"
```

Install the chart:

```bash
helm install "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
if yq -e '.services[strenv(INSTANCE)].endpoints.https.tlsManaged' "$INSTALL_DIR/connections.yaml" >/dev/null; then
  kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "certificate/$TLS_SECRET" \
    --for=condition=Ready --timeout=600s
fi
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o json | \
  jq -e '.metadata.generation as $g | .status.observedGeneration == $g and
    any(.status.conditions[]; .type == "Ready" and .status == "True" and .observedGeneration == $g)'
kubectl --context "$KUBE_CONTEXT" -n "$NS" get deployments,statefulsets,pods
```

Helm waits for controller infrastructure; current-generation `Ready=True` and a fresh HTTPS login establish application usability. For a failed readiness check, inspect:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs deployment/keycloak -c keycloak --tail=80
kubectl --context "$KUBE_CONTEXT" -n "$NS" get events --sort-by=.lastTimestamp
```

### Verify Keycloak

```bash
curl --fail --silent --show-error "$KC_URL/realms/master/.well-known/openid-configuration" \
  -o "$INSTALL_DIR/master-discovery.json"
printf 'Administration: %s/admin/\n' "$KC_URL"
cat "$INSTALL_DIR/credentials/bootstrap/username"; printf '\n'
cat "$INSTALL_DIR/credentials/bootstrap/password"; printf '\n'
```

Sign in through the HTTPS hostname in a fresh browser session and verify the administration console. Check the agreed useful behavior, including configured realm login, MFA and client redirects/token flow.

### Retain operating and recovery inputs

Retain the installation record and protected credentials independently, together with backup history and its encryption key when backup is selected. The record identifies the package used; charts and images remain available from the registry.

Retain administrator access to the backed-up realm for operator acceptance and subsequent administration, including any required MFA recovery material. This is not a runtime restore input: the controller does not require an administrator account or password grant. For a fresh instance whose initial credentials are still current:

```bash
mkdir "$INSTALL_DIR/recovery-admin"
cp "$INSTALL_DIR/credentials/bootstrap/username" "$INSTALL_DIR/recovery-admin/username"
cp "$INSTALL_DIR/credentials/bootstrap/password" "$INSTALL_DIR/recovery-admin/password"
```

Update these operator-held files when that administrator account rotates. Fresh bootstrap credentials cannot administer restored state. Do not create a runtime recovery-administrator Secret.

## Resume an existing deployment

Enter the actual deployment directory. For the ordinary example:

```bash
cd "${WORK_DIR_ROOT:-$HOME/bartl}/acme/acme-installation/clusters/cluster-1/applications/keycloak"
```

For a nested replacement, enter its `applications/<instance>/deployments/<deployment>` directory instead. Wait for the next shell prompt: direnv loads cluster access and the recorded deployment/package through its approved selector. Do not select a deployment by exporting its key.

For an existing directory without a selector, first [refresh its Base context and adopt deployment selectors](https://github.com/bartlapp/base-k3s#refresh-an-existing-installations-context), then enter the deployment directory again.

Acquire the recorded chart only if its cache is absent, then render connections. A pull becomes visible in `CHART` only after successful extraction:

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

### Apply changes to a running deployment

Starting condition: a running, Ready deployment with no admitted capture or transition still executing. Edit the intended retained fields, then repeat **Resume an existing deployment** to refresh context and declarations. For scheduling changes, preserve the current run and routing intent and apply:

```bash
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

Use the dedicated stop, replacement and upgrade procedures for those operations. This does not cancel already admitted work or authorize a native version transition.

### Restart Keycloak

With the deployment selected:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout restart deployment/keycloak
kubectl --context "$KUBE_CONTEXT" -n "$NS" rollout status deployment/keycloak --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o json | \
  jq -e '.metadata.generation as $g | .status.observedGeneration == $g and
    any(.status.conditions[]; .type == "Ready" and .status == "True" and .observedGeneration == $g)'
```

Verify a fresh login and existing realms, clients and users. Normal restart uses existing database state without backup access or session reset.

### Persistently stop and start Keycloak

Set persistent stop intent; scaling the workload alone is undone by the controller:

```bash
yq -i '.operations.deployments[strenv(DEPLOYMENT)].replicas = 0' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Stopped --timeout=10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get deployments,statefulsets,pods
```

Require no Keycloak process, including startup work; a fresh login must fail. PostgreSQL and the controller remain available. `Stopped` does not imply `Quiescent`: inspect outstanding Jobs before another state-changing operation.

Start again:

```bash
yq -i '.operations.deployments[strenv(DEPLOYMENT)].replicas = 1' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait keycloakapplication/keycloak --for=condition=Ready --timeout=10m
```

Verify a fresh login and retained data. Changing replicas does not clear a failed or ambiguous transition.

## Enable backups and trigger one now

Resume the deployment first. PostgreSQL must be running; Keycloak need not be running for capture.

### Prepare or reuse backup access

If no repository is configured, supply [external backup inputs](#external-backup-inputs). Reuse existing repository credentials and Secret references.

Enable backup; this overrides `backup.enabled: false` without changing its schedule or suspension:

```bash
yq -i '.services[strenv(INSTANCE)].backup.enabled = true' "$VALUES"
```

If the runtime backup Secrets do not already exist, create them:

```bash
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml > "$INSTALL_DIR/connections.yaml"
for pair in reader:backupReader writer:backupWriter; do
  directory=${pair%%:*}; role=${pair#*:}
  name=$(ROLE="$role" yq -er '.credentials.bindings[strenv(INSTANCE)][strenv(ROLE)].secretName' "$INSTALL_DIR/connections.yaml")
  kubectl --context "$KUBE_CONTEXT" -n "$NS" create secret generic "$name" \
    --from-file="$INSTALL_DIR/repository/$directory/"
done
```

Retain operator access to the backed-up realm independently for post-recovery acceptance. It is not required by the automatic restore/start procedure.

### Capture a backup

Apply the selected chart and capture:

```bash
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
JOB="keycloak-backup-$(date -u +%Y%m%d%H%M%S)"
kubectl --context "$KUBE_CONTEXT" -n "$NS" create job "$JOB" --from=cronjob/keycloak-backup
kubectl --context "$KUBE_CONTEXT" -n "$NS" wait "job/$JOB" --for=condition=complete --timeout=30m
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs "job/$JOB" -c backup \
  | tee "$INSTALL_DIR/backup-result.json"
jq -e '.snapshotID | test("^[a-f0-9]{64}$")' "$INSTALL_DIR/backup-result.json"
```

Check Job success and the final JSON receipt for the installation, application/PVC/execution, database, repository, full snapshot and package/images. Success includes dump validation and readback of that published point.

Defaults are daily `0 2 * * *` in `UTC`. Existing schedule and suspension settings remain authoritative. `operations.deployments.<deployment>.backupSuspend: true` stops schedules, not manual or admitted Jobs. `services.<instance>.backup.enabled: false` removes the schedule, not admitted work.

The worker deadline is 25 minutes and Job deadline 30 minutes, without automatic replay. A client timeout does not cancel execution. Inspect failed Jobs and logs before another operation; interrupted publication is not a completed backup. Never unlock until its execution is known to have ended.

Backup failure does not stop a healthy application. Monitoring, capacity, independent replication, retention and destructive maintenance belong to the backup operator.

## Recover or replace a deployment

Follow the [recovery and replacement runbook](RECOVERY.md) for reconstruction on independent storage, replacement and supported replacement upgrades. It includes source retirement, restored-state acceptance, a new backup and bounded return to the source.

Recovery preserves native Keycloak session behavior; it does not force logout or advance realm revocation cutoffs. Authentication and authorization state comes from the selected backup, so later session revocations, password changes, account disablements and permission changes may be lost. For security-incident recovery, reconcile affected identities and access before opening service. Private startup, verified reconstruction and source retirement remain required.

Remove any explicit `services.<instance>.recoveryAdminSecret` from retained values when adopting the successor; the chart rejects the obsolete input. CRD revision 3 removes that field. CRDs are cluster-wide: Helm 4.3.2's server-side-apply install can reconcile an existing definition, so inspect the resulting schema and compatibility with every remaining controller before coexistence. Do not assume a new namespace isolates the CRD. Keep operator administrator access independently; remove an old runtime Secret only after no remaining deployment consumes it.

## Troubleshooting and operations

[Resume the deployment](#resume-an-existing-deployment), then inspect the selected target and its current operation:

```bash
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get deployments,statefulsets,pods,jobs
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs deployment/keycloak-controller --tail=80
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs statefulset/keycloak-db --tail=80
kubectl --context "$KUBE_CONTEXT" -n "$NS" logs deployment/keycloak -c keycloak --tail=80
```

Check `status.operation`, current-generation conditions and Job logs (`-c recovery` or `-c backup`). `status.observedGeneration` means examined, not achieved; the condition must be true for the current `metadata.generation`. `Ready` covers intended running/access, `Stopped` excludes Keycloak processes, and `Quiescent` excludes executing or unresolved native work.

A client timeout does not cancel work. Failed transitions remain isolated across restarts; do not clear failed-state markers or roll software back to force progress. Keep diagnostics and verified inputs, then recover on independent empty storage.

For pending certificates check DNS, port 80 and cert-manager events. For maintenance denying an administrator, check the ingress client address, not a wider allowlist. For an unwritable Helm cache, set `HELM_CACHE_HOME="$INSTALL_DIR/helm-cache"`. For image acquisition failures, check the pinned digest and registry access.

## Removal

[Resume the deployment](#resume-an-existing-deployment). Establish the retention decision before removal.

### Stop application work

Set stop intent, disable ingress and suspend schedules:

```bash
yq -i '
  .operations.deployments[strenv(DEPLOYMENT)].replicas = 0 |
  .operations.deployments[strenv(DEPLOYMENT)].ingressEnabled = false |
  .operations.deployments[strenv(DEPLOYMENT)].backupSuspend = true
' "$VALUES"
helm upgrade "$RELEASE" "$CHART" --kube-context "$KUBE_CONTEXT" -n "$NS" \
  --reset-values -f "$VALUES" --set-string deployment="$DEPLOYMENT" --wait=legacy --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get keycloakapplication/keycloak -o yaml
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pods,jobs
```

Require current-generation `Stopped` and `Quiescent`, and no executing or unresolved native work in Jobs/Pods. Keep finalization machinery available until this is established.

### Uninstall the deployment

After the stop checks and removal authorization:

```bash
helm uninstall "$RELEASE" --kube-context "$KUBE_CONTEXT" -n "$NS" --wait --timeout 10m
kubectl --context "$KUBE_CONTEXT" -n "$NS" get pvc,secrets
```

Uninstall retains the database PVC and shared CRD. A new CR cannot automatically adopt the retained database; uninstall is not a reinstall procedure. Delete identified data/Secrets only under the retention decision. Namespace deletion destroys retained PVCs and Secrets. Do not delete the shared CRD as application cleanup.

### DNS and infrastructure cleanup

Remove only the hostname record owned by the removed service, and only when no replacement still uses it.

Application removal leaves the cluster and its charges intact. For authorized cluster disposal, follow [Base's removal procedure](https://github.com/bartlapp/base-k3s/blob/main/README.md#remove-the-selected-cluster). Keep shared infrastructure and external backup repositories.

## Helm inputs and connections

The complete package interface is [interface.schema.json](chart/interface.schema.json), with runtime configuration defaults in [the chart helpers](chart/templates/_helpers.tpl). Omission restores defaults/derivation, maps merge by key, lists replace, and explicit false/zero remain effective. Unknown fields under the selected service fail; unrelated services and resource metadata remain untouched.

| Input under `services.<instance>` | Default or purpose |
| --- | --- |
| `hostname`, `realm` | `login.<dns.root>`; installation ID for advertised realm/issuer, not automatic realm creation |
| `release` | Optional assertion that the service uses the selected chart version |
| `application.replicas` | `1`; `0` persistently stops Keycloak |
| `database.size` | `5Gi` on default storage class |
| `database.secretName`, `database.adminSecretName` | `keycloak-database`, `keycloak-database-admin` |
| `bootstrapAdminSecret` | `keycloak-bootstrap`; fresh initialization only |
| `ingress.enabled`, `ingress.className`, `ingress.tlsSecretName` | `true`, `traefik`, `keycloak-tls` |
| `tls.managed`, `tls.acmeServer` | `true`, Let's Encrypt production; false retains external certificate ownership |
| `ingress.maintenance`, `ingress.administratorSourceRanges` | `false`, `[]`; restriction needs a nonempty trustworthy allowlist |
| `ingress.controllerNamespace`, `ingress.controllerPodLabels` | `kube-system`, `{app.kubernetes.io/name: traefik}` for NetworkPolicy |
| `backup.repository` | Direct credential-free HTTPS Restic URL |
| `backup.enabled` | Omitted enables a supplied repository/binding; false disables even with a stale binding |
| `backup.readerSecret`, `backup.writerSecret` | `keycloak-backup-reader`, `keycloak-backup-writer` |
| `backup.schedule`, `backup.timeZone`, `backup.suspend` | `0 2 * * *`, `UTC`, `false` |

`deployment` selects placement, instance and package from the installation record and checks the Helm namespace/release and chart version. `operations.deployments.<deployment>` overrides `replicas`, `ingressEnabled`, `maintenance` and `backupSuspend`; omitted fields inherit service settings.

Independent chart consumers may omit `deployment` and select `instance` (default `keycloak`) directly, without deployment/package maps; an explicit instance must not conflict with a selected deployment. The supported Helm release remains `keycloak`, one per namespace. IDs are alphanumeric followed by alphanumerics, `.`, `_` or `-`, at most 128 characters.

Offline connection declarations use the same helpers as deployment:

```bash
helm template "$RELEASE" "$CHART" --namespace "$NS" -f "$VALUES" \
  --set-string deployment="$DEPLOYMENT" --set-string renderMode=connections \
  --show-only templates/connections.yaml
```

They expose `services.<instance>.endpoints.https` (URL, hostname, ingress class/controller namespace, enabled/managed-TLS flags), `services.<instance>.oidc.issuer`, `backup.requests.<instance>` and `credentials.requirements/bindings.<instance>.<role>`. Requirements name keys, generated/external sources and required operations; bindings name actual namespace-local Secrets. Roles are database, databaseAdmin, bootstrapAdmin, backupReader, backupWriter and tls. Database requires username/password/database; administrator roles require username/password; backup roles require password/username/restPassword and optional ca.crt; TLS uses standard TLS keys.

For composed backup, `backup.endpoint` selects a matching `resolved.backup.bindings.<instance>` with enabled/endpoint/repository/readerSecret/writerSecret. Explicit service Secret references win; contradictory destinations fail and explicit disablement wins. Refresh calculated bindings when choices change. Connection rendering ignores `resolved`, emits no deployment resources and creates no credentials, realm, client or repository. Native Keycloak administration must create/select the advertised realm and each consumer's actual client/callback configuration without resetting retained permissions.

Original code and configuration are licensed under Apache-2.0; original documentation under CC-BY-4.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE). Upstream components retain their own licenses.
