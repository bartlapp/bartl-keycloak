#!/usr/bin/env node
// Destructive qualification, confined to a fresh disposable KC-A5 lab.
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {readFileSync, writeFileSync} from 'node:fs';
import {execFileSync, spawn} from 'node:child_process';
import {randomBytes, createHash, createPublicKey, verify} from 'node:crypto';
import assert from 'node:assert/strict';
import path from 'node:path';
const state=path.resolve(process.env.KC_A5_STATE||'.lab/kc-a5');
const load=async name=>JSON.parse(await readFile(path.join(state,name+'.json')));
const cfg=await load('runtime'), secrets=await load('credentials');
const source=cfg.source||'kc-a5-source', green='kc-a5-green', base=cfg.base||(cfg.hostname?'https://'+cfg.hostname:'https://kc-a5.dev.fke.systems'), realm='replacement-proof';
const run=(cmd,args,input)=>execFileSync(cmd,args,{input,encoding:'utf8',timeout:650000,maxBuffer:32*1024*1024,env:{...process.env,HELM_CACHE_HOME:path.join(state,'helm-cache')}});
const k=(...args)=>run('kubectl',['--kubeconfig',cfg.kubeconfig,...args]);
const json=(...args)=>JSON.parse(k(...args,'-o','json'));
const save=(name,data)=>writeFile(path.join(state,name+'.json'),JSON.stringify(data,null,2)+'\n',{mode:0o600});
const scope=ns=>{assert.match(ns,/^kc-a5-[a-z0-9-]+$/);return ns};
const sql=(ns,q)=>run('kubectl',['--kubeconfig',cfg.kubeconfig,'-n',scope(ns),'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],q+'\n').trim();
const app=ns=>json('-n',scope(ns),'get','keycloakapplication/keycloak');
const condition=(a,t)=>a.status?.conditions?.some(c=>c.type===t&&c.status==='True'&&c.observedGeneration===a.metadata.generation);
async function until(label,observe,accept,seconds=600){let last;const end=Date.now()+seconds*1000;while(Date.now()<end){last=await observe();if(accept(last))return last;await new Promise(r=>setTimeout(r,1000))}throw Error(label+' timed out: '+JSON.stringify(last))}
const ready=ns=>until(ns+' Ready',()=>app(ns),a=>condition(a,'Ready'));
const stopped=async ns=>{await until(ns+' stopped',()=>app(ns),a=>condition(a,'Stopped')&&condition(a,'Quiescent'));const selector=json('-n',ns,'get','deployment/keycloak').spec.selector.matchLabels;const selected=Object.entries(selector).map(([key,value])=>key+'='+value).join(',');assert.equal(json('-n',ns,'get','pods','-l',selected).items.length,0)};
// Baseline invocations deliberately retain the published pre-0.6 interface.
const installationFile=ns=>path.join(state,scope(ns),'installation.yaml');
function candidateValues(values){
 const {identity,configuration,recoveryAdminSecret,...service}=structuredClone(values);
 service.tls={managed:false};
 return{installation:identity.installation,instance:identity.instance,services:{[identity.instance]:{...service,hostname:new URL(configuration.hostname).hostname}}};
}
async function saveInstallation(ns,values){
 await mkdir(path.dirname(installationFile(ns)),{recursive:true,mode:0o700});
 await writeFile(installationFile(ns),JSON.stringify(values,null,2)+'\n',{mode:0o600});
}
function intent(ns,settings,chart=cfg.baselineChart){
 const candidate=chart===cfg.candidateChart;
 const args=['upgrade','keycloak',chart,'--kubeconfig',cfg.kubeconfig,'-n',scope(ns)];
 if(candidate){
  const values=JSON.parse(readFileSync(installationFile(ns),'utf8')),service=values.services[values.instance];
  for(const [key,value]of Object.entries(settings)){const parts=key.split('.');assert.equal(parts.length,2);service[parts[0]][parts[1]]=value}
  writeFileSync(installationFile(ns),JSON.stringify(values,null,2)+'\n',{mode:0o600});
  args.push('--reset-values','-f',installationFile(ns));
 }else{
  args.push('--reuse-values');
  for(const [key,value]of Object.entries(settings))args.push('--set',key+'='+value);
 }
 args.push('--wait=legacy','--timeout','10m');run('helm',args);
}
const backupConfig={repository:'rest:https://backup-store.kc-a5-repository.svc/keycloak/',readerSecret:'keycloak-backup-reader',writerSecret:'keycloak-backup-writer',schedule:'17 3 * * *',timeZone:'Europe/Berlin',suspend:true};
async function install(ns,{backup=false,route=true,chart=cfg.baselineChart}={}){
 scope(ns);const ca=await readFile(path.join(state,'repository.crt'),'utf8'),candidate=chart===cfg.candidateChart;
 const strings=[['keycloak-database',{username:'keycloak',database:'keycloak',password:randomBytes(24).toString('hex')}],['keycloak-database-admin',{username:'postgres',password:randomBytes(24).toString('hex')}],['keycloak-bootstrap',{username:'admin',password:secrets.admin}],['keycloak-backup-reader',{password:secrets.restic,username:'reader',restPassword:secrets.reader,'ca.crt':ca}],['keycloak-backup-writer',{password:secrets.restic,username:'writer',restPassword:secrets.writer,'ca.crt':ca}]];
 if(!candidate)strings.push(['keycloak-recovery',{username:'admin',password:secrets.admin}]);
 const items=[{apiVersion:'v1',kind:'Namespace',metadata:{name:ns,labels:{'kc-a5-lab':'true'}}},...strings.map(([name,stringData])=>({apiVersion:'v1',kind:'Secret',metadata:{name,namespace:ns},stringData}))];
 if(ns!==source){const tls=json('-n',source,'get','secret/kc-a5-tls');items.push({apiVersion:'v1',kind:'Secret',type:'kubernetes.io/tls',metadata:{name:'kc-a5-tls',namespace:ns},data:tls.data})}
 run('kubectl',['--kubeconfig',cfg.kubeconfig,'apply','-f','-'],JSON.stringify({apiVersion:'v1',kind:'List',items}));
 const values={identity:{installation:'kc-a5',instance:'identity-provider'},configuration:{hostname:base},application:{replicas:1},database:{secretName:'keycloak-database',adminSecretName:'keycloak-database-admin',size:'6Gi'},bootstrapAdminSecret:'keycloak-bootstrap',ingress:{enabled:route,className:'traefik',tlsSecretName:'kc-a5-tls',maintenance:!route,administratorSourceRanges:[cfg.adminIPv4+'/32'],controllerNamespace:'kube-system',controllerPodLabels:{'app.kubernetes.io/name':'traefik'}}};
 if(backup)values.backup=backupConfig;
 if(!candidate)values.recoveryAdminSecret='keycloak-recovery';
 if(candidate)await saveInstallation(ns,candidateValues(values));else await save(ns+'-values',values);
 run('helm',['install','keycloak',chart,'--kubeconfig',cfg.kubeconfig,'-n',ns,'-f',candidate?installationFile(ns):path.join(state,ns+'-values.json'),'--wait=legacy','--timeout','10m']);return ready(ns);
}
async function token(form){const response=await fetch(base+`/realms/${form.realm||realm}/protocol/openid-connect/token`,{method:'POST',body:new URLSearchParams(Object.fromEntries(Object.entries(form).filter(([key])=>key!=='realm')))});const body=await response.json();assert(response.ok,'token HTTP '+response.status+' '+body.error);return body}
async function admin(method,url,body){const t=await token({realm:'master',grant_type:'password',client_id:'admin-cli',username:'admin',password:secrets.admin});const r=await fetch(base+'/admin/'+url,{method,headers:{Authorization:'Bearer '+t.access_token,...(body?{'Content-Type':'application/json'}:{})},body:body?JSON.stringify(body):undefined});assert(r.ok,'admin HTTP '+r.status);const text=await r.text();return text?JSON.parse(text):null}
async function oidc(){
 const verifier=randomBytes(32).toString('base64url'),nonce=randomBytes(16).toString('hex'),s=randomBytes(16).toString('hex'),redirect=base+'/proof-callback';
 const query=new URLSearchParams({client_id:'replacement-proof',response_type:'code',scope:'openid profile',redirect_uri:redirect,code_challenge:createHash('sha256').update(verifier).digest('base64url'),code_challenge_method:'S256',state:s,nonce});const jar=new Map();
 const request=async(url,options={})=>{assert.equal(new URL(url).origin,base);const r=await fetch(url,{...options,redirect:'manual',headers:{...options.headers,Cookie:[...jar].map(([key,v])=>key+'='+v).join('; ')}});for(const line of r.headers.getSetCookie()){const pair=line.split(';')[0],p=pair.indexOf('=');jar.set(pair.slice(0,p),pair.slice(p+1))}return r};
 let r=await request(base+`/realms/${realm}/protocol/openid-connect/auth?`+query);const html=await r.text();const action=html.match(/<form[^>]+action="([^"]+)"/);assert(action,'missing login form');r=await request(action[1].replaceAll('&amp;','&'),{method:'POST',body:new URLSearchParams({username:'proof-user',password:secrets.user,credentialId:''})});assert.equal(r.status,302);
 const callback=new URL(r.headers.get('location'));assert.equal(callback.origin+callback.pathname,redirect);assert.equal(callback.searchParams.get('state'),s);
 const tokens=await token({grant_type:'authorization_code',client_id:'replacement-proof',code:callback.searchParams.get('code'),redirect_uri:redirect,code_verifier:verifier});const [h,p,sig]=tokens.id_token.split('.'),header=JSON.parse(Buffer.from(h,'base64url')),claims=JSON.parse(Buffer.from(p,'base64url'));const keys=await(await fetch(base+`/realms/${realm}/protocol/openid-connect/certs`)).json();assert.equal(header.alg,'RS256');assert(verify('RSA-SHA256',Buffer.from(h+'.'+p),createPublicKey({key:keys.keys.find(key=>key.kid===header.kid),format:'jwk'}),Buffer.from(sig,'base64url')));assert.equal(claims.iss,base+'/realms/'+realm);assert.equal(claims.nonce,nonce);assert.equal(claims.aud,'replacement-proof');assert.equal(claims.preferred_username,'proof-user');return{tokens,subject:claims.sub,keyID:header.kid};
}
async function backup(ns,label){const name='final-'+label+'-'+Date.now();k('-n',ns,'create','job',name,'--from=cronjob/keycloak-backup');await until(name,()=>json('-n',ns,'get','job',name),j=>{if(j.status?.failed)throw Error(k('-n',ns,'logs','job/'+name));return j.status?.succeeded===1});const receipt=JSON.parse(k('-n',ns,'logs','job/'+name,'-c','backup').trim());assert.match(receipt.snapshotID,/^[a-f0-9]{64}$/);await save(label+'-backup',receipt);return receipt}
function workload(ns){const d=json('-n',ns,'get','deployment/keycloak');const pods=json('-n',ns,'get','pods').items.filter(p=>p.metadata.ownerReferences?.some(o=>o.kind==='ReplicaSet')&&!p.metadata.name.startsWith('keycloak-controller'));return{template:d.spec.template,pods:pods.map(p=>({uid:p.metadata.uid,restarts:p.status.containerStatuses?.map(c=>c.restartCount)}))}}
function routes(){return json('get','ingresses','-A').items.filter(i=>i.spec.rules?.some(r=>r.host===new URL(base).hostname)).map(i=>i.metadata.namespace)}
// Use a genuinely separate network source for denied-access checks. A harness
// running on the VM cannot use that same VM's curl as its denied source.
function externalHTTP(headers=[]) {
 const args=['--silent','--show-error','--output','/dev/null','--write-out','%{http_code}',...headers,base+'/realms/master/.well-known/openid-configuration'];
 return (cfg.sshConfig?run('ssh',['-F',cfg.sshConfig,'-o','BatchMode=yes','k3s','curl '+args.map(a=>"'"+a.replaceAll("'","'\\''")+"'").join(' ')]):k('-n','kc-a5-probe','exec','probe','--','curl',...args)).trim();
}
async function reachable(){await until('public route',async()=>{try{return(await fetch(base+'/realms/master/.well-known/openid-configuration')).status}catch{return 0}},x=>x===200,120)}
async function boundary(ns){await reachable();const discovery=await(await fetch(base+`/realms/${realm}/.well-known/openid-configuration`)).json();assert.equal(discovery.issuer,base+'/realms/'+realm);const proof=externalHTTP();assert.equal(proof,'403');const forged=externalHTTP(['-H','X-Forwarded-For: '+cfg.adminIPv4,'-H','X-Real-IP: '+cfg.adminIPv4,'-H','Forwarded: for='+cfg.adminIPv4+';proto=https']);assert.equal(forged,'403');assert.deepEqual(routes(),[ns]);return{allowed:200,denied:proof,forged,issuer:discovery.issuer,routes:routes()}}
async function handover(from,to,chart=cfg.baselineChart){intent(from,{'ingress.enabled':false});await until('old route removed',()=>routes(),r=>r.length===0);k('-n',from,'rollout','restart','deployment/keycloak-controller');k('-n',from,'rollout','status','deployment/keycloak-controller','--timeout=120s');await stopped(from);assert.deepEqual(routes(),[]);intent(to,{'ingress.enabled':true},chart);await ready(to);return boundary(to)}
async function sourceScenario(){await install(source);await reachable();await admin('POST','realms',{realm,enabled:true,ssoSessionIdleTimeout:14400,ssoSessionMaxLifespan:14400});await admin('POST',`realms/${realm}/clients`,{clientId:'replacement-proof',enabled:true,publicClient:true,standardFlowEnabled:true,directAccessGrantsEnabled:true,redirectUris:[base+'/proof-callback'],attributes:{'pkce.code.challenge.method':'S256'}});await admin('POST',`realms/${realm}/users`,{username:'proof-user',enabled:true,email:'proof@example.invalid',firstName:'Fresh',lastName:'Lab',emailVerified:true,credentials:[{type:'password',value:secrets.user,temporary:false}]});const user=(await admin('GET',`realms/${realm}/users?username=proof-user&exact=true`))[0],login=await oidc();assert.equal(login.subject,user.id);await save('fixture',{userID:user.id,keyID:login.keyID});await save('backup-values',{backup:backupConfig});run('helm',['upgrade','keycloak',cfg.baselineChart,'--kubeconfig',cfg.kubeconfig,'-n',source,'--reuse-values','-f',path.join(state,'backup-values.json'),'--wait=legacy']);const before=workload(source);intent(source,{'ingress.maintenance':true});await ready(source);assert.deepEqual(workload(source),before);const access=await boundary(source);intent(source,{'application.replicas':0});await stopped(source);const final=await backup(source,'source');await stopped(source);return{access,snapshot:final.snapshotID,routingOnlyUnchanged:true}}
async function replacementScenario(){const before=await load('source-backup');await install(green,{backup:true,route:false});const a=await ready(green);assert.equal(a.status.operation.snapshot.id,before.snapshotID);const pvcs=[source,green].map(ns=>json('-n',ns,'get','pvc/keycloak-db'));assert.notEqual(pvcs[0].spec.volumeName,pvcs[1].spec.volumeName);assert.notEqual(pvcs[0].metadata.uid,pvcs[1].metadata.uid);const original=workload(green);const access=await handover(source,green);assert.deepEqual(workload(green),original);const fixture=await load('fixture'),login=await oidc();assert.equal(login.subject,fixture.userID);assert.equal(login.keyID,fixture.keyID);await save('replacement-tokens',login.tokens);return{access,snapshot:a.status.operation.snapshot.id,storage:pvcs.map(p=>({uid:p.metadata.uid,pv:p.spec.volumeName})),routingOnlyUnchanged:true,subject:login.subject}}
async function fallbackScenario(){intent(green,{'application.replicas':0,'ingress.enabled':false});await stopped(green);await until('green route gone',()=>routes(),r=>r.length===0);intent(source,{'application.replicas':1,'ingress.enabled':true});await ready(source);const access=await boundary(source),login=await oidc();assert.equal(login.subject,(await load('fixture')).userID);return{access,boundary:'Only disposable login activity on green; no consequential external effects',greenStopped:true}}
async function upgradeScenario(){
 const fixture=await load('fixture'),session=await oidc();await save('pre-upgrade-tokens',session.tokens);
 intent(source,{'ingress.maintenance':true,'backup.suspend':true});await ready(source);await boundary(source);
 intent(source,{'application.replicas':0});await stopped(source);const final=await backup(source,'upgrade');
 const before={pvc:json('-n',source,'get','pvc/keycloak-db').metadata.uid,jobs:json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort(),realmNotBefore:sql(source,`SELECT not_before FROM realm WHERE name='${realm}'`),model:sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),databaseImage:json('-n',source,'get','statefulset/keycloak-db').spec.template.spec.containers[0].image};await save('pre-upgrade-state',before);
 k('-n',source,'delete','secret','keycloak-bootstrap','keycloak-recovery','keycloak-backup-reader','keycloak-backup-writer');
 const settings=candidateValues(await load(source+'-values')),service=settings.services[settings.instance];service.ingress.enabled=true;service.ingress.maintenance=true;service.backup=backupConfig;await saveInstallation(source,settings);
 run('helm',['upgrade','keycloak',cfg.candidateChart,'--kubeconfig',cfg.kubeconfig,'-n',source,'--reset-values','-f',installationFile(source),'--wait=legacy','--timeout','10m']);
 await ready(source);const access=await boundary(source),login=await oidc();assert.equal(login.subject,fixture.userID);assert.equal(login.keyID,fixture.keyID);
 const renewed=await token({grant_type:'refresh_token',client_id:'replacement-proof',refresh_token:session.tokens.refresh_token});assert(renewed.access_token);
 assert.equal(sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.2');assert.equal(sql(source,`SELECT not_before FROM realm WHERE name='${realm}'`),before.realmNotBefore);assert.equal(json('-n',source,'get','pvc/keycloak-db').metadata.uid,before.pvc);assert.deepEqual(json('-n',source,'get','jobs').items.map(j=>j.metadata.uid).sort(),before.jobs);
 const a=app(source);assert.equal(a.spec.packageRelease,cfg.candidateRelease||'0.6.1');assert.equal(a.spec.identity.instance,'identity-provider');assert.equal(a.spec.database.size,'6Gi');assert.equal(a.spec.backup.timeZone,'Europe/Berlin');assert.equal(a.spec.backup.schedule,'17 3 * * *');assert.equal(a.spec.ingress.maintenance,true);assert.equal(a.spec.backup.suspend,true);
 const deployments=json('-n',source,'get','deployments').items,images=deployments.flatMap(d=>[...d.spec.template.spec.containers,...(d.spec.template.spec.initContainers||[])].map(c=>c.image));assert(images.includes(cfg.candidateController));assert(images.includes(cfg.candidateKeycloak));assert.equal(json('-n',source,'get','cronjob/keycloak-backup').spec.jobTemplate.spec.template.spec.containers[0].image,cfg.candidateBackup);assert.equal(json('-n',source,'get','statefulset/keycloak-db').spec.template.spec.containers[0].image,before.databaseImage);
 const original=workload(source);intent(source,{'ingress.maintenance':false},cfg.candidateChart);await ready(source);assert.deepEqual(workload(source),original);await oidc();intent(source,{'ingress.maintenance':true},cfg.candidateChart);await ready(source);assert.deepEqual(workload(source),original);
 return{namespace:source,from:before.model,to:'26.6.2',access,finalSnapshot:final.snapshotID,images,settingsPreserved:true,noRestoreOrSessionReset:true,existingRefreshTokenAccepted:true,routingOnlyUnchanged:true,pvc:before.pvc};
}
async function replacementUpgradeScenario(){
 const ns='kc-a5-upgraded-replacement',final=await load('upgrade-backup');
 await install(ns,{backup:true,route:false,chart:cfg.candidateChart});const a=await ready(ns);assert.equal(a.status.operation.snapshot.id,final.snapshotID);assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.2');
 assert.equal(Object.hasOwn(a.spec,'recoveryAdminSecret'),false);assert(!json('-n',ns,'get','secrets').items.some(s=>s.metadata.name==='keycloak-recovery'));
 const pvc=json('-n',ns,'get','pvc/keycloak-db'),oldPVC=json('-n',source,'get','pvc/keycloak-db');assert.notEqual(pvc.spec.volumeName,oldPVC.spec.volumeName);
 intent(source,{'application.replicas':0,'ingress.enabled':false},cfg.candidateChart);await stopped(source);await until('exclusive handover gap',()=>routes(),r=>r.length===0);
 const before=workload(ns);intent(ns,{'ingress.enabled':true},cfg.candidateChart);await ready(ns);assert.deepEqual(workload(ns),before);const access=await boundary(ns),login=await oidc();assert.equal(login.subject,(await load('fixture')).userID);
 return{sourceVersion:'26.6.1',targetVersion:'26.6.2',snapshot:final.snapshotID,independentVolume:pvc.spec.volumeName,access,subject:login.subject,recoveryAdministratorCredentialAbsent:true};
}
async function interruptionScenario(){
 const ns='kc-a5-interrupted';await install(ns,{backup:true,route:false});intent(ns,{'application.replicas':0});await stopped(ns);
 const fence=spawn('kubectl',['--kubeconfig',cfg.kubeconfig,'-n',ns,'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],{stdio:['pipe','pipe','pipe']});let output='',errorOutput='';fence.stdout.on('data',b=>output+=b);fence.stderr.on('data',b=>errorOutput+=b);const ended=new Promise(resolve=>fence.on('exit',code=>resolve(code)));fence.stdin.write("BEGIN; LOCK TABLE migration_model IN SHARE MODE; SELECT 'held';\n");
 await until('migration fence',()=>output,s=>s.includes('held'),30);
 try{
  await saveInstallation(ns,candidateValues(await load(ns+'-values')));
  run('helm',['upgrade','keycloak',cfg.candidateChart,'--kubeconfig',cfg.kubeconfig,'-n',ns,'--reset-values','-f',installationFile(ns),'--wait=legacy','--timeout','10m']);
  await until('native migration blocked',()=>sql(ns,"SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%migration_model%'"),n=>Number(n)>0);
  const executing=workload(ns);k('-n',ns,'rollout','restart','deployment/keycloak-controller');k('-n',ns,'rollout','status','deployment/keycloak-controller','--timeout=120s');assert.deepEqual(workload(ns),executing);
  intent(ns,{'application.replicas':0},cfg.candidateChart);await until('stop during migration',()=>app(ns),a=>condition(a,'Stopped'));
 }finally{fence.stdin.end('COMMIT;\n\\q\n');assert.equal(await ended,0,errorOutput)}
 assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.1');
 intent(ns,{'application.replicas':1},cfg.candidateChart);await until('interrupted migration refusal',()=>app(ns),a=>condition(a,'Degraded'));assert.equal(json('-n',ns,'get','deployment/keycloak').spec.replicas,0);assert(!routes().includes(ns));
 intent(ns,{'application.replicas':0},cfg.candidateChart);return{sourceVersion:'26.6.1',migrationBlocked:true,controllerRestartRetainedExecution:true,stopWon:true,unsafeReplayRefused:true,routingWithheld:true};
}
async function bypassScenario(){
 const active=routes();assert.equal(active.length,1);const ns=scope(active[0]),svc=json('-n',ns,'get','service/keycloak');
 assert.equal(svc.spec.type,'ClusterIP');const pod=json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items.find(p=>p.status.phase==='Running');assert(pod);
 const control=k('-n','kc-a5-probe','exec','probe','--','curl','--silent','--show-error','--output','/dev/null','--write-out','%{http_code}','--max-time','15',base+'/realms/master/.well-known/openid-configuration').trim();assert.equal(control,'403');
 const attempts=[];for(const target of [svc.spec.clusterIP,pod.status.podIP]){let code=0;try{k('-n','kc-a5-probe','exec','probe','--','curl','--silent','--show-error','--max-time','5',`http://${target}:8080/realms/master/.well-known/openid-configuration`)}catch(error){code=error.status}assert([7,28].includes(code),'backend must refuse or time out under NetworkPolicy');attempts.push({target,exitCode:code})}
 return{namespace:ns,reachableControlHTTP:control,serviceType:svc.spec.type,attempts};
}
async function downgradeScenario(){
 assert.equal(sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.2');const before=sql(source,"SELECT count(*) FROM user_entity");const active=routes();assert(!active.includes(source));
 const values=await load(source+'-values');values.backup=backupConfig;values.ingress.enabled=false;await save('baseline-downgrade-values',values);
 let admissionRefused=false;
 try{run('helm',['upgrade','keycloak',cfg.baselineChart,'--kubeconfig',cfg.kubeconfig,'-n',source,'--reset-values','-f',path.join(state,'baseline-downgrade-values.json'),'--wait=legacy','--timeout','10m'])}
 catch(error){if(!String(error.stderr).includes('.spec.recoveryAdminSecret'))throw error;admissionRefused=true}
 if(admissionRefused)assert.equal(app(source).spec.packageRelease,cfg.candidateRelease);
 else await until('downgrade refusal',()=>app(source),a=>condition(a,'Degraded'));
 assert.equal(json('-n',source,'get','deployment/keycloak').spec.replicas,0);assert.equal(sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.2');assert.equal(sql(source,"SELECT count(*) FROM user_entity"),before);assert.deepEqual(routes(),active);
 return{nativeVersion:'26.6.2',oldPackageRefused:true,admissionRefused,noApplicationProcess:true,dataUnchanged:true};
}
async function restoreSourceScenario(){await install(source,{backup:true,route:false});assert.equal(sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.1');assert.deepEqual(routes(),[]);intent(source,{'ingress.enabled':true});await ready(source);return boundary(source)}
async function openCandidateScenario(){const ns='kc-a5-upgraded-replacement',before=workload(ns);intent(ns,{'ingress.maintenance':false,'backup.suspend':false},cfg.candidateChart);await ready(ns);assert.deepEqual(workload(ns),before);await oidc();const external=externalHTTP();assert.equal(external,'200');const capture=await backup(ns,'candidate');assert.equal(capture.applicationUID,app(ns).metadata.uid);assert.equal(capture.packageRelease,cfg.candidateRelease||'0.6.1');return{namespace:ns,normalUserHTTP:external,snapshot:capture.snapshotID,backupApplicationUID:capture.applicationUID,routingOnlyUnchanged:true}}
const scenarios={source:sourceScenario,replacement:replacementScenario,fallback:fallbackScenario,upgrade:upgradeScenario,'replacement-upgrade':replacementUpgradeScenario,interruption:interruptionScenario,bypass:bypassScenario,downgrade:downgradeScenario,'restore-source':restoreSourceScenario,open:openCandidateScenario};
const scenario=process.argv[2];assert(scenarios[scenario],'unknown scenario');const actual=JSON.parse(run('kubectl',['--kubeconfig',cfg.kubeconfig,'config','view','--minify','-o','json'])).clusters[0].cluster.server;assert.equal(actual,cfg.apiServer);
try{const result=await scenarios[scenario]();await save(scenario+'-result',{time:new Date().toISOString(),scenario,status:'passed',result});console.log(JSON.stringify({scenario,status:'passed',result}))}catch(error){await save(scenario+'-failure',{time:new Date().toISOString(),message:error.message});throw error}
