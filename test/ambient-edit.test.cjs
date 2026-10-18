const {test}=require('node:test');
const assert=require('node:assert/strict');
const vm=require('node:vm');
const fs=require('node:fs');
const path=require('node:path');
const source=fs.readFileSync(path.join(__dirname,'../web/ambient.js'),'utf8');
const svc={cluster:'mesh1',namespace:'app',name:'front',ports:[8080]};
const entry={ref:{cluster:'mesh1',namespace:'app',kind:'HTTPRoute',name:'existing-route'},rule:1,supported:true,reason:'Preserves other rules',request:{...svc,service:'front',port:8080,tasks:['weighted-routing','timeouts'],trafficMode:'weighted',timeout:'7s',backends:[{name:'stable',port:8080,weight:90},{name:'canary',port:8080,weight:10}],edit:{ref:{cluster:'mesh1',namespace:'app',kind:'HTTPRoute',name:'existing-route'},rule:1}}};
function context(entries=[entry]){
  const ctx=vm.createContext({S:{ambient:{result:{editable:entries},settings:{},tasks:{},phase:1}},esc:String,attr:JSON.stringify,amTarget:()=>svc,amService:()=>svc,amCapture:()=>{},amTaskCards:()=>'<div>new-task-cards</div>',$:()=>null});
  vm.runInContext(source.slice(source.indexOf('function amInvalidate('),source.indexOf('function amChoose(')),ctx);
  vm.runInContext(source.slice(source.indexOf('function amSteps('),source.indexOf('function amLanding(')),ctx);
  vm.runInContext(source.slice(source.indexOf('const amTaskText='),source.indexOf('function amTaskCards(')),ctx);
  vm.runInContext(fs.readFileSync(path.join(__dirname,'../web/ambient-configure.js'),'utf8'),ctx);
  vm.runInContext(source.slice(source.indexOf('function amEditors('),source.indexOf('function amWaypoint(')),ctx);
  vm.runInContext(source.slice(source.indexOf('function amRead('),source.indexOf('async function amAction(')),ctx);
  ctx.S.ambient.inventory={services:[svc],namespaces:[]};
  return ctx;
}

test('Service Configure presents existing resources before a closed separate-create section',()=>{
  const ctx=context();const html=ctx.amWizard(svc);
  assert.ok(html.indexOf('existing-route')<html.indexOf('new-task-cards'));
  assert.match(html,/Edit split/);assert.match(html,/Edit timeout/);assert.match(html,/Current configuration/);
  assert.match(html,/<details class="panel am-current-add" id="am-add-config" >/);
  assert.match(context([]).amWizard(svc),/new-task-cards/);
  ctx.S.ambient.createOpen=true;ctx.S.ambient.tasks={'header-routing':true};
  assert.match(ctx.amWizard(svc),/id="am-add-config" open>/,'choosing a task must retain the open picker across renders');
});

test('editing retains original identity, rule and actual values in the plan request',()=>{
  const ctx=context();ctx.S.ambient.plan={id:'old'};ctx.S.ambient.validation={token:'old'};
  ctx.amOpenEdit(entry);const A=ctx.S.ambient;
  assert.equal(A.tab,'wizard');assert.equal(A.phase,2);assert.equal(A.plan,null);assert.equal(A.validation,null);
  A.settings.backends[0].weight=75;A.settings.backends[1].weight=25;A.settings.timeout='9s';
  const req=JSON.parse(JSON.stringify(ctx.amRead()));
  assert.deepEqual(req.edit,entry.request.edit);assert.deepEqual(req.tasks,['weighted-routing','timeouts']);
  assert.equal(req.timeout,'9s');assert.equal(req.backends[0].weight,75);
  assert.equal(entry.request.backends[0].weight,90,'drafts must not overwrite captured defaults');
  A.edit.ref.name='changed-draft';ctx.amOpenEdit(entry);
  assert.equal(A.edit.ref.name,'existing-route');assert.equal(A.settings.backends[0].weight,90);
});

test('unsupported and multiple existing configurations stay explicit',()=>{
  const unsupported={ref:{...entry.ref,name:'complex-route'},rule:0,supported:false,reason:'Unsupported matches'};
  const ctx=context([entry,unsupported]);const html=ctx.amWizard(svc);
  assert.match(html,/data-am-edit-config="0"/);
  assert.doesNotMatch(html,/data-am-edit-config="1"/);
  assert.match(html,/Unsupported matches/);
  const before=ctx.S.ambient.phase;ctx.amOpenEdit(unsupported);assert.equal(ctx.S.ambient.phase,before);
});

