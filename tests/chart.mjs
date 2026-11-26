#!/usr/bin/env node
// Offline chart behavior: no cluster, registry, credentials or company tooling.
import assert from 'node:assert/strict';
import {spawnSync} from 'node:child_process';
import {cpSync, mkdtempSync, rmSync, statSync, writeFileSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {fileURLToPath} from 'node:url';

const chart = process.env.KC_CHART || fileURLToPath(new URL('../chart', import.meta.url));
const playgroundChart = process.env.KC_PLAYGROUND_CHART || fileURLToPath(new URL('../examples/auth-playground', import.meta.url));
const helm = process.env.HELM || 'helm';
const yq = process.env.YQ || 'yq';
const metadata = spawnSync(helm, ['show', 'chart', chart], {encoding: 'utf8'});
assert.equal(metadata.status, 0, metadata.stderr);
const chartVersion = metadata.stdout.match(/^version:\s*(\S+)/m)[1];
function render(values, {connections = false, release = 'keycloak', namespace = 'default', chartPath = chart, showOnly} = {}) {
  return spawnSync(helm, ['template', release, chartPath, '--namespace', namespace, '-f', '-', ...(connections ? ['--set-string', 'renderMode=connections'] : []), ...(showOnly ? ['--show-only', showOnly] : [])], {
    input: JSON.stringify(values), encoding: 'utf8', maxBuffer: 8 * 1024 * 1024,
  });
}
function documents(result) {
  assert.equal(result.status, 0, result.stderr);
  const parsed = spawnSync(yq, ['-o=json', '-I=0', '.'], {input: result.stdout, encoding: 'utf8'});
  assert.equal(parsed.status, 0, parsed.stderr);
  return parsed.stdout.trim().split('\n').filter(Boolean).map(JSON.parse);
}
function application(values, options) {
  const docs = documents(render(values, options));
  const app = docs.find(d => d?.kind === 'KeycloakApplication');
  assert.ok(app, 'application must be rendered');
  return {spec: app.spec, docs};
}
function rejects(values, message, options) {
  const result = render(values, options);
  assert.notEqual(result.status, 0, 'invalid input rendered successfully');
  assert.match(result.stderr, message);
}
const base = () => ({installation: 'acme', dns: {root: 'acme.example'}, services: {keycloak: {}}});
let checks = 0;
function check(name, fn) { fn(); checks++; console.log('PASS ' + name); }

check('standalone input derives hostname and stable identity without backup', () => {
  const {spec} = application(base());
  assert.deepEqual(spec.identity, {installation: 'acme', instance: 'keycloak'});
  assert.equal(spec.configuration.hostname, 'https://login.acme.example');
  assert.equal(spec.backup, undefined);
});
check('v1alpha2 defaults runtime intent at creation and exposes native scale', () => {
  const crds = documents(spawnSync(helm, ['show', 'crds', chart], {encoding: 'utf8'}));
  const crd = crds.find(doc => doc?.kind === 'CustomResourceDefinition' && doc.spec.names.kind === 'KeycloakApplication');
  assert.ok(crd);
  assert.deepEqual(crd.spec.versions.map(version => [version.name, version.served, version.storage]), [['v1alpha2', true, true]]);
  const version = crd.spec.versions[0];
  const schema = version.schema.openAPIV3Schema.properties;
  assert.equal(schema.spec.properties.running.default, true);
  assert.deepEqual(schema.spec.properties.application.default, {});
  assert.equal(schema.spec.properties.application.properties.replicas.default, 1);
  assert.equal(schema.spec.properties.application.properties.replicas.minimum, 0);
  assert.equal(schema.status.properties.replicas.format, 'int32');
  assert.equal(schema.status.properties.selector.type, 'string');
  assert.deepEqual(version.subresources.scale, {
    specReplicasPath: '.spec.application.replicas',
    statusReplicasPath: '.status.replicas',
    labelSelectorPath: '.status.selector',
  });
});
check('static manifests support explicit creation before controller execution without owning runtime fields', () => {
  const values = base();
  const {spec, docs} = application(values, {namespace: 'green', showOnly: 'templates/application.yaml'});
  assert.equal(docs.length, 1);
  const app = docs[0];
  assert.equal(app.apiVersion, 'keycloak.bartl.sh/v1alpha2');
  assert.equal(app.metadata.labels['app.kubernetes.io/managed-by'], 'Helm');
  assert.equal(app.metadata.annotations['meta.helm.sh/release-name'], 'keycloak');
  assert.equal(app.metadata.annotations['meta.helm.sh/release-namespace'], 'green');
  assert.equal(Object.hasOwn(spec, 'running'), false);
  assert.equal(Object.hasOwn(spec.application ?? {}, 'replicas'), false);
  values.services.keycloak.hostname = 'changed.example';
  const next = application(values, {namespace: 'green'}).spec;
  assert.equal(Object.hasOwn(next, 'running'), false);
  assert.equal(Object.hasOwn(next.application ?? {}, 'replicas'), false);
  assert.equal(next.configuration.hostname, 'https://changed.example');
});
check('obsolete runtime Helm authorities are rejected instead of silently ignored', () => {
  for (const connections of [false, true]) {
    for (const service of [{running: false}, {replicas: 0}, {application: {replicas: 2}}]) {
      const values = base(); values.services.keycloak = service;
      rejects(values, /unknown/, {connections});
    }
    for (const field of ['running', 'replicas']) {
      const values = base(); values[field] = false;
      rejects(values, /schema|allowed|false/i, {connections});
      const selected = selection(); selected.operations.deployments.source[field] = field === 'running' ? false : 0;
      rejects(selected, /runtime intent/, {connections, namespace: 'source'});
    }
  }
});
check('release inventory accepts the selected artifact and rejects a different package', () => {
  const v = base(); v.services.keycloak.release = chartVersion;
  assert.equal(application(v).spec.packageRelease, chartVersion);
  documents(render(v, {connections: true}));
  v.services.keycloak.release = '0.5.1';
  for (const connections of [false, true]) assert.notEqual(render(v, {connections}).status, 0);
  v.services.keycloak.release = 0.6;
  rejects(v, /release must be string/);
});
check('managed TLS follows hostname changes even while application routing is withheld', () => {
  const v = base();
  v.services.keycloak = {hostname: 'accounts.example', ingress: {enabled: false, className: 'edge', tlsSecretName: 'accounts-tls'}, tls: {acmeServer: 'https://acme-staging-v02.api.letsencrypt.org/directory'}};
  const {docs} = application(v);
  const certificate = docs.find(d => d?.kind === 'Certificate');
  const issuer = docs.find(d => d?.kind === 'Issuer');
  assert.deepEqual(certificate.spec.dnsNames, ['accounts.example']);
  assert.equal(certificate.spec.secretName, 'accounts-tls');
  assert.equal(certificate.spec.issuerRef.name, issuer.metadata.name);
  assert.equal(issuer.spec.acme.server, v.services.keycloak.tls.acmeServer);
  assert.equal(issuer.spec.acme.solvers[0].http01.ingress.ingressClassName, 'edge');
  const d = documents(render(v, {connections: true}))[0];
  assert.equal(d.services.keycloak.endpoints.https.hostname, certificate.spec.dnsNames[0]);
  assert.equal(d.services.keycloak.endpoints.https.ingressEnabled, false);
  assert.deepEqual(d.credentials.requirements.keycloak.tls.sources, ['cert-manager']);
});
check('external TLS leaves certificate ownership outside the release', () => {
  const v = base(); v.services.keycloak.tls = {managed: false};
  const {docs} = application(v);
  assert.equal(docs.some(d => ['Issuer', 'Certificate'].includes(d?.kind)), false);
  const d = documents(render(v, {connections: true}))[0];
  assert.deepEqual(d.credentials.requirements.keycloak.tls.sources, ['external', 'recovered']);
  v.services.keycloak.ingress = {enabled: false, tlsSecretName: ''};
  application(v);
  v.services.keycloak.tls.managed = true;
  rejects(v, /tlsSecretName is required/);
});
check('explicit hostname wins; removing it restores current-domain derivation', () => {
  const v = base(); v.services.keycloak.hostname = 'accounts.example'; v.dns.root = 'changed.example';
  assert.equal(application(v).spec.configuration.hostname, 'https://accounts.example');
  delete v.services.keycloak.hostname;
  assert.equal(application(v).spec.configuration.hostname, 'https://login.changed.example');
  delete v.dns; v.services.keycloak.hostname = 'accounts.example';
  assert.equal(application(v).spec.configuration.hostname, 'https://accounts.example');
});
check('instance selection is independent of Helm release and ignores unrelated services', () => {
  const v = base(); v.instance = 'accounts'; v.services = {accounts: {hostname: 'accounts.example'}, keycloak: 'not selected', paperless: {foreign: true}};
  v.observed = {server: '192.0.2.1'}; v.infrastructure = {provider: 'external'};
  const {spec} = application(v);
  assert.equal(spec.identity.instance, 'accounts');
  assert.equal(spec.configuration.hostname, 'https://accounts.example');
  assert.equal(spec.observed, undefined);
});
check('false, map siblings and replacement lists survive static defaulting', () => {
  const v = base(); v.services.keycloak = {database: {size: '8Gi'}, ingress: {enabled: false, administratorSourceRanges: ['192.0.2.1/32'], controllerPodLabels: {custom: 'edge'}}};
  const {spec} = application(v);
  assert.equal(spec.application, undefined); assert.equal(spec.ingress.enabled, false);
  assert.equal(spec.database.size, '8Gi'); assert.equal(spec.database.secretName, 'keycloak-database');
  assert.deepEqual(spec.ingress.administratorSourceRanges, ['192.0.2.1/32']);
  assert.deepEqual(spec.ingress.controllerPodLabels, {'app.kubernetes.io/name': 'traefik', custom: 'edge'});
});
check('standalone repository maps to native backup without intermediate data', () => {
  const v = base(); v.services.keycloak.backup = {repository: 'rest:https://backup.example/acme/', suspend: false};
  const {spec} = application(v);
  assert.equal(spec.backup.repository, v.services.keycloak.backup.repository);
  assert.equal(spec.backup.readerSecret, 'keycloak-backup-reader');
  assert.equal(spec.backup.suspend, false); assert.equal(spec.backup.enabled, undefined);
});
check('matching endpoint binding supplies repository and scoped secrets', () => {
  const v = base(); v.services.keycloak.backup = {endpoint: 'archive', readerSecret: 'override-reader'};
  v.resolved = {backup: {bindings: {keycloak: {enabled: true, endpoint: 'archive', repository: 'rest:https://backup.example/acme/', readerSecret: 'bound-reader', writerSecret: 'bound-writer'}, paperless: 'unrelated'}}};
  const {spec} = application(v);
  assert.equal(spec.backup.repository, v.resolved.backup.bindings.keycloak.repository);
  assert.equal(spec.backup.readerSecret, 'override-reader'); assert.equal(spec.backup.writerSecret, 'bound-writer');
});
check('explicit disablement ignores malformed stale resolved data', () => {
  const v = base(); v.services.keycloak.backup = {enabled: false}; v.resolved = 'obsolete';
  assert.equal(application(v).spec.backup, undefined);
});
check('deployment rejects unresolved or contradictory backup choices', () => {
  for (const backup of [{enabled: true}, {endpoint: 'missing'}]) {
    const v = base(); v.services.keycloak.backup = backup; rejects(v, /requires|must supply/);
  }
  const v = base(); v.services.keycloak.backup = {repository: 'rest:https://one.example/app/'};
  v.resolved = {backup: {bindings: {keycloak: {repository: 'rest:https://two.example/app/'}}}};
  rejects(v, /conflicts/);
  v.services.keycloak.backup.endpoint = 'archive'; rejects(v, /either endpoint or repository/);
  v.services.keycloak.backup = {};
  v.resolved.backup.bindings.keycloak = {enabled: true}; rejects(v, /requires/);
});
check('selected owned typos, invalid types and unknown binding fields fail', () => {
  for (const service of [{hostnme: 'wrong.example'}, {database: {secreName: 'typo'}}, {application: {replicas: -1}}, {application: {replicas: 1.5}}, {backup: {enabled: 'false'}}, {ingress: {maintenance: true}}, {hostname: 'https://wrong.example'}]) {
    const v = base(); v.services.keycloak = service; rejects(v, /unknown|minimum|integer|boolean|required|format/);
  }
  const v = base(); v.resolved = {backup: {bindings: {keycloak: {repository: 'rest:https://backup.example/acme/', readerSecrt: 'typo'}}}}; rejects(v, /unknown/);
});
check('legacy service roots fail but shared backup policy is permitted', () => {
  const v = base(); v.backup = {defaultEndpoint: 'archive'}; application(v);
  v.backup.repository = 'rest:https://backup.example/acme/'; rejects(v, /schema|allowed|false/i);
  delete v.backup; v.identity = {installation: 'old', instance: 'old'}; rejects(v, /schema|allowed|false/i);
});
check('declarations work before resolution and emit no deployable resources', () => {
  const v = base(); v.services.keycloak.backup = {endpoint: 'archive'}; v.resolved = 'ignored';
  const docs = documents(render(v, {connections: true, release: 'inspection'}));
  assert.equal(docs.length, 1);
  const d = docs[0]; assert.equal(d.kind, undefined); assert.equal(d.apiVersion, undefined);
  assert.equal(d.services.keycloak.oidc.issuer, 'https://login.acme.example/realms/acme');
  assert.equal(d.backup.requests.keycloak.endpoint, 'archive');
  assert.deepEqual(d.credentials.requirements.keycloak.backupReader.keys, ['password', 'username', 'restPassword']);
  assert.equal(d.credentials.bindings.keycloak.backupReader.secretName, 'keycloak-backup-reader');
  rejects(v, /resolved/);
});
check('declarations preserve implicit backup eligibility and explicit disablement', () => {
  const v = base();
  assert.equal(documents(render(v, {connections: true}))[0].backup.requests.keycloak.enabled, true);
  v.services.keycloak.backup = {enabled: false};
  const d = documents(render(v, {connections: true}))[0];
  assert.equal(d.backup.requests.keycloak.enabled, false);
  assert.equal(d.credentials.requirements.keycloak.backupReader, undefined);
});
check('empty optional bootstrap reference omits permissions and declaration bindings', () => {
  const v = base(); v.services.keycloak.bootstrapAdminSecret = '';
  const {spec, docs} = application(v);
  assert.equal(spec.bootstrapAdminSecret, undefined);
  const secretRule = docs.find(d => d?.kind === 'Role').rules.find(r => r.resources.includes('secrets'));
  assert.deepEqual(secretRule.resourceNames.sort(), ['keycloak-database', 'keycloak-database-admin']);
  const d = documents(render(v, {connections: true}))[0];
  assert.equal(d.credentials.bindings.keycloak.bootstrapAdmin, undefined);
});
check('native recovery declares no privileged administrator startup dependency', () => {
  const v = base();
  v.services.keycloak.backup = {repository: 'rest:https://backup.example/acme/'};
  const {spec, docs} = application(v);
  assert.equal(spec.recoveryAdminSecret, undefined);
  const secretRule = docs.find(d => d?.kind === 'Role').rules.find(r => r.resources.includes('secrets'));
  assert.deepEqual(secretRule.resourceNames.sort(), ['keycloak-bootstrap', 'keycloak-database', 'keycloak-database-admin']);
  const d = documents(render(v, {connections: true}))[0];
  assert.deepEqual(Object.keys(d.credentials.requirements.keycloak).sort(), ['backupReader', 'backupWriter', 'bootstrapAdmin', 'database', 'databaseAdmin', 'tls']);
  assert.equal(d.credentials.bindings.keycloak.recoveryAdmin, undefined);
});
check('removed recovery administrator input is rejected in both render modes', () => {
  const v = base(); v.services.keycloak.recoveryAdminSecret = 'obsolete-admin';
  for (const connections of [false, true]) rejects(v, /unknown/, {connections});
});
function selection() { return {
  ...base(), deployment: 'source',
  services: {
    'identity.provider': {
      ingress: {enabled: true, maintenance: true, administratorSourceRanges: ['192.0.2.1/32']},
      backup: {repository: 'rest:https://backup.example/acme/', suspend: true},
    },
    keycloak: 'unrelated', foreign: {unknown: true},
  },
  deployments: {
    source: {instance: 'identity.provider', namespace: 'source', release: 'keycloak', package: 'candidate'},
    target: {instance: 'identity.provider', namespace: 'target', release: 'keycloak', package: 'candidate'},
    foreign: 'unrelated',
  },
  packages: {candidate: {version: chartVersion, chart: 'local-candidate'}, foreign: null},
  operations: {deployments: {
    source: {ingressEnabled: false, maintenance: false, backupSuspend: false, recordedBy: 'operator'},
    target: {ingressEnabled: true, maintenance: true, backupSuspend: true},
    foreign: 'unrelated',
  }},
}; }
check('deployment static overrides select the stable dotted instance without consuming unrelated inputs', () => {
  const v = selection();
  const {spec} = application(v, {namespace: 'source'});
  assert.deepEqual(spec.identity, {installation: 'acme', instance: 'identity.provider'});
  assert.equal(spec.application, undefined);
  assert.equal(spec.ingress.enabled, false);
  assert.equal(spec.ingress.maintenance, false);
  assert.equal(spec.backup.suspend, false);
  const d = documents(render(v, {namespace: 'source', connections: true}))[0];
  assert.deepEqual(Object.keys(d.services), ['identity.provider']);
  assert.equal(d.services['identity.provider'].endpoints.https.ingressEnabled, false);
  assert.equal(d.backup.requests['identity.provider'].repository, spec.backup.repository);
  assert.ok(d.credentials.bindings['identity.provider']);
  v.instance = 'identity.provider';
  application(v, {namespace: 'source'});
});
check('absent deployment overrides inherit static service choices and then chart defaults', () => {
  const v = selection();
  for (const intent of [undefined, {}]) {
    if (intent === undefined) delete v.operations; else v.operations = {deployments: {source: intent}};
    const {spec} = application(v, {namespace: 'source'});
    assert.equal(spec.application, undefined);
    assert.equal(spec.ingress.enabled, true);
    assert.equal(spec.ingress.maintenance, true);
    assert.equal(spec.backup.suspend, true);
  }
  v.services['identity.provider'] = {};
  const {spec} = application(v, {namespace: 'source'});
  assert.equal(spec.application, undefined);
  assert.equal(spec.ingress.maintenance, false);
  assert.equal(spec.backup, undefined);
  delete v.deployment;
  v.instance = 'identity.provider';
  v.operations = 'not consumed without a deployment';
  assert.equal(application(v).spec.identity.instance, 'identity.provider');
  v.deployment = '';
  assert.equal(application(v).spec.identity.instance, 'identity.provider');
});
check('selected stable instance owns backup binding lookup and connection keys', () => {
  const v = selection();
  v.services['identity.provider'].backup = {endpoint: 'archive'};
  v.resolved = {backup: {bindings: {
    'identity.provider': {endpoint: 'archive', repository: 'rest:https://backup.example/stable/', readerSecret: 'stable-reader'},
    source: 'wrong deployment key', target: 'wrong deployment key', keycloak: 'wrong default key',
  }}};
  const {spec} = application(v, {namespace: 'source'});
  assert.equal(spec.backup.repository, 'rest:https://backup.example/stable/');
  assert.equal(spec.backup.readerSecret, 'stable-reader');
  assert.equal(spec.backup.suspend, false);
  const d = documents(render(v, {namespace: 'source', connections: true}))[0];
  assert.equal(d.backup.requests['identity.provider'].endpoint, 'archive');
  delete v.resolved.backup.bindings['identity.provider'];
  rejects(v, /matching resolved.backup.bindings/, {namespace: 'source'});
});
check('source and target resolution in one render cannot mutate shared values or leak intent', () => {
  const temp = mkdtempSync(join(tmpdir(), 'keycloak-chart-'));
  try {
    if (statSync(chart).isDirectory()) cpSync(chart, temp, {recursive: true});
    else {
      const unpack = spawnSync('tar', ['-xzf', chart, '--strip-components=1', '-C', temp], {encoding: 'utf8'});
      assert.equal(unpack.status, 0, unpack.stderr);
    }
    writeFileSync(join(temp, 'templates', 'isolation.yaml'), `
{{- $before := mustToJson .Values -}}
{{- $source := include "keycloak.service" . | fromYaml -}}
{{- $targetRoot := deepCopy . -}}
{{- $_ := set $targetRoot.Values "deployment" "target" -}}
{{- $_ := set $targetRoot.Release "Namespace" "target" -}}
{{- $target := include "keycloak.service" $targetRoot | fromYaml -}}
{{- $again := include "keycloak.service" . | fromYaml -}}
before: {{ $before | quote }}
after: {{ mustToJson .Values | quote }}
source: {{ mustToJson $source | quote }}
target: {{ mustToJson $target | quote }}
again: {{ mustToJson $again | quote }}
`);
    const d = documents(render(selection(), {namespace: 'source', chartPath: temp, showOnly: 'templates/isolation.yaml'}))[0];
    assert.equal(d.before, d.after);
    assert.equal(d.source, d.again);
    const source = JSON.parse(d.source), target = JSON.parse(d.target);
    assert.deepEqual([source.ingress.enabled, source.ingress.maintenance, source.backup.suspend], [false, false, false]);
    assert.deepEqual([target.ingress.enabled, target.ingress.maintenance, target.backup.suspend], [true, true, true]);
  } finally { rmSync(temp, {recursive: true, force: true}); }
});
check('every rendering mode rejects missing selected references and invocation mismatches', () => {
  const cases = [
    [v => { delete v.deployments; }, /deployments must be an object/],
    [v => { delete v.deployments.source; }, /deployments.source is required/],
    [v => { v.deployments.source = null; }, /deployments.source must be object/],
    ...['instance', 'namespace', 'release', 'package'].map(field => [v => { delete v.deployments.source[field]; }, new RegExp(`deployments.source.${field} is required`)]),
    [v => { delete v.services['identity.provider']; }, /services.identity.provider is required/],
    [v => { v.instance = 'keycloak'; }, /instance.*conflicts/],
    [v => { delete v.packages; }, /packages must be an object/],
    [v => { delete v.packages.candidate; }, /packages.candidate is required/],
    [v => { v.packages.candidate = null; }, /packages.candidate must be object/],
    [v => { delete v.packages.candidate.version; }, /packages.candidate.version is required/],
    [v => { v.packages.candidate.version = '0.6.1'; }, /packages.candidate.version selects/],
    [v => { v.deployments.source.namespace = 'other'; }, /does not match Helm namespace/],
    [v => { v.deployments.source.release = 'other'; }, /does not match Helm release/],
  ];
  for (const connections of [false, true]) for (const [change, message] of cases) {
    const v = selection(); change(v); rejects(v, message, {namespace: 'source', connections});
  }
});
check('selected consumed mapping and intent fields reject malformed values in every mode', () => {
  const changes = [
    v => { v.deployment = 3; },
    v => { v.deployments = []; },
    ...['instance', 'namespace', 'release', 'package'].flatMap(field => [null, '', 1].map(value => v => { v.deployments.source[field] = value; })),
    v => { v.packages.candidate.version = 7; },
    v => { v.operations = []; },
    v => { v.operations.deployments = []; },
    v => { v.operations.deployments.source = null; },
    ...['ingressEnabled', 'maintenance', 'backupSuspend'].flatMap(field => ['false', 0, null].map(value => v => { v.operations.deployments.source[field] = value; })),
  ];
  for (const connections of [false, true]) for (const change of changes) {
    const v = selection(); change(v);
    rejects(v, /schema|must be|minimum|maximum|too short|format|type/i, {namespace: 'source', connections});
  }
});
check('Playground accepts independent domains and DNS length boundaries', () => {
  for (const host of ['play.kc060.acme.fke.systems', 'auth.customer.example', 'xn--bcher-kva.example', 'a'.repeat(63) + '.example', Array(3).fill('a'.repeat(63)).concat('b'.repeat(61)).join('.')]) {
    const result = spawnSync(helm, ['template', 'auth-playground', playgroundChart, '-f', '-'], {
      input: JSON.stringify({host, tlsSecret: 'playground-tls', keycloakIPv4: '192.0.2.1'}), encoding: 'utf8',
    });
    const ingress = documents(result).find(d => d?.kind === 'Ingress');
    assert.equal(ingress.spec.rules[0].host, host);
    assert.deepEqual(ingress.spec.tls[0].hosts, [host]);
  }
});
check('Playground rejects malformed hosts before creating an ingress', () => {
  for (const host of ['', 'https://auth.example', 'auth.example/path', 'auth.example:443', '*.example', 'Auth.example', 'auth_name.example', '-auth.example', 'auth-.example', 'auth..example', 'auth.example.', '192.0.2.1', 'a'.repeat(64) + '.example', Array(3).fill('a'.repeat(63)).concat('b'.repeat(62)).join('.')]) {
    const result = spawnSync(helm, ['template', 'auth-playground', playgroundChart, '-f', '-'], {
      input: JSON.stringify({host, tlsSecret: 'playground-tls', keycloakIPv4: '192.0.2.1'}), encoding: 'utf8',
    });
    assert.notEqual(result.status, 0, `invalid host accepted: ${host}`);
    assert.equal(result.stdout, '', 'invalid host emitted resources');
  }
});
console.log(`${checks} chart behavior checks passed`);
