#!/usr/bin/env node
// Runs only against explicitly retained disposable kc-a4-* namespaces.
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {execFileSync, spawn} from 'node:child_process';
import {randomBytes, createHash, createPublicKey, verify} from 'node:crypto';
import assert from 'node:assert/strict';
import path from 'node:path';

const state = path.resolve(process.env.KC_A4_STATE || '.lab/kc-a4');
const cfg = JSON.parse(await readFile(path.join(state, 'runtime.json')));
const secrets = JSON.parse(await readFile(path.join(state, 'credentials.json')));
const ca = await readFile(path.join(state, 'repository.crt'), 'utf8');
// Source fixture uses this exact candidate API; historical migrations live in A5.
const sourceValues = JSON.parse(await readFile(path.join(state, 'source-values.json')));
const source = 'kc-a4-source';
const base = sourceValues.configuration.hostname;
const realm = 'recovery-proof';
const repository = cfg.repository || 'rest:https://backup-store.kc-a4-repository.svc/keycloak/';
const fixtureRepository = kind => 'rest:'+new URL('../'+kind+'/',repository.slice('rest:'.length)).href;
const run = (cmd, args, input) => execFileSync(cmd, args, {
  input, encoding: 'utf8', timeout: 650000, maxBuffer: 32 * 1024 * 1024,
  env: {...process.env, HELM_CACHE_HOME: path.join(state, 'helm-cache')},
  stdio: ['pipe', 'pipe', 'pipe'],
});
const k = (...args) => run('kubectl', ['--kubeconfig', cfg.kubeconfig, ...args]);
const json = (...args) => JSON.parse(k(...args, '-o', 'json'));
const scope = ns => { assert.match(ns, /^kc-a4-[a-z0-9-]+$/); return ns; };
const apply = items => run('kubectl', ['--kubeconfig', cfg.kubeconfig, 'apply', '-f', '-'], JSON.stringify({apiVersion:'v1',kind:'List',items}));
const save = (name, data) => writeFile(path.join(state, name), JSON.stringify(data, null, 2)+'\n', {mode:0o600});
const sql = (ns, query) => run('kubectl', ['--kubeconfig',cfg.kubeconfig,'-n',scope(ns),'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'], query+'\n').trim();
const nativeRecord = ns => { const c=sql(ns,"SELECT coalesce(shobj_description(oid,'pg_database'),'') FROM pg_database WHERE datname=current_database()"); const record=c.match(/:recovery:(\{.*\})$/); return record?JSON.parse(record[1]):null; };
const app = ns => json('-n',scope(ns),'get','keycloakapplication/keycloak');
const condition = (a,type) => a.status?.conditions?.some(c=>c.type===type && c.status==='True' && c.observedGeneration===a.metadata.generation);
async function until(label, observe, accept, seconds=600) {
  const deadline=Date.now()+seconds*1000;
  let last;
  while(Date.now()<deadline) { last=await observe(); if(accept(last))return last; await new Promise(r=>setTimeout(r,1000)); }
  throw Error(label+' timed out: '+JSON.stringify(last));
}
const ready = ns => until(ns+' Ready',()=>app(ns),a=>condition(a,'Ready'));
const stopped = async ns => {
  const a=await until(ns+' whole application Stopped',()=>app(ns),a=>condition(a,'Stopped'));
  for(const component of ['keycloak','keycloak-db']) assert.equal(json('-n',ns,'get','pods','-l','app.kubernetes.io/name='+component).items.length,0);
  assert(json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak-backup').items.every(p=>['Succeeded','Failed'].includes(p.status.phase)&&!p.metadata.deletionTimestamp));
  return a;
};
const keycloakZero = ns => until(ns+' Keycloak zero with database running',()=>app(ns),a=>{
  assert.equal(a.spec.running,true);
  return a.status?.application?.activePods===0 && condition(a,'Quiescent') &&
    !condition(a,'Stopped') && json('-n',ns,'get','statefulset/keycloak-db').status.readyReplicas===1;
});
const installationFile=ns=>path.join(state,scope(ns),'installation.yaml');
function intent(ns, settings) {
  const spec={};
  for(const [key,value] of Object.entries(settings)){
    const parts=key.split('.');
    if(key==='running'){spec.running=value;continue}
    assert.equal(parts.length,2);
    (spec[parts[0]]??={})[parts[1]]=value;
  }
  k('-n',scope(ns),'patch','keycloakapplication/keycloak','--type=merge','-p',JSON.stringify({spec}));
}
function staticApply(ns){
  run('helm',['upgrade','keycloak',cfg.chart,'--kubeconfig',cfg.kubeconfig,'-n',scope(ns),
    '--force-conflicts','--reset-values','-f',installationFile(ns),'--wait=legacy','--timeout','10m']);
}
const backupConfig = {repository,readerSecret:'keycloak-backup-reader',writerSecret:'keycloak-backup-writer',schedule:'0 2 * * *',timeZone:'Etc/UTC',suspend:true};
async function install(ns,{backup=true,replicas=1,running=true,identity='kc-a4',instance='keycloak',route=false,readerPassword=secrets.reader,bootstrap=true,repo=repository}={}) {
  scope(ns);
  const existing=json('get','namespaces').items;
  assert(!existing.some(n=>n.metadata.name===ns),'refusing to reuse an existing target namespace');
  const strings=[['keycloak-database',{username:'keycloak',database:'keycloak',password:randomBytes(24).toString('hex')}],['keycloak-database-admin',{username:'postgres',password:randomBytes(24).toString('hex')}],['keycloak-backup-reader',{password:secrets.restic,username:'reader',restPassword:readerPassword,'ca.crt':ca}],['keycloak-backup-writer',{password:secrets.restic,username:'writer',restPassword:secrets.writer,'ca.crt':ca}]];
  if(bootstrap)strings.push(['keycloak-bootstrap',{username:'admin',password:secrets.admin}]);
  apply([{apiVersion:'v1',kind:'Namespace',metadata:{name:ns,labels:{'kc-a4-lab':'true'}}}]);
  const tls=JSON.parse(await readFile(path.join(state,'tls.json')));
  const items=strings.map(([name,stringData])=>({apiVersion:'v1',kind:'Secret',metadata:{name,namespace:ns},stringData}));
  items.push({apiVersion:'v1',kind:'Secret',type:'kubernetes.io/tls',metadata:{name:'kc-a4-tls',namespace:ns},data:tls.data});
  apply(items);
  const service={
    hostname:new URL(base).hostname,
    tls:{managed:false},
    database:structuredClone(sourceValues.database),
    bootstrapAdminSecret:sourceValues.bootstrapAdminSecret,
    ingress:{...structuredClone(sourceValues.ingress),enabled:route},
    backup:backup?{...backupConfig,repository:repo}:{enabled:false},
  };
  const values={installation:identity,instance,services:{[instance]:service}};
  const directory=path.join(state,ns);await mkdir(directory,{recursive:true,mode:0o700});
  const file=installationFile(ns);await writeFile(file,JSON.stringify(values),{mode:0o600});
  const manifest=run('helm',['template','keycloak',cfg.chart,'-n',ns,'-f',file,'--show-only','templates/application.yaml']);
  const resource=JSON.parse(run('yq',['-o=json','.'],manifest));
  resource.metadata.namespace=ns;
  resource.spec.running=running;
  resource.spec.application??={};
  resource.spec.application.replicas=replicas;
  run('kubectl',['--kubeconfig',cfg.kubeconfig,'create','-f','-'],JSON.stringify(resource));
  run('helm',['install','keycloak',cfg.chart,'--kubeconfig',cfg.kubeconfig,'-n',ns,'-f',file,'--wait=legacy','--timeout','10m']);
  return ns;
}
async function backup(ns, label) {
  scope(ns); const name='proof-'+label+'-'+Date.now();
  k('-n',ns,'create','job',name,'--from=cronjob/keycloak-backup');
  await until('backup '+name,()=>json('-n',ns,'get','job',name),j=>{
    if(j.status?.failed)throw Error('backup failed: '+k('-n',ns,'logs','job/'+name));
    return j.status?.succeeded===1;
  });
  const receipt=JSON.parse(k('-n',ns,'logs','job/'+name,'-c','backup').trim());
  assert.match(receipt.snapshotID,/^[a-f0-9]{64}$/);await save(label+'-backup.json',receipt);return receipt;
}
async function token(form) {
  const response=await fetch(base+`/realms/${form.realm||realm}/protocol/openid-connect/token`,{method:'POST',body:new URLSearchParams(Object.fromEntries(Object.entries(form).filter(([k])=>k!=='realm')))});
  const body=await response.json();if(!response.ok)throw Error('token request HTTP '+response.status+' '+body.error);return body;
}
async function admin(method,url,body) {
  const t=await token({realm:'master',grant_type:'password',client_id:'admin-cli',username:'admin',password:secrets.admin});
  const response=await fetch(base+'/admin/'+url,{method,headers:{Authorization:'Bearer '+t.access_token,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});
  if(!response.ok)throw Error('administration HTTP '+response.status+' '+method+' '+url);
  const text=await response.text();return text?JSON.parse(text):null;
}
async function oidc() {
  const verifier=randomBytes(32).toString('base64url'),nonce=randomBytes(16).toString('hex'),stateValue=randomBytes(16).toString('hex');
  const redirect=base+'/proof-callback';
  const query=new URLSearchParams({client_id:'recovery-proof',response_type:'code',scope:'openid profile',redirect_uri:redirect,code_challenge:createHash('sha256').update(verifier).digest('base64url'),code_challenge_method:'S256',state:stateValue,nonce});
  const jar=new Map();
  const request=async(url,options={})=>{assert.equal(new URL(url).origin,base);const r=await fetch(url,{...options,redirect:'manual',headers:{...options.headers,Cookie:[...jar].map(([k,v])=>k+'='+v).join('; ')}});for(const line of r.headers.getSetCookie()){const pair=line.split(';')[0],p=pair.indexOf('=');jar.set(pair.slice(0,p),pair.slice(p+1))}return r};
  let response=await request(base+`/realms/${realm}/protocol/openid-connect/auth?`+query);
  let html=await response.text();
  const action=html.match(/<form[^>]+action="([^"]+)"/);
  assert(action,'OIDC did not present a native login form');
  const actionURL=action[1].replaceAll('&amp;','&');
  response=await request(actionURL,{method:'POST',body:new URLSearchParams({username:'recovery-user',password:secrets.user,credentialId:''})});
  assert.equal(response.status,302,'OIDC credentials did not produce an authorization redirect');
  const callback=new URL(response.headers.get('location'));assert.equal(callback.origin+callback.pathname,redirect);assert.equal(callback.searchParams.get('state'),stateValue);assert(callback.searchParams.get('code'));
  const tokens=await token({grant_type:'authorization_code',client_id:'recovery-proof',code:callback.searchParams.get('code'),redirect_uri:redirect,code_verifier:verifier});
  const [header,payload,signature]=tokens.id_token.split('.');const h=JSON.parse(Buffer.from(header,'base64url'));const claims=JSON.parse(Buffer.from(payload,'base64url'));
  const keys=await (await fetch(base+`/realms/${realm}/protocol/openid-connect/certs`)).json();const key=keys.keys.find(k=>k.kid===h.kid);
  assert.equal(h.alg,'RS256');assert(verify('RSA-SHA256',Buffer.from(header+'.'+payload),createPublicKey({key,format:'jwk'}),Buffer.from(signature,'base64url')));
  assert.equal(claims.iss,base+'/realms/'+realm);assert.equal(claims.nonce,nonce);assert.equal(claims.aud,'recovery-proof');assert.equal(claims.preferred_username,'recovery-user');
  return {tokens,subject:claims.sub,keyID:h.kid};
}
async function activate(ns) {
  for(const resource of json('get','ingresses','-A').items)if(resource.spec.rules?.some(r=>r.host===new URL(base).hostname)) {
    const owner=scope(resource.metadata.namespace);if(owner!==ns){intent(owner,{running:false,'ingress.enabled':false,'backup.suspend':true});await stopped(owner)}
  }
  intent(ns,{running:true,'application.replicas':1,'ingress.enabled':true});await ready(ns);
  await until('test route',async()=>{try{return (await fetch(base+'/realms/master/.well-known/openid-configuration')).status}catch{return 0}},x=>x===200,120);
}
async function assertRestored(ns, snapshot, firstName='Latest') {
  const a=await ready(ns),r=nativeRecord(ns);assert.equal(r.phase,'Complete');assert.equal(r.snapshotID,snapshot);assert.equal(a.status.operation.snapshot.id,snapshot);
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  assert.equal(sql(ns,`SELECT id FROM user_entity WHERE username='recovery-user'`),fixture.userID);
  assert.equal(sql(ns,`SELECT first_name FROM user_entity WHERE id='${fixture.userID}'`),firstName);
  assert.equal(sql(ns,`SELECT id FROM realm WHERE name='${realm}'`),fixture.realmID);
  const targetPVC=json('-n',ns,'get','pvc/keycloak-db'),sourcePVC=JSON.parse(await readFile(path.join(state,'source-pvc.json')));
  assert.notEqual(targetPVC.metadata.uid,sourcePVC.metadata.uid);assert.notEqual(targetPVC.spec.volumeName,sourcePVC.spec.volumeName);
  return {applicationUID:a.metadata.uid,pvcUID:targetPVC.metadata.uid,volume:targetPVC.spec.volumeName,operation:a.status.operation};
}
async function sourceScenario() {
  await ready(source);
  await save('tls.json',{data:json('-n',source,'get','secret/kc-a4-tls').data});
  await save('source-pvc.json',json('-n',source,'get','pvc/keycloak-db'));
  await admin('POST','realms',{realm,enabled:true,registrationAllowed:false,resetPasswordAllowed:false,ssoSessionIdleTimeout:7200,ssoSessionMaxLifespan:7200});
  await admin('POST',`realms/${realm}/clients`,{clientId:'recovery-proof',enabled:true,publicClient:true,standardFlowEnabled:true,directAccessGrantsEnabled:true,redirectUris:[base+'/proof-callback'],attributes:{'pkce.code.challenge.method':'S256'}});
  await admin('POST',`realms/${realm}/users`,{username:'recovery-user',enabled:true,email:'recovery@example.invalid',firstName:'Older',lastName:'Proof',emailVerified:true,credentials:[{type:'password',value:secrets.user,temporary:false}]});
  let user=(await admin('GET',`realms/${realm}/users?username=recovery-user&exact=true`))[0];assert(user?.id);
  const r=await admin('GET',`realms/${realm}`);
  const online=await oidc();assert.equal(online.subject,user.id);
  const offline=await token({grant_type:'password',client_id:'recovery-proof',username:'recovery-user',password:secrets.user,scope:'openid offline_access'});
  const offlineClaims=JSON.parse(Buffer.from(offline.refresh_token.split('.')[1],'base64url'));assert.equal(offlineClaims.typ,'Offline');
  await save('old-tokens.json',{online:online.tokens.refresh_token,offline:offline.refresh_token});
  intent(source,{'backup.repository':repository,'backup.readerSecret':backupConfig.readerSecret,
    'backup.writerSecret':backupConfig.writerSecret,'backup.schedule':backupConfig.schedule,
    'backup.timeZone':backupConfig.timeZone,'backup.suspend':true});
  await backup(source,'older');
  user.firstName='Latest';await admin('PUT',`realms/${realm}/users/${user.id}`,user);
  assert.equal(sql(source,`SELECT first_name FROM user_entity WHERE id='${user.id}'`),'Latest');
  const sessions=sql(source,'SELECT offline_flag,count(*) FROM offline_user_session GROUP BY offline_flag ORDER BY offline_flag');assert.match(sessions,/0\|[1-9]/);assert.match(sessions,/1\|[1-9]/);
  const latest=await backup(source,'latest');
  await save('fixture.json',{userID:user.id,realmID:r.id,keyID:online.keyID,snapshotID:latest.snapshotID,sessions,realmNotBefore:sql(source,`SELECT not_before FROM realm WHERE name='${realm}'`)});
  intent(source,{running:false,'ingress.enabled':false});await stopped(source);
  return {sourceRelease:app(source).spec.packageRelease,snapshot:latest.snapshotID,sessions,sourceStopped:true};
}
async function freshScenario() {
  const cases=[];
  for(const [name,options] of [['kc-a4-no-repo',{backup:false,identity:'recovery'}],['kc-a4-no-match',{identity:'kc-a4-empty',instance:'recovery'}]]) {
    await install(name,options);const a=await ready(name);assert.equal(sql(name,"SELECT count(*) FROM realm WHERE name='master'"),'1');assert.equal(sql(name,"SELECT count(*) FROM realm WHERE name='recovery-proof'"),'0');
    const rec=nativeRecord(name);if(options.backup===false)assert.equal(rec,null);else{assert.equal(rec.phase,'Complete');assert.equal(rec.snapshotID,'');assert.equal(rec.initializationStarted,true)}
    cases.push({namespace:name,state:a.status.state,record:rec});intent(name,{running:false});await stopped(name);
  }return cases;
}
async function vanillaScenario() {
  const results=[];
  for(const ns of ['kc-a4-no-repo','kc-a4-no-match']) {
    await activate(ns);
    const t=await token({realm:'master',grant_type:'password',client_id:'admin-cli',username:'admin',password:secrets.admin});
    const response=await fetch(base+'/admin/realms/master',{headers:{Authorization:'Bearer '+t.access_token}});
    assert.equal(response.status,200);assert.equal((await response.json()).realm,'master');
    results.push({namespace:ns,administratorAuthentication:true,trustedHTTPS:true});
    intent(ns,{running:false,'ingress.enabled':false});await stopped(ns);
  }
  return results;
}

async function restoreScenario(ns='kc-a4-restored') {
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  intent(source,{running:false,'ingress.enabled':false});await stopped(source);
  k('-n',source,'scale','deployment/keycloak-controller','--replicas=0');
  k('-n',source,'wait','pod','-l','app.kubernetes.io/name=keycloak-controller','--for=delete','--timeout=120s');
  assert.equal(json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak-db').items.length,0);
    await install(ns,{bootstrap:false});const result=await assertRestored(ns,fixture.snapshotID);await activate(ns);
    assert.equal(Object.hasOwn(app(ns).spec,'recoveryAdminSecret'),false);
    assert(!json('-n',ns,'get','secrets').items.some(s=>['keycloak-recovery','keycloak-bootstrap'].includes(s.metadata.name)));
    assert.equal(sql(ns,`SELECT not_before FROM realm WHERE name='${realm}'`),fixture.realmNotBefore);
    // These still-valid sessions were captured in the recovery point; Keycloak
    // handles them natively rather than the package resetting their realm.
    const old=JSON.parse(await readFile(path.join(state,'old-tokens.json')));
    for(const refresh of [old.online,old.offline])assert((await token({grant_type:'refresh_token',client_id:'recovery-proof',refresh_token:refresh})).access_token);
    const login=await oidc();assert.equal(login.subject,fixture.userID);assert.equal(login.keyID,fixture.keyID);
    assert.equal((await admin('GET',`realms/${realm}`)).id,fixture.realmID);
    return {...result,sourceDatabaseUnavailable:true,runtimeAdministratorCredentialsAbsent:true,oidc:'RS256 authorization-code/PKCE, identity and signing key preserved',capturedOnlineAndOfflineRefreshAccepted:true};
  // Leave the predecessor retired; fallback is an explicit synthetic-only case.
}

async function fallbackScenario() {
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  k('-n',source,'scale','deployment/keycloak-controller','--replicas=1');
  k('-n',source,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  await activate(source);const original=await oidc();
  assert.equal(original.subject,fixture.userID);assert.equal(original.keyID,fixture.keyID);
  await activate('kc-a4-restored');const restored=await oidc();
  assert.equal(restored.subject,fixture.userID);assert.equal(restored.keyID,fixture.keyID);
  return {sourceRelease:app(source).spec.packageRelease,targetRelease:app('kc-a4-restored').spec.packageRelease,identityPreserved:true,boundary:'Synthetic login activity only; no consequential writes or external effects',restoredTargetReactivated:true};
}

async function holdCatalog(ns, table='pg_catalog.pg_class') {
  const child=spawn('kubectl',['--kubeconfig',cfg.kubeconfig,'-n',scope(ns),'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],{stdio:['pipe','pipe','pipe']});
  let output='',failure='';child.stdout.on('data',b=>{output+=b});child.stderr.on('data',b=>{failure+=b});
  const ended=new Promise((resolve,reject)=>child.on('exit',code=>code===0?resolve():reject(Error('catalog fence exited '+code+' '+failure))));
  assert(['pg_catalog.pg_class','pg_catalog.pg_shdescription'].includes(table));
  child.stdin.write(`BEGIN; LOCK TABLE ${table} IN SHARE MODE; SELECT 'catalog-held';\n`);
  await until('native catalog fence',()=>output,s=>s.includes('catalog-held'),30);
  return async()=>{child.stdin.end('COMMIT;\n\\q\n');await ended};
}
async function initializerScenario() {
  const ns='kc-a4-init-lock';await install(ns,{replicas:0,identity:'kc-a4-lock'});
  await until('empty initializer target',()=>app(ns),a=>a.status?.application?.activePods===0&&a.status.state.classification==='Absent');
  k('label','namespace',ns,'kc-a4-lab-');
  intent(ns,{'application.replicas':1});
  const job=await until('finite recovery admitted',()=>json('-n',ns,'get','jobs').items.find(j=>j.metadata.name.startsWith('keycloak-recover-')),Boolean,60);
  k('-n',ns,'scale','deployment/keycloak-controller','--replicas=0');
  k('-n',ns,'wait','pod','-l','app.kubernetes.io/name=keycloak-controller','--for=delete','--timeout=120s');
  k('label','namespace',ns,'kc-a4-lab=true');
  await until('recovery without controller',()=>json('-n',ns,'get','job',job.metadata.name),j=>j.status?.succeeded===1,180);
  assert.equal(nativeRecord(ns).phase,'NoBackup');
  const release=await holdCatalog(ns,'pg_catalog.pg_shdescription');
  try {
    k('-n',ns,'scale','deployment/keycloak-controller','--replicas=1');
    await until('initializer owns uncommitted native claim',()=>sql(ns,"SELECT count(*) FROM pg_stat_activity a WHERE a.application_name='bartl-keycloak' AND a.wait_event_type='Lock' AND a.query LIKE 'COMMENT ON DATABASE%' AND EXISTS(SELECT 1 FROM pg_locks l WHERE l.pid=a.pid AND l.locktype='advisory' AND l.granted)"),x=>x==='1',120);
    const pod=json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items[0];assert(pod);
    for(let n=0;n<3;n++) {
      await new Promise(r=>setTimeout(r,2000));
      assert.equal(json('-n',ns,'get','deployment/keycloak').spec.replicas,1);
      const current=json('-n',ns,'get','pod',pod.metadata.name);assert.equal(current.metadata.deletionTimestamp,undefined);
      assert.equal(nativeRecord(ns).initializationStarted,undefined);
    }
  } finally {await release();}
  await ready(ns);assert.equal(nativeRecord(ns).phase,'Complete');
  intent(ns,{running:false});await stopped(ns);
  return {namespace:ns,uncommittedClaimPreserved:true,initialized:true};
}

async function recoveryBoundary(ns) {
  const a=await until('empty zero-replica target',()=>app(ns),a=>a.status?.application?.activePods===0&&a.status?.state?.classification==='Absent');
  assert.equal(a.spec.application.replicas,0);
  const release=await holdCatalog(ns);
  intent(ns,{'application.replicas':1});
  const backend=await until('restore transaction blocked inside native DDL',()=>{
    const rows=sql(ns,"SELECT coalesce(json_agg(row_to_json(s)),'[]'::json) FROM (SELECT pid,client_addr::text,application_name,wait_event FROM pg_stat_activity WHERE datname=current_database() AND application_name='psql' AND wait_event_type='Lock' AND pid<>pg_backend_pid()) s");
    return JSON.parse(rows);
  },rows=>rows.length===1,180);
  const job=json('-n',ns,'get','jobs').items.find(j=>j.metadata.name.startsWith('keycloak-recover-'));assert(job);
  assert.equal(sql(ns,`SELECT count(*) FROM pg_locks WHERE pid=${backend[0].pid} AND locktype='advisory' AND granted`),'1');
  return {release,backend:backend[0],job};
}
async function interruptionScenario() {
  const rollback='kc-a4-rollback';await install(rollback,{replicas:0});
  let fence=await recoveryBoundary(rollback);
  const pods=json('-n',rollback,'get','pods','-l','job-name='+fence.job.metadata.name).items;
  assert.equal(pods.length,1);
  k('-n',rollback,'delete','job',fence.job.metadata.name,'--cascade=foreground','--wait=true','--timeout=60s');
  await fence.release();
  const refused=await until('interrupted restore refusal',()=>app(rollback),a=>condition(a,'Degraded'));
  assert(!condition(refused,'Quiescent'),'unresolved restore must not report quiescence');
  assert.equal(nativeRecord(rollback),null);
  assert.equal(sql(rollback,"SELECT count(*) FROM pg_tables WHERE schemaname='public'"),'0');
  assert.equal(json('-n',rollback,'get','deployments').items.filter(d=>d.metadata.name==='keycloak').length,0);
  k('-n',rollback,'rollout','restart','deployment/keycloak-controller');
  k('-n',rollback,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  const after=await until('reentry retains rollback refusal',()=>app(rollback),a=>condition(a,'Degraded'));
  assert(!json('-n',rollback,'get','jobs').items.some(j=>j.metadata.name===fence.job.metadata.name));
  assert.equal(nativeRecord(rollback),null);
  intent(rollback,{running:false});await stopped(rollback);

  const committed='kc-a4-commit-loss';await install(committed,{replicas:0});
  fence=await recoveryBoundary(committed);
  const duplicate=structuredClone(fence.job);
  duplicate.metadata={name:'duplicate-native-worker',namespace:committed,ownerReferences:fence.job.metadata.ownerReferences};
  delete duplicate.status;delete duplicate.spec.selector;delete duplicate.spec.manualSelector;
  duplicate.spec.template.metadata.labels=Object.fromEntries(Object.entries(duplicate.spec.template.metadata.labels).filter(([key])=>!key.includes('job-name')&&!key.includes('controller-uid')));
  apply([duplicate]);
  await until('duplicate restore rejected by transaction fence',()=>json('-n',committed,'get','job/duplicate-native-worker'),j=>j.status?.failed===1,120);
  assert.equal(nativeRecord(committed),null);
  const pod=json('-n',committed,'get','pods','-l','job-name='+fence.job.metadata.name).items[0];assert(pod?.status?.podIP);
  // Lose the worker's observer connection while the separate transaction remains
  // fenced. Its next result read fails after COMMIT, simulating lost acknowledgement.
  assert.equal(sql(committed,`SELECT count(*) FROM pg_stat_activity WHERE client_addr='${pod.status.podIP}' AND application_name='bartl-keycloak'`),'1');
  assert.equal(sql(committed,`SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE client_addr='${pod.status.podIP}' AND application_name='bartl-keycloak'`),'t');
  intent(committed,{'application.replicas':0});
  k('-n',committed,'rollout','restart','deployment/keycloak-controller');
  k('-n',committed,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  await until('zero replica intent observed while restore still executing',()=>app(committed),a=>a.spec.application.replicas===0&&a.status?.application?.activePods===0);await fence.release();
  await until('committed native restore without successful Job',()=>nativeRecord(committed),r=>r?.phase==='Restored',120);
  const failed=await until('lost acknowledgement makes Job fail',()=>json('-n',committed,'get','job',fence.job.metadata.name),j=>j.status?.failed===1,120);
  const stop=await until('failed committed restore stays isolated',()=>app(committed),a=>condition(a,'Degraded')&&a.status?.application?.activePods===0);
  assert.equal(stop.status.application.activePods,0);assert(!condition(stop,'Ready'));assert(!condition(stop,'Quiescent'));
  assert.equal(json('-n',committed,'get','ingresses').items.length,0);
  k('-n',committed,'delete','job',fence.job.metadata.name,'--wait=true','--timeout=60s');
  k('-n',committed,'rollout','restart','deployment/keycloak-controller');
  k('-n',committed,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  await until('failed operation remains isolated',()=>app(committed),a=>condition(a,'Degraded')&&a.status?.application?.activePods===0);
  intent(committed,{'application.replicas':1});
  const retained=await until('lost acknowledgement cannot authorize continuation',()=>app(committed),a=>condition(a,'Degraded'));
  assert.equal(retained.status.application.activePods,0);
  assert(!json('-n',committed,'get','jobs').items.some(j=>j.metadata.name===fence.job.metadata.name));
  assert.equal(nativeRecord(committed).phase,'Restored');
  // Retry is a new empty target, not repair or replay of the ambiguous target.
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  const retry='kc-a4-independent-retry';await install(retry);
  const restored=await assertRestored(retry,fixture.snapshotID);await activate(retry);
  assert.equal((await oidc()).subject,fixture.userID);
  return {rollback:{reason:refused.status.state.reason,reentry:after.status.state.reason,jobUID:pods[0].metadata.ownerReferences[0].uid,nativePhase:null,tables:0},committed:{jobFailed:failed.status.failed,stopObserved:true,duplicateRefused:true,continuationRefused:true},independentRetry:restored};
}
async function fixturePod() {
  const found=json('-n','kc-a4-repository','get','pods').items.find(p=>p.metadata.name==='fixture');
  if(!found)apply([{apiVersion:'v1',kind:'Pod',metadata:{name:'fixture',namespace:'kc-a4-repository'},spec:{restartPolicy:'Never',automountServiceAccountToken:false,securityContext:{runAsUser:1000,runAsGroup:1000,fsGroup:1000},containers:[{name:'fixture',image:cfg.backupImage,command:['/bin/sleep','infinity'],env:[{name:'RESTIC_PASSWORD',valueFrom:{secretKeyRef:{name:'repository-init',key:'password'}}}],volumeMounts:[{name:'repository',mountPath:'/repository'}]}],volumes:[{name:'repository',persistentVolumeClaim:{claimName:'repository'}}]}}]);
  k('-n','kc-a4-repository','wait','pod/fixture','--for=condition=Ready','--timeout=180s');
}
const fixtureExec = (args,input) => run('kubectl',['--kubeconfig',cfg.kubeconfig,'-n','kc-a4-repository','exec','-i','fixture','--',...args],input);
async function invalidRepository(kind) {
  await fixturePod();const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  const dir='/repository/'+kind;const temporary='/tmp/'+kind;
  fixtureExec(['/bin/sh','-ec',`mkdir '${temporary}'; restic -r '${dir}' init`]);
  const config=JSON.parse(fixtureExec(['restic','-r',dir,'cat','config']));
  if(kind==='unreadable-snapshot') {
    fixtureExec(['/bin/sh','-ec',`printf corrupt > '${dir}/snapshots/${'a'.repeat(64)}'`]);
    return fixtureRepository(kind);
  }
  const manifest=JSON.parse(fixtureExec(['restic','-r','/repository/keycloak','dump',fixture.snapshotID,'/manifest.json']));
  manifest.repositoryID=config.id;
  if(kind==='bad-identity')manifest.installation='foreign';
  if(kind==='bad-integrity')manifest.dumpSHA256='0'.repeat(64);
  fixtureExec(['/bin/sh','-ec',`cat > '${temporary}/manifest.json'`],JSON.stringify(manifest));
  if(kind!=='incomplete')fixtureExec(['/bin/sh','-ec',`restic -r /repository/keycloak dump '${fixture.snapshotID}' /keycloak.dump > '${temporary}/keycloak.dump'`]);
  const files=kind==='incomplete'?'manifest.json':'manifest.json keycloak.dump';
  fixtureExec(['/bin/sh','-ec',`cd '${temporary}'; restic -r '${dir}' backup --host kc-a4 --tag installation=kc-a4 --tag instance=keycloak ${files}`]);
  return fixtureRepository(kind);
}
async function refusalScenario(resume) {
  const cases=[
    ['kc-a4-lookup-error',{readerPassword:'deliberately-invalid'}],
  ];
  for(const kind of ['bad-identity','bad-integrity','incomplete','unreadable-snapshot'])cases.push(['kc-a4-'+kind,{repo:resume==='resume'?fixtureRepository(kind):await invalidRepository(kind)}]);
  const results=[];
  for(const [ns,options] of cases) {
    const existing=json('get','namespaces').items.find(n=>n.metadata.name===ns);
    if(existing&&resume==='resume') {
      assert.equal(existing.metadata.labels['kc-a4-lab'],'true');
      assert.equal(app(ns).spec.backup.repository,options.repo||repository);
      intent(ns,{'application.replicas':1});
    } else await install(ns,options);
    const a=await until(ns+' safe refusal',()=>app(ns),a=>{
      if(!condition(a,'Degraded'))return false;
      const jobs=json('-n',ns,'get','jobs').items;
      return jobs.some(j=>j.status?.failed>0);
    });
    assert(!condition(a,'Ready'));assert.equal(json('-n',ns,'get','ingresses').items.length,0);
    const pods=json('-n',ns,'get','pods').items.filter(p=>p.metadata.labels?.['app.kubernetes.io/name']==='keycloak');assert.equal(pods.length,0);
    const tables=Number(sql(ns,"SELECT count(*) FROM pg_tables WHERE schemaname='public'"));
    assert.equal(tables,0);
    const jobs=json('-n',ns,'get','jobs').items;
    const logs=jobs.filter(j=>j.status?.failed).map(j=>({name:j.metadata.name,log:k('-n',ns,'logs','job/'+j.metadata.name)}));
    results.push({namespace:ns,reason:a.status.state.reason,tables,logs});
    intent(ns,{running:false});await stopped(ns);
  }return results;
}
async function subsequentScenario() {
  const ns='kc-a4-independent-retry';await activate(ns);
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  const user=(await admin('GET',`realms/${realm}/users?username=recovery-user&exact=true`))[0];
  user.firstName='Subsequent';await admin('PUT',`realms/${realm}/users/${user.id}`,user);
  assert.equal(sql(ns,`SELECT first_name FROM user_entity WHERE id='${fixture.userID}'`),'Subsequent');
  const captured=await backup(ns,'subsequent');
  intent(ns,{running:false,'ingress.enabled':false});await stopped(ns);
  const target='kc-a4-subsequent';await install(target);
  const result=await assertRestored(target,captured.snapshotID,'Subsequent');await activate(target);
  assert.equal((await oidc()).subject,fixture.userID);
  return {...result,sourceApplicationUID:captured.applicationUID,newWorkPreserved:true,restoredSubsequentBackup:true,oidc:true};
}
async function bypassScenario() {
  const ns='kc-a4-subsequent';await activate(ns);
  const before=json('-n',ns,'get','jobs').items.map(j=>j.metadata.uid).sort();
  k('-n',ns,'delete','secret','keycloak-backup-reader','keycloak-bootstrap','keycloak-database-admin');
  k('-n',ns,'rollout','restart','deployment/keycloak');k('-n',ns,'rollout','status','deployment/keycloak','--timeout=600s');
  k('-n',ns,'rollout','restart','deployment/keycloak-controller');k('-n',ns,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  await ready(ns);assert.deepEqual(json('-n',ns,'get','jobs').items.map(j=>j.metadata.uid).sort(),before);
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));assert.equal((await oidc()).subject,fixture.userID);
  return {namespace:ns,unchangedJobUIDs:before,oidc:true,unavailableSecrets:['reader','bootstrap','database-admin']};
}
async function staleScenario() {
  const ns='kc-a4-subsequent';await activate(ns);await ready(ns);
  const before=json('-n',ns,'get','deployment/keycloak');
  const pods=json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items.map(p=>p.metadata.uid);
  const release=await holdCatalog(ns,'pg_catalog.pg_shdescription');
  try {
    k('-n',ns,'rollout','restart','deployment/keycloak-controller');
    k('-n',ns,'rollout','status','deployment/keycloak-controller','--timeout=180s');
    for(let n=0;n<3;n++) {
      await new Promise(r=>setTimeout(r,6000));
      assert(condition(app(ns),'Ready'));
      assert.deepEqual(json('-n',ns,'get','deployment/keycloak').spec.template,before.spec.template);
      assert.deepEqual(json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items.map(p=>p.metadata.uid),pods);
      const controller=json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak-controller').items.find(p=>p.status.podIP);
      assert(controller);
      assert.equal(sql(ns,`SELECT count(*) FROM pg_stat_activity WHERE client_addr='${controller.status.podIP}' AND application_name='bartl-keycloak'`),'0');
    }
  } finally {await release()}
  const foreign='kc-a4-foreign-marker';await install(foreign,{replicas:0});
  await until('empty foreign marker target',()=>app(foreign),a=>a.status?.application?.activePods===0&&a.status.state.classification==='Absent');
  const original=sql(ns,"SELECT shobj_description(oid,'pg_database') FROM pg_database WHERE datname=current_database()");
  sql(foreign,"COMMENT ON DATABASE keycloak IS '"+original.replaceAll("'","''")+"'");
  intent(foreign,{'application.replicas':1});
  const mismatch=await until('foreign binding rejected',()=>app(foreign),a=>condition(a,'Degraded'));
  assert.equal(sql(foreign,"SELECT count(*) FROM pg_tables WHERE schemaname='public'"),'0');
  assert.equal(json('-n',foreign,'get','jobs').items.length,0);
  return {noRecurringDatabaseObserver:true,healthyWorkloadRetained:true,foreignReason:mismatch.status.state.reason};
}

async function unsupportedScenario() {
  const ns='kc-a4-unsupported';await install(ns);await ready(ns);
  intent(ns,{'application.replicas':0});await keycloakZero(ns);
  sql(ns,"UPDATE migration_model SET version='26.5.0' WHERE update_time=(SELECT max(update_time) FROM migration_model)");
  const users=sql(ns,'SELECT count(*) FROM user_entity');
  const jobs=json('-n',ns,'get','jobs').items.map(j=>j.metadata.uid).sort();
  intent(ns,{'application.replicas':1});
  await until('unsupported native startup refused',()=>json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items,pods=>pods.some(p=>p.status.containerStatuses?.some(c=>c.restartCount>0)));
  assert(!condition(app(ns),'Ready'));
  assert.equal(sql(ns,'SELECT count(*) FROM user_entity'),users);
  assert.deepEqual(json('-n',ns,'get','jobs').items.map(j=>j.metadata.uid).sort(),jobs);
  intent(ns,{'application.replicas':0});await keycloakZero(ns);
  return {sourceVersion:'26.5.0',targetVersion:'26.6.2',nativeStartupRefused:true,usersPreserved:true,noRecoveryReplay:true};
}

async function ordinaryScenario() {
  await activate(source);
  assert.equal(app(source).spec.packageRelease,cfg.candidateRelease,'ordinary startup must exercise the exact candidate');
  const realmID=sql(source,"SELECT id FROM realm WHERE name='recovery-proof'");
  const jobs=json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort();
  k('-n',source,'scale','keycloakapplication/keycloak','--replicas=2');await ready(source);
  const pods=json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items;
  assert.equal(pods.filter(p=>p.status.containerStatuses?.every(c=>c.ready)).length,2);
  await oidc(); await oidc();
  k('-n',source,'rollout','restart','deployment/keycloak');
  k('-n',source,'rollout','status','deployment/keycloak','--timeout=600s');await ready(source);await oidc();
  assert.equal(sql(source,"SELECT id FROM realm WHERE name='recovery-proof'"),realmID);
  assert.deepEqual(json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort(),jobs);
  intent(source,{'application.replicas':0});await keycloakZero(source);
  k('-n',source,'rollout','restart','deployment/keycloak-controller');
  k('-n',source,'rollout','status','deployment/keycloak-controller','--timeout=180s');await keycloakZero(source);
  assert.equal(json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items.length,0);
  const captured=await backup(source,'keycloak-zero');
  const fixture=JSON.parse(await readFile(path.join(state,'fixture.json')));
  fixture.snapshotID=captured.snapshotID;await save('fixture.json',fixture);
  k('-n',source,'scale','keycloakapplication/keycloak','--replicas=2');
  intent(source,{running:false});await stopped(source);
  staticApply(source);
  k('-n',source,'rollout','restart','deployment/keycloak-controller');
  k('-n',source,'rollout','status','deployment/keycloak-controller','--timeout=180s');
  await stopped(source);
  assert.equal(app(source).spec.running,false);assert.equal(app(source).spec.application.replicas,2);
  for(const component of ['deployment/keycloak','statefulset/keycloak-db']){
    k('-n',source,'rollout','restart',component);
    await stopped(source);
    assert.equal(json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items.length,0);
    assert.equal(json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak-db').items.length,0);
  }
  assert(!json('-n',source,'get','jobs').items.some(j=>j.status?.active));
  assert.equal(json('-n',source,'get','ingresses').items.length,0);
  intent(source,{running:true});await ready(source);await oidc();
  const keycloakPods=json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items.map(p=>p.metadata.uid).sort();
  k('-n',source,'rollout','restart','statefulset/keycloak-db');
  k('-n',source,'rollout','status','statefulset/keycloak-db','--timeout=600s');await ready(source);await oidc();
  assert.deepEqual(json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items.map(p=>p.metadata.uid).sort(),keycloakPods);
  intent(source,{'application.replicas':1});await ready(source);
  const session=await oidc();
  const notBefore=sql(source,"SELECT not_before FROM realm WHERE name='recovery-proof'");
  const samePod=json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items[0];
  const restarts=samePod.status.containerStatuses.find(c=>c.name==='keycloak').restartCount;
  k('-n',source,'exec',samePod.metadata.name,'-c','keycloak','--','/bin/bash','-c','kill -TERM 1');
  const restarted=await until('ordinary same-Pod kubelet restart',()=>json('-n',source,'get','pod',samePod.metadata.name),p=>p.status.containerStatuses?.some(c=>c.name==='keycloak'&&c.restartCount>restarts&&c.ready));
  assert.equal(restarted.metadata.uid,samePod.metadata.uid);await ready(source);
  assert((await token({grant_type:'refresh_token',client_id:'recovery-proof',refresh_token:session.tokens.refresh_token})).access_token);
  assert.equal(sql(source,"SELECT not_before FROM realm WHERE name='recovery-proof'"),notBefore);
  const restartJobs=json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort();
  const original=json('-n',source,'get','secret/keycloak-database');
  intent(source,{'application.replicas':0});await keycloakZero(source);
  k('-n',source,'patch','secret/keycloak-database','--type=merge','-p',JSON.stringify({data:{password:Buffer.from('deliberately-wrong-connection-password').toString('base64')}}));
  intent(source,{'application.replicas':1});
  await until('unreadable database startup refusal',()=>json('-n',source,'get','pods','-l','app.kubernetes.io/name=keycloak').items,pods=>pods.some(p=>p.status.containerStatuses?.some(c=>c.restartCount>0)));
  assert(!condition(app(source),'Ready'));
  assert.deepEqual(json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort(),restartJobs);
  assert.equal(sql(source,"SELECT id FROM realm WHERE name='recovery-proof'"),realmID);
  k('-n',source,'patch','secret/keycloak-database','--type=merge','-p',JSON.stringify({data:original.data}));
  await ready(source);await oidc();
  const generation=app(source).metadata.generation;
  for(const value of [-1,1.5]) {
    let failure;try{k('-n',source,'patch','keycloakapplication/keycloak','--type=merge','-p',JSON.stringify({spec:{application:{replicas:value}}}))}catch(e){failure=e}
    assert(failure,'invalid replica intent accepted');assert.equal(app(source).metadata.generation,generation);
  }
  return {replicas:2,nativeScale:true,ordinaryRestartPreservedIdentity:true,samePodRestart:true,ordinaryRefreshPreserved:true,wholeApplicationStop:true,runtimeIntentSurvivedStaticApplyAndControllerRestart:true,stoppedComponentRestartsDidNotWake:true,independentDatabaseRestart:true,keycloakZeroCapture:captured.snapshotID,failedObservationDidNotInitialize:true,invalidReplicaIntentsRejected:true};
}

async function conflictScenario() {
  await activate(source);
  const child=spawn('kubectl',['--kubeconfig',cfg.kubeconfig,'-n',source,'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],{stdio:['pipe','pipe','pipe']});
  let out='';child.stdout.on('data',b=>out+=b);const ended=new Promise(resolve=>child.on('exit',resolve));
  child.stdin.write("SELECT pg_advisory_lock(748920164); SELECT 'held';\n");
  await until('conflicting native operation owns lock',()=>out,s=>s.includes('held'),30);
  let failures;
  try {
    const previous=new Set(json('-n',source,'get','jobs').items.map(j=>j.metadata.uid));
    intent(source,{'backup.schedule':'* * * * *','backup.suspend':false});
    const name='manual-conflict-'+Date.now();k('-n',source,'create','job',name,'--from=cronjob/keycloak-backup');
    await until('manual capture refused',()=>json('-n',source,'get','job',name),j=>j.status?.failed===1,180);
    const scheduled=await until('scheduled capture refused',()=>json('-n',source,'get','jobs').items.find(j=>!previous.has(j.metadata.uid)&&j.metadata.name!==name&&j.metadata.ownerReferences?.some(o=>o.kind==='CronJob')&&j.status?.failed===1),Boolean,180);
    failures=[name,scheduled.metadata.name].map(name=>({name,log:k('-n',source,'logs','job/'+name,'-c','backup').trim()}));
    assert(failures.every(f=>/lock|conflict|operation/i.test(f.log)));
    intent(source,{'backup.suspend':true});
  }finally{child.stdin.end('SELECT pg_advisory_unlock(748920164);\n\\q\n');assert.equal(await ended,0)}
  await ready(source);await oidc();
  intent(source,{running:false,'ingress.enabled':false});await stopped(source);
  return {scheduledAndManualNativeExclusion:failures,applicationUsableAfterConflict:true};
}

async function initialStoppedScenario(){
  const ns='kc-a4-initial-stopped';
  await install(ns,{backup:false,running:false,replicas:2,identity:'kc-a4-initial'});
  await stopped(ns);
  const assertNoExecution=()=>{
    assert.equal(app(ns).spec.running,false);assert.equal(app(ns).spec.application.replicas,2);
    assert(!json('-n',ns,'get','pods').items.some(p=>p.metadata.labels?.['app.kubernetes.io/name']!=='keycloak-controller'));
    assert.equal(json('-n',ns,'get','jobs').items.length,0);
    assert.equal(json('-n',ns,'get','ingresses').items.length,0);
    assert.equal(json('-n',ns,'get','cronjobs').items.length,0);
  };
  assertNoExecution();staticApply(ns);
  k('-n',ns,'rollout','restart','deployment/keycloak-controller');
  k('-n',ns,'rollout','status','deployment/keycloak-controller','--timeout=180s');await stopped(ns);assertNoExecution();
  const uid=app(ns).metadata.uid;
  await assert.rejects(install(ns,{backup:false}),/existing target namespace/);
  assert.equal(app(ns).metadata.uid,uid);assertNoExecution();
  intent(ns,{running:true});await ready(ns);
  assert.equal(json('-n',ns,'get','deployment/keycloak').spec.replicas,2);
  intent(ns,{running:false});await stopped(ns);
  return{initialStopNeverExecuted:true,staticApplyAndControllerRestartPreservedIntent:true,occupiedTargetRejected:true,selectedReplicas:2};
}

const liveConfig=JSON.parse(k('config','view','--minify','-o','json'));
assert.equal(liveConfig.clusters[0].cluster.server,cfg.server,'refusing an unexpected cluster API');
await mkdir(path.join(state,'results'),{recursive:true,mode:0o700});
const scenario=process.argv[2];
const scenarios={source:sourceScenario,fresh:freshScenario,'initial-stopped':initialStoppedScenario,restore:restoreScenario,initializer:initializerScenario,interruption:interruptionScenario,refusal:refusalScenario,subsequent:subsequentScenario,bypass:bypassScenario,stale:staleScenario,ordinary:ordinaryScenario,conflict:conflictScenario,fallback:fallbackScenario,vanilla:vanillaScenario,unsupported:unsupportedScenario};
if(!scenarios[scenario])throw Error('Unknown scenario: '+scenario);
const started=new Date().toISOString();
try {const result=await scenarios[scenario](...process.argv.slice(3));await save('results/'+scenario+'.json',{started,completed:new Date().toISOString(),scenario,result});console.log(JSON.stringify({scenario,status:'passed',result}));}
catch(error){await save('results/'+scenario+'-failure.json',{started,failed:new Date().toISOString(),scenario,error:String(error),stderr:error.stderr?.toString()});throw error;}