test('changing the target through an Ambient link discards the previous plan and edit',()=>{
  const app=fs.readFileSync(path.join(__dirname,'../web/app.js'),'utf8');
  const ctx=context();ctx.location={hash:'#ambient?tab=wizard&kind=Service&cluster=mesh1&namespace=app&name=other'};
  ctx.URLSearchParams=URLSearchParams;
  vm.runInContext(app.slice(app.indexOf('function parseHash('),app.indexOf('function nav(')),ctx);
  ctx.amOpenEdit(entry);ctx.S.ambient.plan={id:'previous-service'};ctx.S.ambient.validation={token:'old'};
  // Use the actual draft reset, including clearing the existing-resource target.
  vm.runInContext(source.slice(source.indexOf('function amInvalidate('),source.indexOf('function amChoose(')),ctx);
  ctx.$=()=>null;ctx.parseHash();
  assert.equal(ctx.S.ambient.key,JSON.stringify(['mesh1','app','other']));
  assert.equal(ctx.S.ambient.phase,1);assert.equal(ctx.S.ambient.edit,null);assert.equal(ctx.S.ambient.plan,null);assert.equal(ctx.S.ambient.validation,null);
  ctx.S.ambient.plan={id:'same-service'};ctx.parseHash();assert.equal(ctx.S.ambient.plan.id,'same-service','same-target links retain the draft');
  ctx.location.hash='#ambient?service=front&cluster=mesh1&namespace=app&route=existing-route';ctx.parseHash();
  assert.equal(ctx.S.ambient.key,JSON.stringify(['mesh1','app','front']));assert.equal(ctx.S.ambient.route,'existing-route');assert.equal(ctx.S.ambient.plan,null);
});

test('all Configure workflows have the same breadcrumb in options and review',()=>{
  const ctx=context();const A=ctx.S.ambient;
  for(const [mode,label] of [['waypoint','Waypoint setup'],['enrol-namespace','Ambient enrolment'],['endpoints','Endpoint recovery'],['route-parent','Route parent repair']]){
    A.mode=mode;
    for(const phase of [2,3]){A.phase=phase;const html=ctx.amConfigurationNav(svc);assert.match(html,/data-amconfig-home/);assert.ok(html.includes(label));assert.match(html,new RegExp(`aria-current="page">${phase===3?'Review':'Configure'}`));}
  }
  A.mode='';
  for(const task of ['weighted-routing','timeouts','methods','jwt','rate-limit','global-service','explain']){
    A.tasks={[task]:true};const html=ctx.amConfigurationNav(svc);assert.match(html,/All configurations/);assert.ok(!html.includes('undefined'));
  }
});

test('returning to all configurations and switching rules preserves separate drafts',()=>{
  const second=JSON.parse(JSON.stringify(entry));second.rule=0;second.request.edit.rule=0;second.request.backends[0].weight=50;second.request.backends[1].weight=50;
  const ctx=context([entry,second]);const A=ctx.S.ambient;
  ctx.amOpenEdit(entry);A.settings.backends[0].weight=75;A.settings.backends[1].weight=25;A.plan={id:'old-plan'};A.validation={token:'old'};
  ctx.amShowConfigurations();
  assert.equal(A.edit,null);assert.equal(A.phase,1);assert.equal(A.plan,null);assert.equal(A.validation,null);assert.equal(A.key,JSON.stringify(['mesh1','app','front']));
  const html=ctx.amWizard(svc);assert.match(html,/existing-route/);assert.match(html,/90%/);assert.match(html,/50%/);
  ctx.amOpenEdit(second);assert.equal(A.edit.rule,0);assert.equal(A.settings.backends[0].weight,50);
  A.settings.backends[0].weight=60;A.settings.backends[1].weight=40;
  ctx.amOpenEdit(entry);assert.equal(A.edit.rule,1);assert.equal(A.settings.backends[0].weight,75);assert.equal(A.settings.backends[1].weight,25);
  assert.equal(entry.request.backends[0].weight,90,'captured values remain separate from drafts');
  ctx.amOpenEdit(second);assert.equal(A.settings.backends[0].weight,60);
});

test('new tasks and setup/repair drafts can resume from the shared configuration list',()=>{
  for(const mode of ['', 'waypoint','endpoints','route-parent','enrol-namespace']){
    const ctx=context();const A=ctx.S.ambient;A.mode=mode;A.phase=2;A.tasks=mode?{}:{jwt:true};A.settings={issuer:'https://issuer.example',wpName:'shared',replicas:3};
    ctx.amShowConfigurations();assert.equal(A.mode,'');assert.equal(A.phase,1);assert.match(ctx.amConfigurationNav(svc),/data-amresume-task/);
    ctx.amResumeTaskDraft();assert.equal(A.mode,mode);assert.equal(A.phase,2);assert.equal(A.settings.wpName,'shared');assert.equal(A.settings.issuer,'https://issuer.example');
    ctx.amResetDraft();assert.equal(A.taskDraft,null);assert.equal(Object.keys(A.editDrafts).length,0,'changing targets clears saved drafts');
  }
});
