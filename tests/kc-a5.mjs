#!/usr/bin/env node
// Destructive qualification, confined to a fresh disposable KC-A5 lab.
import {readFile, writeFile, mkdir} from 'node:fs/promises';
import {execFileSync, spawn} from 'node:child_process';
import {randomBytes, createHash, createPublicKey, verify} from 'node:crypto';
import assert from 'node:assert/strict';
import path from 'node:path';
const state=path.resolve(process.env.KC_A5_STATE||'.lab/kc-a5');
const load=async name=>JSON.parse(await readFile(path.join(state,name+'.json')));
const cfg=await load('runtime'), secrets=await load('credentials');
const source=cfg.source||'kc-a5-source', base=cfg.base||(cfg.hostname?'https://'+cfg.hostname:'https://kc-a5.dev.fke.systems'), realm='replacement-proof';
const run=(cmd,args,input)=>execFileSync(cmd,args,{input,encoding:'utf8',timeout:650000,maxBuffer:32*1024*1024,env:{...process.env,HELM_CACHE_HOME:path.join(state,'helm-cache')}});
assert(cfg.candidateRelease,'explicit candidate release is required');
assert(['0.3.0','0.8.0'].includes(cfg.baselineRelease),'select the exact supported baseline fixture release');
assert(['26.6.1','26.6.2'].includes(cfg.sourceVersion),'select the actual native source version');
const modernBaseline=cfg.baselineRelease==='0.8.0';
const configFor=ns=>ns===source?cfg.sourceKubeconfig:cfg.kubeconfig;
const k=(...args)=>run('kubectl',['--kubeconfig',configFor(args[0]==='-n'?args[1]:null),...args]);
const json=(...args)=>JSON.parse(k(...args,'-o','json'));
const save=(name,data)=>writeFile(path.join(state,name+'.json'),JSON.stringify(data,null,2)+'\n',{mode:0o600});
const scope=ns=>{assert.match(ns,/^kc-a5-[a-z0-9-]+$/);return ns};
const sql=(ns,q)=>run('kubectl',['--kubeconfig',configFor(ns),'-n',scope(ns),'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],q+'\n').trim();
const app=ns=>json('-n',scope(ns),'get','keycloakapplication/keycloak');
const condition=(a,t)=>a.status?.conditions?.some(c=>c.type===t&&c.status==='True'&&c.observedGeneration===a.metadata.generation);
async function until(label,observe,accept,seconds=600){let last;const end=Date.now()+seconds*1000;while(Date.now()<end){last=await observe();if(accept(last))return last;await new Promise(r=>setTimeout(r,1000))}throw Error(label+' timed out: '+JSON.stringify(last))}
const ready=ns=>until(ns+' Ready',()=>app(ns),a=>condition(a,'Ready'));
const zero=async ns=>{await until(ns+' Keycloak zero',()=>app(ns),a=>a.status?.application?.activePods===0&&condition(a,'Quiescent'));assert.equal(json('-n',ns,'get','statefulset/keycloak-db').status.readyReplicas,1);assert.equal(json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items.length,0);if(ns!==source)assert(!condition(app(ns),'Stopped'))};
const stopped=async ns=>{assert.notEqual(ns,source,'historical source uses zero plus explicit retirement');await until(ns+' whole application stopped',()=>app(ns),a=>condition(a,'Stopped'));for(const component of ['keycloak','keycloak-db'])assert.equal(json('-n',ns,'get','pods','-l','app.kubernetes.io/name='+component).items.length,0);assert(json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak-backup').items.every(p=>['Succeeded','Failed'].includes(p.status.phase)&&!p.metadata.deletionTimestamp))};
// Each historical baseline retains its own released Helm interface.
const installationFile=ns=>path.join(state,scope(ns),'installation.yaml');
function candidateValues(values){
 const {identity,configuration,recoveryAdminSecret,application,...service}=structuredClone(values);
 service.tls={managed:false};
 return{installation:identity.installation,instance:identity.instance,services:{[identity.instance]:{...service,hostname:new URL(configuration.hostname).hostname}}};
}
async function saveInstallation(ns,values){
 await mkdir(path.dirname(installationFile(ns)),{recursive:true,mode:0o700});
 await writeFile(installationFile(ns),JSON.stringify(values,null,2)+'\n',{mode:0o600});
}
function intent(ns,settings){
 const candidate=ns!==source;
 if(candidate){
  const spec={};
  for(const [key,value]of Object.entries(settings)){const parts=key.split('.');if(key==='running'){spec.running=value;continue}assert.equal(parts.length,2);(spec[parts[0]]??={})[parts[1]]=value}
  k('-n',scope(ns),'patch','keycloakapplication/keycloak','--type=merge','-p',JSON.stringify({spec}));return;
 }
 const args=['upgrade','keycloak',cfg.baselineChart,'--kubeconfig',cfg.sourceKubeconfig,'-n',scope(ns),'--reuse-values'];
 for(const [key,value]of Object.entries(settings))args.push('--set',(modernBaseline?'services.identity-provider.':'')+key+'='+value);
 args.push('--wait=legacy','--timeout','10m');run('helm',args);
}
const backupConfig={repository:cfg.repository||'rest:https://backup-store.kc-a5-repository.svc/keycloak/',readerSecret:'keycloak-backup-reader',writerSecret:'keycloak-backup-writer',schedule:'17 3 * * *',timeZone:'Europe/Berlin',suspend:true};
async function install(ns,{backup=false,route=true,chart=cfg.baselineChart,replicas=1,running=true}={}){
 scope(ns);const ca=await readFile(path.join(state,'repository.crt'),'utf8'),candidate=chart===cfg.candidateChart;
 if(candidate)assert(!json('get','namespaces').items.some(n=>n.metadata.name===ns),'occupied target slot');
 const strings=[['keycloak-database',{username:'keycloak',database:'keycloak',password:randomBytes(24).toString('hex')}],['keycloak-database-admin',{username:'postgres',password:randomBytes(24).toString('hex')}],['keycloak-bootstrap',{username:'admin',password:secrets.admin}],['keycloak-backup-reader',{password:secrets.restic,username:'reader',restPassword:secrets.reader,'ca.crt':ca}],['keycloak-backup-writer',{password:secrets.restic,username:'writer',restPassword:secrets.writer,'ca.crt':ca}]];
 if(!candidate&&!modernBaseline)strings.push(['keycloak-recovery',{username:'admin',password:secrets.admin}]);
 const items=[{apiVersion:'v1',kind:'Namespace',metadata:{name:ns,labels:{'kc-a5-lab':'true'}}},...strings.map(([name,stringData])=>({apiVersion:'v1',kind:'Secret',metadata:{name,namespace:ns},stringData}))];
 if(candidate){const tls=await load('tls');items.push({apiVersion:'v1',kind:'Secret',type:'kubernetes.io/tls',metadata:{name:'kc-a5-tls',namespace:ns},data:tls.data})}
 run('kubectl',['--kubeconfig',configFor(ns),'apply','-f','-'],JSON.stringify({apiVersion:'v1',kind:'List',items}));
 const values={identity:{installation:'kc-a5',instance:'identity-provider'},configuration:{hostname:base},application:{replicas:1},database:{secretName:'keycloak-database',adminSecretName:'keycloak-database-admin',size:'6Gi'},bootstrapAdminSecret:'keycloak-bootstrap',ingress:{enabled:route,className:'traefik',tlsSecretName:'kc-a5-tls',maintenance:!route,administratorSourceRanges:[cfg.adminIPv4+'/32'],controllerNamespace:'kube-system',controllerPodLabels:{'app.kubernetes.io/name':'traefik'}}};
 if(backup)values.backup=backupConfig;
 if(!candidate&&!modernBaseline)values.recoveryAdminSecret='keycloak-recovery';
 if(candidate)await saveInstallation(ns,candidateValues(values));
 else if(modernBaseline){const normalized=candidateValues(values);normalized.services[normalized.instance].application={replicas:1};await save(ns+'-values',normalized)}
 else await save(ns+'-values',values);
 if(candidate){
  const rendered=run('helm',['template','keycloak',chart,'-n',ns,'-f',installationFile(ns),'--show-only','templates/application.yaml']);
  const resource=JSON.parse(run('yq',['-o=json','.'],rendered));resource.metadata.namespace=ns;resource.spec.running=running;resource.spec.application??={};resource.spec.application.replicas=replicas;
  run('kubectl',['--kubeconfig',cfg.kubeconfig,'create','-f','-'],JSON.stringify(resource));
 }
 run('helm',['install','keycloak',chart,'--kubeconfig',configFor(ns),'-n',ns,'-f',candidate?installationFile(ns):path.join(state,ns+'-values.json'),'--wait=legacy','--timeout','10m']);
 return candidate&&(replicas===0||!running)?app(ns):ready(ns);
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
function routes(ns){return JSON.parse(run('kubectl',['--kubeconfig',configFor(ns),'get','ingresses','-A','-o','json'])).items.filter(i=>i.spec.rules?.some(r=>r.host===new URL(base).hostname)).map(i=>i.metadata.namespace)}
// Use a genuinely separate network source for denied-access checks. A harness
// running on the VM cannot use that same VM's curl as its denied source.
function externalHTTP(headers=[]) {
 const args=['--silent','--show-error','--output','/dev/null','--write-out','%{http_code}',...headers,base+'/realms/master/.well-known/openid-configuration'];
 return (cfg.sshConfig?run('ssh',['-F',cfg.sshConfig,'-o','BatchMode=yes','k3s','curl '+args.map(a=>"'"+a.replaceAll("'","'\\''")+"'").join(' ')]):k('-n','kc-a5-probe','exec','probe','--','curl',...args)).trim();
}
async function reachable(){await until('public route',async()=>{try{return(await fetch(base+'/realms/master/.well-known/openid-configuration')).status}catch{return 0}},x=>x===200,120)}
async function boundary(ns){await reachable();const discovery=await(await fetch(base+`/realms/${realm}/.well-known/openid-configuration`)).json();assert.equal(discovery.issuer,base+'/realms/'+realm);const proof=externalHTTP();assert.equal(proof,'403');const forged=externalHTTP(['-H','X-Forwarded-For: '+cfg.adminIPv4,'-H','X-Real-IP: '+cfg.adminIPv4,'-H','Forwarded: for='+cfg.adminIPv4+';proto=https']);assert.equal(forged,'403');assert.deepEqual(routes(ns),[ns]);return{allowed:200,denied:proof,forged,issuer:discovery.issuer,routes:routes(ns)}}
async function retireSource(){
 intent(source,{'application.replicas':0,'ingress.enabled':false,'backup.suspend':true});await zero(source);
 k('-n',source,'scale','deployment/keycloak-controller','--replicas=0');
 k('-n',source,'wait','pod','-l','app.kubernetes.io/name=keycloak-controller','--for=delete','--timeout=120s');
 k('-n',source,'scale','statefulset/keycloak-db','--replicas=0');
 k('-n',source,'wait','pod/keycloak-db-0','--for=delete','--timeout=120s');
 assert(!json('-n',source,'get','pods').items.some(p=>p.status?.phase==='Running'));
 assert.deepEqual(routes(source),[]);
 await save('source-retirement',{applicationUID:app(source).metadata.uid,confirmed:true});
}
async function sourceScenario(){
 await install(source,{backup:true});await reachable();
 await save('tls',{data:json('-n',source,'get','secret/kc-a5-tls').data});
 await admin('POST','realms',{realm,enabled:true,ssoSessionIdleTimeout:14400,ssoSessionMaxLifespan:14400});
 await admin('POST',`realms/${realm}/clients`,{clientId:'replacement-proof',enabled:true,publicClient:true,standardFlowEnabled:true,directAccessGrantsEnabled:true,redirectUris:[base+'/proof-callback'],attributes:{'pkce.code.challenge.method':'S256'}});
 await admin('POST',`realms/${realm}/users`,{username:'proof-user',enabled:true,email:'proof@example.invalid',firstName:'Fresh',lastName:'Lab',emailVerified:true,credentials:[{type:'password',value:secrets.user,temporary:false}]});
 const user=(await admin('GET',`realms/${realm}/users?username=proof-user&exact=true`))[0],login=await oidc();
 assert.equal(login.subject,user.id);await save('fixture',{userID:user.id,keyID:login.keyID,realmNotBefore:sql(source,`SELECT not_before FROM realm WHERE name='${realm}'`)});
 await save('pre-replacement-tokens',login.tokens);
 intent(source,{'ingress.maintenance':true});await ready(source);const access=await boundary(source);
 intent(source,{'application.replicas':0});await zero(source);assert.equal(sql(source,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),cfg.sourceVersion);
 const final=await backup(source,'source');await retireSource();
 await save('source-pvc',json('-n',source,'get','pvc/keycloak-db'));
 return{access,snapshot:final.snapshotID,sourceRetired:true,sourceVersion:cfg.sourceVersion};
}
async function fallbackScenario(){
 const target=scope(routes()[0]);assert(target&&target!==source);
 intent(target,{running:false,'ingress.enabled':false});await stopped(target);assert.deepEqual(routes(),[]);
 k('-n',source,'scale','statefulset/keycloak-db','--replicas=1');k('-n',source,'rollout','status','statefulset/keycloak-db','--timeout=180s');
 k('-n',source,'scale','deployment/keycloak-controller','--replicas=1');
 intent(source,{'application.replicas':1,'ingress.enabled':true});await ready(source);
 const access=await boundary(source),login=await oidc();assert.equal(login.subject,(await load('fixture')).userID);
 return{access,boundary:'Explicit consequence-free synthetic fallback only; never automatic after ambiguous writes',targetStopped:true};
}
async function replacementUpgradeScenario(){
 const ns='kc-a5-upgraded-replacement',final=await load('source-backup'),fixture=await load('fixture');
 assert.equal((await load('source-retirement')).confirmed,true,'operator source retirement must precede reconstruction; this scenario never contacts the predecessor');
 await install(ns,{backup:true,route:false,chart:cfg.candidateChart});const a=await ready(ns);
 assert.equal(a.status.operation.snapshot.id,final.snapshotID);assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.2');
 assert.equal(Object.hasOwn(a.spec,'recoveryAdminSecret'),false);assert(!json('-n',ns,'get','secrets').items.some(s=>s.metadata.name==='keycloak-recovery'));
 const pvc=json('-n',ns,'get','pvc/keycloak-db'),oldPVC=await load('source-pvc');assert.notEqual(pvc.metadata.uid,oldPVC.metadata.uid);
 assert.deepEqual(routes(),[]);const before=workload(ns);intent(ns,{'ingress.enabled':true});await ready(ns);assert.deepEqual(workload(ns),before);
 const access=await boundary(ns),login=await oidc();assert.equal(login.subject,fixture.userID);assert.equal(login.keyID,fixture.keyID);
 assert.equal(sql(ns,`SELECT not_before FROM realm WHERE name='${realm}'`),fixture.realmNotBefore);
 assert((await token({grant_type:'refresh_token',client_id:'replacement-proof',refresh_token:(await load('pre-replacement-tokens')).refresh_token})).access_token);
 const images=json('-n',ns,'get','deployments').items.flatMap(d=>[...d.spec.template.spec.containers,...(d.spec.template.spec.initContainers||[])].map(c=>c.image));assert(images.includes(cfg.candidateController));assert(images.includes(cfg.candidateKeycloak));
 assert.equal(json('-n',ns,'get','cronjob/keycloak-backup').spec.jobTemplate.spec.template.spec.containers[0].image,cfg.candidateBackup);
 assert.equal(a.spec.packageRelease,cfg.candidateRelease);assert.equal(a.spec.identity.instance,'identity-provider');assert.equal(a.spec.database.size,'6Gi');assert.equal(a.spec.backup.timeZone,'Europe/Berlin');assert.equal(a.spec.backup.schedule,'17 3 * * *');
 return{sourceVersion:cfg.sourceVersion,targetVersion:'26.6.2',snapshot:final.snapshotID,independentVolume:pvc.spec.volumeName,access,subject:login.subject,recoveryAdministratorCredentialAbsent:true,sessionPreserved:true,images};
}
async function interruptionScenario(){
 assert.equal(cfg.sourceVersion,'26.6.1','migration interruption requires an actual 26.6.1 source backup, not same-version 26.6.2');
 const ns='kc-a5-interrupted';await install(ns,{backup:true,route:false,chart:cfg.candidateChart,replicas:0});
 await until('empty database',()=>app(ns),a=>a.status?.state?.classification==='Absent');
 const lock=async table=>{
  const child=spawn('kubectl',['--kubeconfig',cfg.kubeconfig,'-n',ns,'exec','-i','keycloak-db-0','--','psql','-X','-U','postgres','-d','keycloak','-At','-v','ON_ERROR_STOP=1'],{stdio:['pipe','pipe','pipe']});let output='',errorOutput='';
  child.stdout.on('data',b=>output+=b);child.stderr.on('data',b=>errorOutput+=b);const ended=new Promise(resolve=>child.on('exit',resolve));child.stdin.write(`BEGIN; LOCK TABLE ${table} IN SHARE MODE; SELECT 'held';\n`);
  await until('native lock '+table,()=>output,s=>s.includes('held'),30);
  return async()=>{child.stdin.end('COMMIT;\n\\q\n');assert.equal(await ended,0,errorOutput)};
 };
 let release=await lock('pg_catalog.pg_class');
 intent(ns,{'application.replicas':1});
 const job=await until('restore admitted',()=>json('-n',ns,'get','jobs').items.find(j=>j.metadata.name.startsWith('keycloak-recover-')),Boolean);
 k('-n',ns,'scale','deployment/keycloak-controller','--replicas=0');k('-n',ns,'wait','pod','-l','app.kubernetes.io/name=keycloak-controller','--for=delete','--timeout=120s');
 await release();await until('restore complete while coordinator absent',()=>json('-n',ns,'get','job',job.metadata.name),j=>j.status?.succeeded===1);
 assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.1');
 release=await lock('migration_model');
 try{
  k('-n',ns,'scale','deployment/keycloak-controller','--replicas=1');
  await until('native migration blocked',()=>sql(ns,"SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query ILIKE '%migration_model%'"),n=>Number(n)>0);
  const executing=workload(ns);k('-n',ns,'rollout','restart','deployment/keycloak-controller');k('-n',ns,'rollout','status','deployment/keycloak-controller','--timeout=120s');assert.deepEqual(workload(ns),executing);
  intent(ns,{'application.replicas':0});await until('migration serving process stopped',()=>app(ns),a=>a.status?.application?.activePods===0);
 }finally{await release()}
 assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),'26.6.1');
 intent(ns,{'application.replicas':1});await until('interrupted migration refusal',()=>app(ns),a=>condition(a,'Degraded'));assert.equal(json('-n',ns,'get','deployment/keycloak').spec.replicas,0);assert(!routes().includes(ns));
 intent(ns,{running:false});await stopped(ns);
 return{sourceVersion:'26.6.1',migrationBlocked:true,controllerRestartRetainedExecution:true,wholeStopWon:true,unsafeReplayRefused:true,routingWithheld:true,retry:'Run replacement-upgrade on its independent empty target'};
}
async function bypassScenario(){
 const active=routes();assert.equal(active.length,1);const ns=scope(active[0]),svc=json('-n',ns,'get','service/keycloak');
 assert.equal(svc.spec.type,'ClusterIP');const pod=json('-n',ns,'get','pods','-l','app.kubernetes.io/name=keycloak').items.find(p=>p.status.phase==='Running');assert(pod);
 const control=k('-n','kc-a5-probe','exec','probe','--','curl','--silent','--show-error','--output','/dev/null','--write-out','%{http_code}','--max-time','15',base+'/realms/master/.well-known/openid-configuration').trim();assert.equal(control,'403');
 const attempts=[];for(const target of [svc.spec.clusterIP,pod.status.podIP]){let code=0;try{k('-n','kc-a5-probe','exec','probe','--','curl','--silent','--show-error','--max-time','5',`http://${target}:8080/realms/master/.well-known/openid-configuration`)}catch(error){code=error.status}assert([7,28].includes(code),'backend must refuse or time out under NetworkPolicy');attempts.push({target,exitCode:code})}
 return{namespace:ns,reachableControlHTTP:control,serviceType:svc.spec.type,attempts};
}
async function downgradeScenario(){
 const active=routes();assert.equal(active.length,1);const ns=scope(active[0]);assert.notEqual(ns,source);
 const before={version:sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),users:sql(ns,'SELECT count(*) FROM user_entity'),workload:workload(ns)};
 const target='kc-a5-unsupported-package';assert(!json('get','namespaces').items.some(n=>n.metadata.name===target),'occupied refusal target');
 k('create','namespace',target);const values=await load(source+'-values'),service=modernBaseline?values.services[values.instance]:values;service.ingress.enabled=false;service.application.replicas=0;await save('unsupported-package-values',values);
 let refusal;try{run('helm',['install','keycloak',cfg.baselineChart,'--kubeconfig',cfg.kubeconfig,'-n',target,'--skip-crds','-f',path.join(state,'unsupported-package-values.json'),'--wait=legacy','--timeout','2m'])}catch(error){refusal=String(error.stderr)}
 assert(refusal&&/no matches for kind|resource mapping not found|v1alpha1|unsupported/i.test(refusal),'old package must fail schema admission on a new target, not mutate migrated state');
 assert.equal(json('-n',target,'get','pods').items.length,0);assert.deepEqual(routes(),active);
 assert.equal(sql(ns,'SELECT version FROM migration_model ORDER BY update_time DESC LIMIT 1'),before.version);assert.equal(sql(ns,'SELECT count(*) FROM user_entity'),before.users);assert.deepEqual(workload(ns),before.workload);
 return{oldPackageRefused:true,independentTarget:true,activeDataUnchanged:true,boundary:'API-incompatible old package refused before execution; not an in-place native downgrade exercise'};
}
async function openCandidateScenario(){
 const ns='kc-a5-upgraded-replacement',before=workload(ns);intent(ns,{'ingress.maintenance':false,'backup.suspend':false});await ready(ns);assert.deepEqual(workload(ns),before);await oidc();
 const external=externalHTTP();assert.equal(external,'200');
 await admin('POST',`realms/${realm}/users`,{username:'post-migration-proof',enabled:true});
 const user=(await admin('GET',`realms/${realm}/users?username=post-migration-proof&exact=true`))[0];assert(user?.id);
 const capture=await backup(ns,'candidate');assert.equal(capture.applicationUID,app(ns).metadata.uid);assert.equal(capture.packageRelease,cfg.candidateRelease);
 intent(ns,{running:false,'ingress.enabled':false});await stopped(ns);
 const retry='kc-a5-subsequent';await install(retry,{backup:true,route:false,chart:cfg.candidateChart});assert.equal(app(retry).status.operation.snapshot.id,capture.snapshotID);
 assert.equal(sql(retry,"SELECT id FROM user_entity WHERE username='post-migration-proof'"),user.id);
 intent(retry,{'ingress.enabled':true});await ready(retry);await boundary(retry);assert.equal((await oidc()).subject,(await load('fixture')).userID);
 return{namespace:ns,normalUserHTTP:external,snapshot:capture.snapshotID,backupApplicationUID:capture.applicationUID,routingOnlyUnchanged:true,subsequentRestoredUser:user.id};
}
const scenarios={source:sourceScenario,fallback:fallbackScenario,'replacement-upgrade':replacementUpgradeScenario,interruption:interruptionScenario,bypass:bypassScenario,downgrade:downgradeScenario,open:openCandidateScenario};
const scenario=process.argv[2];assert(scenarios[scenario],'unknown scenario');const actual=JSON.parse(run('kubectl',['--kubeconfig',cfg.kubeconfig,'config','view','--minify','-o','json'])).clusters[0].cluster.server;assert.equal(actual,cfg.apiServer);
if(scenario==='source'||scenario==='fallback'){assert(cfg.sourceKubeconfig&&cfg.sourceAPIServer,'source operations require explicit source access');const sourceActual=JSON.parse(run('kubectl',['--kubeconfig',cfg.sourceKubeconfig,'config','view','--minify','-o','json'])).clusters[0].cluster.server;assert.equal(sourceActual,cfg.sourceAPIServer);assert.notEqual(actual,sourceActual,'v1alpha1 source requires an independent target cluster')}
try{const result=await scenarios[scenario]();await save(scenario+'-result',{time:new Date().toISOString(),scenario,status:'passed',result});console.log(JSON.stringify({scenario,status:'passed',result}))}catch(error){await save(scenario+'-failure',{time:new Date().toISOString(),message:error.message});throw error}
