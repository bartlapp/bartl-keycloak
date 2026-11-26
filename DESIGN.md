# Keycloak package design and maintenance

The [README](README.md) and linked [recovery runbook](RECOVERY.md) own operation. The [application contract](https://github.com/bartlapp/bartl-app/blob/main/docs/application-contract.md) defines required behavior; [How to bartlize](https://github.com/bartlapp/bartl-app/blob/main/docs/how-to-bartlize.md) defines the development method. This document records Keycloak-specific design decisions and maintainer procedures.

## Package and qualification scope

Current stable release: `0.8.0`, Keycloak 26.6.2, PostgreSQL 17.6, Restic 0.19.1 and CRD revision 3. It removes the recovery-administrator input and forced session invalidation. Native migration supports Keycloak 26.6.1 → 26.6.2, not downgrades or PostgreSQL major upgrades. Governing contract/guide revision: `bartl-app` `e891fac99edb16a5349b6e86577dbdab7720f108`.

The runtime-interface decision of 2026-11-26 below is a documentation-only target for a future fresh-installation package, governed by the application contract and operating guide. It is not part of stable 0.8.0 or a claim of published implementation. The release and historical qualification descriptions here retain their original scope.

The README uses Base's shared `load_deployment` context: each deployment directory sources its cluster `.envrc` and selects one recorded deployment. The loader checks cluster and directory identity and exports placement plus the normalized `$WORK_DIR/packages/<package>/<version>` chart root without network access. Current creation procedures generate the selectors; resumption uses retained selectors rather than a context-migration procedure.

Chart acquisition remains explicit. Fresh/replacement operations resolve the latest stable release, retain a new immutable package entry and promote the extracted chart from disposable staging into its package/version cache only after a successful pull. Routine operation reuses that cache or pulls the recorded version. Target preparation does not select the target before source retirement; bounded return explicitly re-enters the retained source directory.

Stable 0.8.0 promotes the tested controller image without rebuilding it. Its exact chart passed standalone installation, recorded-version operation, reconstruction on an independent host with the source host powered off, restored MFA/OIDC and new work followed by another verified backup. In-place and replacement migration from 26.6.1 to 26.6.2, unsupported-package refusal, anonymous latest/recorded acquisition and installation from the public registry also passed. Exact artifact identities, evidence reuse and limits belong in the [0.8.0 release record](https://github.com/bartlapp/bartl-keycloak/releases/tag/v0.8.0). These checks do not establish coordinated four-service or provider-loss recovery.

Native-session development candidate `0.8.0-dev.1` verification on 2026-11-24 used one disposable Hetzner Debian 13/K3s `v1.36.5+k3s1` worker, an HTTPS Restic fixture and independent namespace PVCs. The historical source was published chart 0.5.0; native Keycloak remained 26.6.2. `go test ./...`, Go formatting checks and 26 chart contract checks passed. The A4 `source`, `restore`, `interruption`, `subsequent`, `bypass`, `fresh` and `vanilla` scenarios passed.

The restored candidate completed without bootstrap or recovery-administrator Secrets while the source controller and database were unavailable. Realm/user/signing identity and real OIDC/PKCE were preserved; still-valid captured online/offline refresh tokens remained usable without changing the realm cutoff. Browser administrator login also succeeded. Interrupted rollback, duplicate workers and lost acknowledgement retained refusal/stop boundaries; a fresh independent retry succeeded. New work survived a subsequent capture and reconstruction. Existing-state restart worked without reader, bootstrap or database-administrator Secrets. After retiring the historical controller, CRD revision 3 and both empty-state initialization branches were exercised with trusted HTTPS administrator access.

This establishes functional recovery and exclusion on distinct claims, not survival of the shared host or provider. The following identities describe the tested historical candidate; its disposable private lab workspace is not a current recovery input. Stable delivery and selected qualification evidence are retained in the release record:

| Artifact | SHA-256 |
| --- | --- |
| Controller image manifest | `9afdfc59526deeeb5b0af5b73960c4505385b49ffd7038aaf6f780a6507170ad` |
| Candidate chart archive | `285a2ec1e38512c91936cd47ced6949b311489fb86061fa48ee729dc16b3f059` |
| Implementation source archive | `15f1f323c98f67a4e791b3f71e8e12fd31d4ccc7c69606861b57b1ec298ee013` |
| Retained controller OCI archive | `5070e65b840b868f940d0bd723d390ba47679bc7263126c7f76cdec6d1548f58` |

The worker, its dedicated firewall and both primary IP allocations were deleted after qualification. The sole test hostname was removed and both authoritative DNS servers confirmed absence; the shared zone and operator identity were retained. The private fixture access files were disposable lab inputs, not a live environment.

Local verification on 2026-11-23 exercised real direnv entry, source/target switching, subdirectory inheritance, watched-record reload, parent-context clearing, prior-environment restoration and wrong-cluster/directory refusal. Native Helm/yq against a loopback OCI fixture exercised the README's creation and replacement blocks across prompt reloads, version-specific cache reuse with the registry unavailable, failed-pull invisibility, recorded-version reacquisition and source return. Lost-source preparation succeeded without a source cluster directory or kubeconfig. Template ignore checks retained selectors without exposing runtime credentials. These are local context/acquisition checks, not runtime migration, recovery or public-registry qualification. Original historical records remain privately archived; selected public qualification belongs in the [release records](https://github.com/bartlapp/bartl-keycloak/releases).

This public history starts with a reviewed technical snapshot. Original commits and raw historical records remain in private archives; source references in older receipts identify those archives, not commits reachable in this repository.

## Lifecycle and state boundary

Keycloak's durable state lives in PostgreSQL. A Deployment suffices for Keycloak because native distributed caches and `jdbc-ping` need no stable Pod ordinals; PostgreSQL uses a single-replica StatefulSet for its data directory. Neither provides database HA. Extensions with persistent files change the backup inventory.

Capture uses PostgreSQL dumps rather than realm export to preserve identities, credentials/MFA, roles, client relationships and signing material.

One namespace-local controller coordinates database provisioning, recovery Jobs, Keycloak startup and traffic admission. In the current implementation Helm renders its configuration and replica intent as well as the controller Deployment, not the lifecycle-owned workloads. Native `kc.sh start` owns initialization and migration. The controller drains predecessor processes before migration and admits traffic after verified native startup.

Normal restart disables native empty initialization: otherwise state loss could silently create a new instance. Established-target PVC metadata avoids recurring database/schema observation; a local guard checks target binding and supported native model version. Existing state needs no backup access. Only confirmed absence permits backup lookup and, if no matching point exists, initialization.

The current implementation observes replica intent at completion: a restore transaction may finish with Keycloak stopped, and PostgreSQL remains available for diagnosis. This is the existing Keycloak-only stop boundary, not the whole-application stop defined below.

### Target runtime interface — not yet implemented

Helm owns the immutable package selection and static configuration, never live run/stop or replica intent. The application CR owns durable runtime intent independently of the installation configuration. Creation defaults may initialize a new CR once; Helm upgrades, configuration reconciliation and controller restarts must preserve existing runtime intent, including an intentional stop. Helm must not render competing runtime overrides from `operations.deployments`, service settings or retained Helm values.

`spec.running` controls the complete logical application. `spec.application.replicas` controls the desired number of Keycloak replicas while the application is enabled. With `spec.running: false`, whole-application stop dominates any positive replica count without erasing that count. With `spec.running: true` and `spec.application.replicas: 0`, only Keycloak is scaled to zero: the dedicated PostgreSQL database remains available, including for capture and diagnosis. That state must not be reported as whole-application `Stopped`.

A CR scale subresource targeting `spec.application.replicas` is planned for native scaling through the application interface. Scaling the generated Deployment is not the durable interface. Neither scaling nor restarting an individual component may change `spec.running` or awaken a stopped application.

Fresh installations default to `spec.running: true` and one Keycloak replica; those defaults apply only at creation. Routine stop, start, scaling, status and component restart require no chart acquisition, connection rendering or local installation-value edits. A replacement procedure must select its initial runtime intent before application execution; a stopped target must not briefly start through an unconditional creation default.

Whole-application `Stopped` means Keycloak, its dedicated PostgreSQL database and owned background execution have stopped, with admission of new transition and backup work prevented. The controller remains available to observe intent and coordinate a later start; persistent state and diagnostic evidence are retained. Shared or external databases are outside this stop authority. A requested stop is not confirmation: status must not claim `Stopped` while owned work is still executing or its termination is unconfirmed.

Stop must safely drain or cancel already admitted initialization, migration, restore and backup work before stopping the database. Suspending a schedule alone does not stop an existing Job. A completion racing with stop must recheck current authority and cannot restart Keycloak, reopen access or re-admit background work. An interrupted or uncertain transition retains its failure boundary rather than becoming eligible for automatic replay when the application is enabled again.

Individual Keycloak and PostgreSQL restarts remain native workload operations, not a package-specific tandem restart. A Keycloak restart does not require restarting PostgreSQL; a PostgreSQL restart does not require a controller-ordered Keycloak restart. Native dependency reconnection and readiness still apply. Neither operation authorizes initialization, reconstruction or a change to durable running intent.

This target is fresh-installation only. There is no existing-installation migration, runtime-override shim or adoption procedure in this decision. A replacement target receives explicitly selected new runtime intent; it must not blindly inherit a stopped source's intent. Sharing logical identity, static configuration or a recovery point does not share live runtime authority. Source retirement and prevention of renewed production effects remain separate activation obligations.

The current API has `spec.application.replicas`, but no `spec.running`; the CRD exposes only the status subresource, not scale. Current Helm rendering still projects deployment-specific replica overrides, and the README's persistent-stop procedure leaves PostgreSQL running. Those schemas, implementation and recipes are unchanged by this decision. They remain the available interface for their matching package revision, not an implementation of this target. Existing stop, stopped-capture, restart and failure evidence must not be reused as qualification of whole-stack stop, runtime ownership separation or CR scaling.

## Recovery and exclusion

Restic retrieves a recovery point; it does not restore PostgreSQL. The recovery Job verifies one selected archive and runs `pg_restore --exit-on-error --no-owner --no-acl`, then executes the generated SQL, target binding checks and completion record through one `psql --single-transaction --set=ON_ERROR_STOP=1` connection.

Advisory lock `748920164` protects capture and startup/transition admission in the owned database. Exclusion must live on the executing SQL connection: a separate monitoring connection cannot fence a surviving restore. CronJob concurrency policy, Job names and controller leader Leases cannot exclude manual/duplicate workers or already executing SQL.

The capture manifest's repository ID identifies its origin, not necessarily the destination repository after `restic copy`. Recovery binds the destination repository and selected destination snapshot while checking original capture identity and integrity. Requiring both repository IDs to match would break secondary recovery. Historical secondary-recovery results remain in private artifact custody; they are not a prerequisite for operating the package.

Recovery retains native Keycloak session semantics. The package neither invokes realm logout nor advances revocation cutoffs; completing reconstruction does not depend on privileged realm administration or an administration HTTP request. Authentication and authorization state is restored to the selected recovery point, including its session and credential state. Later security changes may be lost. Security-incident reconciliation belongs to the authorized operating procedure before activation, not a partial automatic session-reset guarantee. Operator administrator access remains necessary for acceptance and ongoing administration, not as a controller startup credential.

## Capture and publication

Online `pg_dump` needs PostgreSQL, not running Keycloak. The backup worker validates the dump, publishes `/keycloak.dump` and `/manifest.json`, then reads back the complete snapshot before issuing its JSON receipt. Streaming avoids loading a database dump into memory. Lost database exclusion or termination cancels native children.

The controller-owned CronJob admits scheduled/manual capture. Already admitted Jobs retain their configuration when scheduling is suspended or the CronJob is removed. Backup failure must not stop a healthy application, override stop/routing intent or introduce repository access into normal restart.

For the target runtime interface, backup scheduling and manual admission additionally obey whole-application running authority. Keycloak-only zero replicas still permits a database capture; whole-application stop does not wake PostgreSQL to take one. Already admitted work must reach a safe completion or confirmed cancellation before `Stopped` can be asserted. Failed capture or uncertain state cannot authorize restart, and publication failure alone must neither stop an otherwise healthy enabled application nor override an intentional stop.

## Failure and re-entry

One-shot execution claims prevent replay after failed initialization, migration or recovery. A lost post-commit acknowledgement is not proof of completion: failed/missing recovery Jobs persist startup failure on the PVC even if SQL committed. Controller restart may observe an unambiguous result, but does not reconstruct uncertain success.

New running intent cannot clear that failure. Keep the target isolated with diagnostics and recover on independent empty storage. Same-target repair and Helm rollback cannot safely infer or reverse native data changes. In the target interface, transition failure is not an instruction to rewrite `spec.running` or the desired replica count: failure continues to block unsafe startup, and whole-application `Stopped` is reported only after its owned-component and work-drain requirements are met.

## Ownership and isolation

The [CRD](chart/crds/keycloakapplications.yaml) records the available API schema; the [input reference](README.md#helm-inputs-and-connections) owns current configuration and Secret bindings. Helm currently renders the CR, controller/RBAC and optional certificate request. The application controller owns database/application workloads, Jobs, schedules and routing. cert-manager owns certificate issuance and renewal; native workers have no Kubernetes mutation authority.

Current deployment-specific overrides are projected on copies of service settings. That is the existing rendering model, not the target ownership model: the target retains deployment selection for placement and static configuration while runtime intent lives only on each target's application CR. Connection rendering uses the same helpers, not previously calculated `resolved` output. Neither rendering nor a declared issuer creates native realms or clients.

Secrets are read by explicit name, not through a namespace-wide informer. Backup workers receive repository-role and database credentials, never destructive repository authority. Reader access includes native lock writes but no backup-content mutation. Restic locks coordinate cooperating clients, not malicious deletion.

Owner UID checks prevent adoption of foreign resources; labels are not authorization. Database PVCs survive uninstall, but a new CR UID cannot automatically adopt their binding. Ingress maintenance requires preserved client addresses and no backend bypass. Ingress restriction does not suppress restored SMTP, federation or custom-listener effects; these need separate outbound isolation.

## Shared CRD and package compatibility

The namespace-local controllers share one cluster-scoped CRD. [Helm installs missing CRDs but does not upgrade or delete existing ones](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/). The controller does not manage the CRD.

Each release must declare compatible CRD/API revisions, operation records and native transitions. A shared-schema change needs a separate administrator operation covering every installed controller; unchanged `v1alpha1` naming is not compatibility. Prefer additive evolution; incompatible changes require an explicit conversion/stored-object migration design.

Leader election does not fence existing SQL. A replacement controller must understand inherited operations or refuse before transition work. CRD deletion removes CRs across namespaces and loses intent/evidence even when PVCs survive; it is not application cleanup.

## Maintainer procedures

Run from a source checkout. Live builds and application/database workloads use an authorized Hetzner worker and disposable synthetic data, not local Docker or Colima. Qualify the [README path](README.md), including its linked [recovery runbook](RECOVERY.md), with company-specific tooling unavailable and an operator-supplied backup repository; the harnesses below supplement that exercise.

### Offline and database checks

With Node.js, Helm and Mike Farah yq v4 on `PATH`:

```bash
node tests/chart.mjs
```

`HELM` and `YQ` select executable paths; `KC_CHART` selects an exact packaged archive instead of the source chart. This checks rendering, selection, validation, backup precedence and declarations, not live lifecycle behavior.

For PostgreSQL integration, supply `KC_TEST_POSTGRES_DSN` as a disposable PostgreSQL 17 administrator connection, then run on the authorized worker:

```bash
go test ./... -count=1 -v
```

The regression creates/removes its own database and role; without the variable it is skipped. Exercise the built package afterwards.

### Recovery harness

`tests/kc-a4.mjs` uses explicit `kc-a4-*` namespaces and verifies the API address. Select its protected fixture directory with `KC_A4_STATE` (default `.lab/kc-a4`):

| File | Required fixture inputs |
| --- | --- |
| `runtime.json` | `kubeconfig`, `server`, historical `sourceChart`, candidate `chart`, fixture `backupImage`, and explicit `candidateRelease: "0.8.0"`. `chart` must name the exact retained local candidate archive; do not inherit the historical version default. |
| `credentials.json` | `admin`, `user`, `restic` encryption password, and REST authentication passwords `reader` and `writer`. |
| `repository.crt` | Trusted repository CA certificate. |
| `source-values.json` | Retained pre-0.6 source values, including its HTTPS `configuration.hostname`; historical source commands keep that release's interface. |

Prepare ready source `kc-a4-source` with trusted HTTPS and TLS Secret `kc-a4-tls`. Initialize the separate repository at `rest:https://backup-store.kc-a4-repository.svc/keycloak/`, with an append-only writer and a data-read-only, lock-writable reader, including exact lock-object permissions on refusal paths. Import candidate images before running. Retain the exact private fixture setup and diagnostics with the qualification artifacts, outside Git.

For a controller OCI archive already transferred to a qualification node, set `CONTROLLER_ARCHIVE` to its verified absolute path and use a fully qualified import base:

```bash
k3s ctr images import --base-name ghcr.io/bartlapp/bartl-keycloak-controller \
  --digests --skip-digest-for-named "$CONTROLLER_ARCHIVE"
```

Use the corresponding repository base for other operating images. Verify actual container admission before starting stateful qualification; an image listing alone is insufficient. Unqualified auto-generated import names can leave an unusable CRI reference. This is local fixture preparation, not a reason to alter the qualified manifest or bypass registry/TLS checks.

Helm 4.3.2 passes its default server-side-apply setting to [CRD installation](https://github.com/helm/helm/blob/v4.3.2/pkg/action/install.go). The live migration fixture observed revision 2 become revision 3 when the replacement release was installed. Preserve schema-admission refusal as a valid unsupported-package boundary, together with explicit checks that data and active routing remain unchanged.

Invoke scenarios individually against their prepared fixture state; this list is not a replay/repair script:

```bash
node tests/kc-a4.mjs source
node tests/kc-a4.mjs ordinary
node tests/kc-a4.mjs conflict
node tests/kc-a4.mjs fresh
node tests/kc-a4.mjs vanilla
node tests/kc-a4.mjs restore
node tests/kc-a4.mjs fallback
node tests/kc-a4.mjs initializer
node tests/kc-a4.mjs interruption
node tests/kc-a4.mjs refusal
node tests/kc-a4.mjs subsequent
node tests/kc-a4.mjs bypass
node tests/kc-a4.mjs stale
node tests/kc-a4.mjs unsupported
```

`ordinary` covers replicas, restart, persistent stop, stopped capture, unreadable database credentials and invalid replica intent, and advances the selected snapshot. `conflict` requires scheduled/manual refusal under a native lock; `vanilla` checks trusted-HTTPS administrator login in both fresh-state branches. `interruption` covers blocked catalog mutation, interrupted/duplicate restore, lost post-commit acknowledgement and controller restart under stop intent, requiring refusal and independent-target retry. `stale` covers healthy unchanged reconciliation and foreign-binding refusal; `unsupported` requires refusal without replay or data loss. Include controller-to-PostgreSQL outage checks of readiness and access when qualifying observation loss.

For source-independent restoration, stop the source controller, wait for its Pod to disappear, then stop its database, retaining PVC and inputs. `fallback` may reactivate the original only for consequence-free synthetic tests.

### Replacement and upgrade harness

`tests/kc-a5.mjs` uses `kc-a5-*` namespaces on the authorized worker. `KC_A5_STATE` (default `.lab/kc-a5`) holds `runtime.json`, `credentials.json` and `repository.crt`. Credentials have the same keys as the recovery harness. Runtime inputs are `kubeconfig`, `apiServer`, `baselineChart`, `candidateChart`, `candidateController`, `candidateKeycloak`, `candidateBackup`, `hostname` and allowed worker-host `adminIPv4`. Set `candidateRelease` explicitly to `"0.8.0"` and `candidateChart` to the exact retained local candidate archive, not a historical default.

The baseline is committed 0.3.0 source rebuilt on Hetzner. Prepare its trusted HTTPS TLS Secret `kc-a5-tls` and initialized repository at `rest:https://backup-store.kc-a5-repository.svc/keycloak/` with the reader/writer permissions above. The unrelated `kc-a5-probe` Pod supplies denied-source/backend-isolation checks, separate from the allowed worker host. Optional `sshConfig` selects the retained SSH configuration for external checks through host `k3s`; optional `base` overrides the public URL.

```bash
node tests/kc-a5.mjs source
node tests/kc-a5.mjs replacement
node tests/kc-a5.mjs bypass
node tests/kc-a5.mjs fallback
node tests/kc-a5.mjs upgrade
node tests/kc-a5.mjs interruption
node tests/kc-a5.mjs replacement-upgrade
node tests/kc-a5.mjs downgrade
node tests/kc-a5.mjs open
```

`interruption` holds a native table lock, restarts the controller during migration and applies persistent stop; incomplete migration must not restart blindly, and the refused target remains available for diagnosis. `downgrade` presents the old package to migrated disposable state and requires refusal without data changes. For a fresh upgrade exercise from an independently recovered baseline, prepare its TLS Secret, select its namespace through optional runtime `source`, then run:

```bash
node tests/kc-a5.mjs restore-source
```

Record exact artifacts and exercised failure boundaries in the release evidence. Acceptance includes real login/use, new work and a subsequent usable backup; namespace separation alone does not prove host/provider-loss recovery.

During maintainer qualification of administrator-restricted recovery access, also probe forged forwarding headers from a denied address and direct-service, NodePort and alternate-ingress bypasses. They must not admit login or administration. These adversarial probes qualify the ingress enforcement; ordinary recovery still requires observed allowed-administrator access and denied ordinary access, preserved identities, useful behavior, new work and another usable backup through the runbook.

### Build, qualification and publication

1. Select the next unused stable package version; Helm's default latest selection excludes release candidates. Prefer promotion of retained artifacts: verify checksums and manifest digests; do not rebuild or repackage merely to publish. For new implementation, build the controller from `Dockerfile` and/or worker from `Backup.Dockerfile` for `linux/amd64` on an authorized Hetzner worker and update [manifest pins](chart/templates/_helpers.tpl). Never overwrite a released chart or image tag.
2. Package `chart/crds/keycloakapplications.yaml`, bundled `examples/auth-playground` under `files/`, `LICENSE` and `NOTICE`. Retain committed source revision, source archive and chart checksum. Qualify these exact immutable artifacts through the README and its linked recovery runbook: first installation, routine operation on its recorded version, replacement restoring and migrating the latest matching backup, and refusal of unsupported transitions. Verify source/target CRD compatibility, preserved identities/data, new work and another usable backup. Source tests or registry transfer alone are not qualification.
3. Publication requires explicit authorization and a registry credential with `write:packages`. Verify GitHub identity, organization/package authority, public visibility and absence of the new version tags. Use isolated temporary registry configuration, never print the token, and remove the configuration afterwards.
4. Publish the qualified image manifests without format conversion and the qualified chart to `oci://ghcr.io/bartlapp`. Record the chart manifest digest and require unchanged image digests. With empty registry credentials and no source checkout, follow the README's unversioned acquisition and confirm that it selects the intended stable release; also retrieve a recorded version explicitly. Download the images and verify their digests. Keep released chart versions and referenced images available for routine operation.
5. Verify installation through public acquisition on an authorized Hetzner target. Keep raw/private output outside Git; record exact artifact identities, source revisions, observed qualification results and material limits in the release/integration description, separately from registry-transfer checks. State outstanding qualification explicitly; subsequent package defects require a new release.

See [GitHub registry instructions](https://docs.github.com/en/packages/working-with-a-github-packages-registry/working-with-the-container-registry).

Native references: [Keycloak database migration](https://www.keycloak.org/docs/latest/upgrading/index.html#_upgrade_db), [cache persistence and discovery](https://www.keycloak.org/server/caching), [CRD evolution](https://kubernetes.io/docs/tasks/extend-kubernetes/custom-resources/custom-resource-definition-versioning/).
