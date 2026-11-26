# Keycloak package design and maintenance

The [README](README.md) and linked [recovery runbook](RECOVERY.md) own operation. The [application contract](https://github.com/bartlapp/bartl-app/blob/main/docs/application-contract.md) defines required behavior; [How to bartlize](https://github.com/bartlapp/bartl-app/blob/main/docs/how-to-bartlize.md) defines the development method. This document records Keycloak-specific design decisions and maintainer procedures.

## Package and qualification scope

Current stable release remains `0.8.0`, Keycloak 26.6.2, PostgreSQL 17.6, Restic 0.19.1 and CRD revision 3. Its API and in-place evidence are historical, not candidate qualification.

This checkout implements the **unqualified `0.9.0-dev.2` v1alpha2 candidate**, governed by `bartl-app` `d1cd9702a50fe6b43afae7dfb63632fe2731ab28` and GmbH ADR-10 `84537f9`. It is not published or fully live-qualified. Supported native data transitions remain 26.6.1 → 26.6.2 and same-version 26.6.2 with PostgreSQL 17, not downgrades or PostgreSQL major upgrades. This pair needs no intermediate native hop. Exact candidate artifacts, all contract cases and standalone procedure execution require qualification; no historical check is silently reused.

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

One namespace-local controller coordinates provisioning, recovery Jobs, startup and access. Helm renders static configuration and controller infrastructure, never runtime running/replica intent. Native `kc.sh start` owns initialization and migration. The controller drains predecessor processes before migration and admits traffic after verified native startup.

Normal restart disables native empty initialization: otherwise state loss could silently create a new instance. Established-target PVC metadata avoids recurring database/schema observation; a local guard checks target binding and supported native model version. Existing state needs no backup access. Only confirmed absence permits backup lookup and, if no matching point exists, initialization.

Unavailable Kubernetes observations or reconciliation (authorization, transport, timeout or server availability errors) block dependent management work and report degradation without withdrawing already admitted execution. A reproduced Secret-read authorization failure exposed this boundary in candidate `0.9.0-dev.1`: the controller incorrectly scaled a healthy Deployment to zero. Confirmed invalid configuration and native-state failures retain their separate refusal behavior. Stop still wins before credential observation; unavailable stop observations never establish termination.


### Runtime interface

The API is `keycloak.bartl.sh/v1alpha2`, retaining group and Kind `KeycloakApplication`. Its changed whole-application `Stopped` contract is explicit; there is no v1alpha1 conversion/adoption. Helm owns package selection and static configuration, never run/stop or replica intent. The CR owns that intent durably. API defaults initialize a new CR once; static configuration and controller restarts must preserve it. The README precreates the CR with chosen intent through native `kubectl create` before installing the controller; static Helm ownership metadata allows subsequent installation without either runtime field in the Helm manifest.

`spec.running` controls the complete logical application. `spec.application.replicas` controls the desired number of Keycloak replicas while the application is enabled. With `spec.running: false`, whole-application stop dominates any positive replica count without erasing that count. With `spec.running: true` and `spec.application.replicas: 0`, only Keycloak is scaled to zero: the dedicated PostgreSQL database remains available, including for capture and diagnosis. That state must not be reported as whole-application `Stopped`.

The CR scale subresource targets `spec.application.replicas` for `kubectl scale keycloakapplication/keycloak`. Scaling the generated Deployment is not durable intent. Scaling or restarting a component must not change `spec.running` or awaken a stopped application.

Fresh installations default to `spec.running: true` and one Keycloak replica; those defaults apply only at creation. Routine stop, start, scaling, status and component restart require no chart acquisition, connection rendering or local installation-value edits. A replacement procedure must select its initial runtime intent before application execution; a stopped target must not briefly start through an unconditional creation default.

Whole-application `Stopped` means Keycloak, its dedicated PostgreSQL database and owned background execution have stopped, with ordinary access withdrawn and admission of new transition and backup work prevented. The controller remains available to observe intent and coordinate a later start; persistent state and diagnostic evidence are retained. Shared or external databases are outside this stop authority. A requested stop is not confirmation: status must not claim `Stopped` while owned work is still executing or its termination is unconfirmed.

Stop must safely drain or cancel already admitted initialization, migration, restore and backup work before stopping the database. Suspending a schedule alone does not stop an existing Job. A completion racing with stop must recheck current authority and cannot restart Keycloak, reopen access or re-admit background work. An interrupted or uncertain transition retains its failure boundary rather than becoming eligible for automatic replay when the application is enabled again.

Individual Keycloak and PostgreSQL restarts remain native workload operations, not a package-specific tandem restart. A Keycloak restart does not require restarting PostgreSQL; a PostgreSQL restart does not require a controller-ordered Keycloak restart. Native dependency reconnection and readiness still apply. Neither operation authorizes initialization, reconstruction or a change to durable running intent.

This target is installed on fresh target state, including replacement from a supported backup. There is no migration or adoption of the predecessor's CRs, generated Kubernetes resources, controller operation records or runtime overrides, and no compatibility shim. A replacement target receives explicitly selected new runtime intent; it must not blindly inherit a stopped source's intent. Sharing logical identity, static configuration or a recovery point does not share live runtime authority. Supported backup/data migration is required and is distinct from Kubernetes-state adoption.


### Replacement and update path

Replacement on independent target storage is the required package-update path. It uses source-independent reconstruction and native migration; there is no in-place package update, coordinator-state adoption or compatibility shim. The operator selects the destination package, not migration hops. For declared 26.6.1 → 26.6.2 and same-version transitions, native startup performs the work without intermediate artifacts. A changed package/image selection on existing bound storage is refused. Failed/uncertain work stays isolated; unsupported backup/data versions and downgrades are refused without initialization fallback.

Blue/green denotes sequential replacement placements, not concurrent active writers or automatic failover. Source and target may use separate namespaces such as `keycloak-blue` and `keycloak-green`, each with `KeycloakApplication/keycloak`, independent volumes and a namespace-local controller. They retain the same logical application identity and production hostname. Coexistence in one cluster requires a shared CRD compatible with every needed package/controller; namespace separation does not isolate CRD semantics. An incompatible API transition may require a new target cluster rather than conversion of old Kubernetes state.

For planned replacement, the operator withdraws source write authority, confirms drain, produces and verifies the final backup while PostgreSQL is still available, installs the target, checks useful application behavior, activates production access and finally retires remaining source resources. Retire the source earlier wherever necessary to prevent concurrent writers or external effects; a final backup cannot be taken after a full-stack stop has shut down its database. The target package neither discovers nor contacts nor retires its predecessor. It must reconstruct with independently retained artifacts, target configuration, protected inputs and backup access when the source is unavailable.

Internal restore completion and native readiness are not the operator's production-routing decision. Successful reconstruction needs no second technical restore-release command; production activation remains a separate operator-owned transfer of authority. That ownership does not relax enforcement: the procedure must cover direct and stale access, existing connections, background writers and external effects, prevent a returning predecessor from reclaiming authority, and establish the active generation after interrupted activation or lost acknowledgement. Do not automatically return to a source after the target may have accepted writes or produced effects. Unreachable source state is not proof of retirement; if the declared failure model's authority transfer cannot be enforced, activation stops.

Qualification must exercise the public README and linked procedures using exact immutable artifacts, with company repositories/tooling unavailable and an operator-supplied compatible backup service plus independently usable keys and credentials. Cover both empty-state initialization branches, existing-state restart without backup access, source-independent restore, supported backup/data transitions including required native hops, and the target runtime interface. Failed observations, lookup/integrity errors, unsupported versions, foreign state, conflicting work, interrupted transitions and stale completion must retain their refusal, isolation and stop boundaries across restarts; an independent-target retry must succeed within the accepted data-loss boundary. Check useful behavior, preserved identity/data, new work and a subsequent usable backup, not only Pod/Job readiness. Keep source-independent application recovery, shared-host tests and actual provider-loss/authority-transfer evidence distinct. No historical result qualifies these new obligations by declaration.

## Recovery and exclusion

Restic retrieves a recovery point; it does not restore PostgreSQL. The recovery Job verifies one selected archive and runs `pg_restore --exit-on-error --no-owner --no-acl`, then executes the generated SQL, target binding checks and completion record through one `psql --single-transaction --set=ON_ERROR_STOP=1` connection.

Advisory lock `748920164` protects capture and startup/transition admission in the owned database. Exclusion must live on the executing SQL connection: a separate monitoring connection cannot fence a surviving restore. CronJob concurrency policy, Job names and controller leader Leases cannot exclude manual/duplicate workers or already executing SQL.

The capture manifest's repository ID identifies its origin, not necessarily the destination repository after `restic copy`. Recovery binds the destination repository and selected destination snapshot while checking original capture identity and integrity. Requiring both repository IDs to match would break secondary recovery. Historical secondary-recovery results remain in private artifact custody; they are not a prerequisite for operating the package.

Recovery retains native Keycloak session semantics. The package neither invokes realm logout nor advances revocation cutoffs; completing reconstruction does not depend on privileged realm administration or an administration HTTP request. Authentication and authorization state is restored to the selected recovery point, including its session and credential state. Later security changes may be lost. Security-incident reconciliation belongs to the authorized operating procedure before activation, not a partial automatic session-reset guarantee. Operator administrator access remains necessary for acceptance and ongoing administration, not as a controller startup credential.

## Capture and publication

Online `pg_dump` needs PostgreSQL, not running Keycloak. The backup worker validates the dump, publishes `/keycloak.dump` and `/manifest.json`, then reads back the complete snapshot before issuing its JSON receipt. Streaming avoids loading a database dump into memory. Lost database exclusion or termination cancels native children.

A dedicated `keycloak-capture` PVC, sized from database storage rather than another operator choice, retains complete captures across publication failures. The next admitted backup publishes retained complete work before capturing current database state, so a final receipt never represents only an earlier failed publication. Include temporary capture capacity in planning. Both database and capture PVCs survive stop/uninstall until an explicit retention decision. These changed worker semantics require new exact-image qualification.

Native file locking excludes concurrent capture-volume mutation, including a canceled worker whose child has not exited yet. Completed capture becomes eligible by atomic rename to `pending`; verified publication retires that name atomically before file deletion. A later worker reclaims only abandoned temporary capture and retired directories under the same lock, never the pending point.

The controller-owned CronJob admits scheduled/manual capture. Already admitted Jobs retain their configuration when scheduling is suspended or the CronJob is removed. Backup failure must not stop a healthy application, override stop/routing intent or introduce repository access into normal restart.

Backup admission obeys running authority. Keycloak-only zero replicas permits capture; whole-application stop removes the CronJob without deleting admitted Jobs, drains work and never wakes PostgreSQL for backup. `Stopped` requires confirmed component/work termination. Failed or uncertain capture cannot authorize restart; publication failure alone neither stops a healthy enabled application nor overrides stop.

## Failure and re-entry

One-shot execution claims prevent replay after failed initialization, migration or recovery. A lost post-commit acknowledgement is not proof of completion: failed/missing recovery Jobs persist startup failure on the PVC even if SQL committed. Controller restart may observe an unambiguous result, but does not reconstruct uncertain success.

New running intent cannot clear that failure. Keep the target isolated with diagnostics and recover on independent empty storage. Same-target repair and Helm rollback cannot safely infer or reverse native data changes. In the target interface, transition failure is not an instruction to rewrite `spec.running` or the desired replica count: failure continues to block unsafe startup, and whole-application `Stopped` is reported only after its owned-component and work-drain requirements are met.

## Ownership and isolation

The [CRD](chart/crds/keycloakapplications.yaml) records the API schema; the [input reference](README.md#helm-inputs-and-connections) owns static configuration and Secret bindings. Helm renders the static CR, controller/RBAC and optional certificate request. The controller owns database/application workloads, Jobs, schedules and routing. cert-manager owns certificate issuance/renewal; native workers have no Kubernetes mutation authority.

Deployment selection resolves placement/package/static settings only. Runtime fields in service settings and operational overrides are rejected. Connection rendering uses static helpers, not previously calculated `resolved` output. Neither rendering nor a declared issuer creates realms or clients.

Secrets are read by explicit name, not through a namespace-wide informer. Backup workers receive repository-role and database credentials, never destructive repository authority. Reader access includes native lock writes but no backup-content mutation. Restic locks coordinate cooperating clients, not malicious deletion.

Owner UID checks prevent foreign-resource adoption; labels are not authorization. Database/capture PVCs survive uninstall, but a new CR cannot adopt their binding. Restored database metadata receives a fresh target binding; source coordinator state is never adopted. Ingress maintenance requires preserved client addresses and no backend bypass. NetworkPolicy restricts Keycloak egress to its database, peers and DNS; SMTP/federation/custom remote effects are outside this stock isolated profile. The runbook checks additive policies and actual denial rather than claiming ingress restriction suppresses effects.

## Shared CRD and package compatibility

The namespace-local controllers share one cluster-scoped CRD. [Helm installs missing CRDs but does not upgrade or delete existing ones](https://helm.sh/docs/chart_best_practices/custom_resource_definitions/). The controller does not manage the CRD.

Each release declares compatible CRD/API revisions and native transitions. A pre-v1alpha2 source requires a new target cluster: no old served API, conversion, stored-object migration or adoption. Already-v1alpha2 coexistence still requires review of every installed controller against the shared definition; namespaces/colors do not isolate CRD semantics.

Leader election does not fence existing SQL. Restarting a controller within an installation must understand that installation's operation records or refuse before transition work; this does not require a replacement target to inherit predecessor controller state. CRD deletion removes CRs across namespaces and loses intent/evidence even when PVCs survive; it is not application cleanup.

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
| `runtime.json` | `kubeconfig`, `server`, exact candidate `chart`, fixture `backupImage`, and explicit `candidateRelease: "0.9.0-dev.2"`. Optional `source` selects a retained candidate source namespace; `repository` selects the supplied HTTPS fixture. Never inherit a historical release default. |
| `credentials.json` | `admin`, `user`, `restic` encryption password, and REST authentication passwords `reader` and `writer`. |
| `repository.crt` | Trusted repository CA certificate. |
| `source-values.json` | Candidate fixture's HTTPS `configuration.hostname`, database settings, bootstrap Secret name and ingress settings; not an old Helm runtime interface. |

Prepare ready **v1alpha2 candidate** source `kc-a4-source` with the same exact chart and trusted TLS Secret `kc-a4-tls`; retain its normalized static values as `<state>/kc-a4-source/installation.yaml`. Install the candidate CRD once before scenarios. Initialize the repository at `rest:https://backup-store.kc-a4-repository.svc/keycloak/` with append-only writer and data-read-only/lock-writable reader, including exact lock-object permissions on refusal paths. Import images before running; retain private fixture setup/diagnostics outside Git. Old-API migration belongs in A5 on a separate source cluster.

For a controller OCI archive already transferred to a qualification node, set `CONTROLLER_ARCHIVE` to its verified absolute path and use a fully qualified import base:

```bash
k3s ctr images import --base-name ghcr.io/bartlapp/bartl-keycloak-controller \
  --digests --skip-digest-for-named "$CONTROLLER_ARCHIVE"
```

Use the corresponding repository base for other operating images. Verify actual container admission before starting stateful qualification; an image listing alone is insufficient. Unqualified auto-generated import names can leave an unusable CRI reference. This is local fixture preparation, not a reason to alter the qualified manifest or bypass registry/TLS checks.

Historical Helm 4.3.2 qualification observed cluster-wide CRD replacement. That is not permission to overwrite an incompatible predecessor schema: current old-API migration uses a separate target cluster and retains schema refusal plus unchanged active data/routing checks.

Invoke scenarios individually against their prepared fixture state; this list is not a replay/repair script:

```bash
node tests/kc-a4.mjs source
node tests/kc-a4.mjs ordinary
node tests/kc-a4.mjs conflict
node tests/kc-a4.mjs fresh
node tests/kc-a4.mjs initial-stopped
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

`ordinary` covers native scale, independent component restarts, whole-application stop including PostgreSQL, runtime ownership across static apply/controller restart, stopped-component restart non-awakening, Keycloak-zero capture, unreadable credentials and invalid replicas. `conflict` requires scheduled/manual native-lock refusal; `vanilla` checks trusted-HTTPS administration in both fresh branches. `interruption` retains catalog, duplicate-worker, interrupted restore, lost acknowledgement and independent retry checks. `stale` covers unchanged reconciliation/foreign binding; `unsupported` requires refusal without replay/data loss. Exact-artifact qualification also requires observation loss, retained capture/publication failure, stop during admitted backup/restore/migration, stale completion, fresh stopped creation and occupied-slot cases; these harnesses alone do not qualify every obligation.

Source-independent restore leaves the predecessor whole-stopped/controller-unavailable. `fallback` may reactivate it only for explicitly consequence-free synthetic tests; normal restore has no automatic source restart.

### Replacement migration harness

`tests/kc-a5.mjs` uses `kc-a5-*` namespaces. `KC_A5_STATE` holds `runtime.json`, `credentials.json` and `repository.crt` (default `.lab/kc-a5`). Credentials match A4. Runtime supplies target `kubeconfig`/`apiServer`, `baselineChart`, `candidateChart`, `candidateController`, `candidateKeycloak`, `candidateBackup`, `hostname`, allowed `adminIPv4` and explicit `candidateRelease: "0.9.0-dev.2"`. Source preparation additionally requires independent old-API `sourceKubeconfig`/`sourceAPIServer`; target reconstruction uses retained source inputs and never contacts that API. Install the target CRD before the harness. `repository` can select an external fixture reachable independently from both clusters.

Select `baselineRelease`/`sourceVersion` explicitly. Public released `0.8.0` with native `26.6.2` supplies old-API source → v1alpha2 same-version reconstruction. Historical `0.3.0`/26.6.1 source was located at archived commit `c2d6add24cfe016f286e0a7087d0faa6fe4cd495`; its old chart and operating images are absent from the current public registry. Rebuilding that exact source is a qualification fixture, not a reissued release or an operator dependency. Record the rebuilt image identities and retain a genuine recovery point for later transition qualification; never relabel 26.6.2 data or synthesize version metadata. Prepare source `kc-a5-tls` and the supplied HTTPS repository. Source preparation retains TLS/PVC comparison inputs and observed retirement before shutdown. Target `kc-a5-probe` supplies denied/backend checks. Hostname routing and source-host exclusion remain explicit operator actions, not assumptions inferred from a retained retirement receipt. Optional `sshConfig` selects external checks through host `k3s`; optional `base` overrides the URL.

```bash
node tests/kc-a5.mjs source
# For an actual 26.6.1 source only:
node tests/kc-a5.mjs interruption
# Switch approved fixture routing to target before HTTPS acceptance:
node tests/kc-a5.mjs replacement-upgrade
node tests/kc-a5.mjs bypass
node tests/kc-a5.mjs downgrade
node tests/kc-a5.mjs open
# Optional consequence-free fallback uses its own declared fixture state.
```

`source` captures 26.6.1 with zero Keycloak/running PostgreSQL, then retires controller/database. `interruption` reconstructs independent candidate storage, blocks migration and checks controller restart, stop and persistent refusal. `replacement-upgrade` provides independent successful retry, preserving IDs, native sessions, configuration and exact images. `downgrade` refuses an old incompatible package on another empty target, checking active data/routing unchanged; it is not an in-place native downgrade test. `open` checks ordinary access, new work, another backup and reconstruction of that work. No scenario performs an in-place Helm package upgrade.

Run scenarios individually against their declared state; run interruption before independent successful replacement. The source/target access-routing switch uses the fixture's approved native infrastructure/DNS procedure. Never infer that installing the target changes public routing.

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
