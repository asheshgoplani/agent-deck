import{render as Ua,html as ja}from"htm/preact";import{html as Ga}from"htm/preact";import{useEffect as Xs}from"preact/hooks";import{html as z}from"htm/preact";import{useEffect as Dt}from"preact/hooks";import{html as vt}from"htm/preact";import{html as $e}from"htm/preact";function bt(){return $e`
    <svg width="28" height="18" viewBox="0 0 120 80" aria-hidden="true">
      <rect fill="#1a1b26" width="120" height="80" rx="12" stroke="var(--border-hi)" stroke-width="1"/>
      <line x1="40" y1="8" x2="40" y2="72" stroke="#414868" stroke-width="1.5"/>
      <line x1="80" y1="8" x2="80" y2="72" stroke="#414868" stroke-width="1.5"/>
      <circle cx="20" cy="40" r="11" fill="var(--tn-green)"/>
      <circle cx="60" cy="40" r="11" fill="var(--tn-yellow)"/>
      <circle cx="100" cy="40" r="11" fill="var(--tn-muted-2)"/>
    </svg>
  `}function S({d:t,size:s=14}){return $e`
    <svg width=${s} height=${s} viewBox="0 0 24 24" fill="none" stroke="currentColor"
         stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
      <path d=${t}/>
    </svg>
  `}var C={play:"M6 4l14 8-14 8V4z",stop:"M6 6h12v12H6z",restart:"M4 4v5h5 M20 20v-5h-5 M20 9a8 8 0 00-14-3 M4 15a8 8 0 0014 3",fork:"M6 3v6a3 3 0 003 3h6a3 3 0 013 3v3 M6 3v0 M6 21v-6 M18 21v0 M6 21v0 M6 9v0",trash:"M3 6h18 M8 6V4a2 2 0 012-2h4a2 2 0 012 2v2 M6 6v14a2 2 0 002 2h8a2 2 0 002-2V6",plus:"M12 5v14 M5 12h14",filter:"M3 5h18 M6 12h12 M10 19h4",search:"M11 2a9 9 0 100 18 9 9 0 000-18z M22 22l-5-5",settings:"M12 8a4 4 0 100 8 4 4 0 000-8z M12 2v2 M12 20v2 M4.93 4.93l1.41 1.41 M17.66 17.66l1.41 1.41 M2 12h2 M20 12h2 M4.93 19.07l1.41-1.41 M17.66 6.34l1.41-1.41",chev:"M6 9l6 6 6-6",chevR:"M9 6l6 6-6 6",x:"M6 6l12 12 M6 18L18 6",zap:"M13 2L3 14h8l-1 8 10-12h-8l1-8z",wifi:"M5 12a10 10 0 0114 0 M8.5 15.5a5 5 0 017 0 M12 19h.01",send:"M22 2L11 13 M22 2l-7 20-4-9-9-4 20-7z",book:"M4 4h12a4 4 0 014 4v12H8a4 4 0 01-4-4z M4 4v16",term:"M4 4h16v16H4z M8 9l3 3-3 3 M13 15h4",edit:"M12 20h9 M16.5 3.5a2.121 2.121 0 013 3L7 19l-4 1 1-4 12.5-12.5z"};function Kt({status:t,size:s=7}){return $e`<span class=${`dot ${t||"idle"}`} style=${{width:s+"px",height:s+"px"}}/>`}function ze(t){return t==="conductor"?"\u25C6":t==="watcher"?"\u25C7":"\u203A"}import{computed as mn}from"@preact/signals";import{signal as k}from"@preact/signals";import{html as yt}from"htm/preact";var on=0,ln=50,rn=5e3,cn="agentdeck_toast_history";function be(t){if(!t)return;let s=[...kt.value,t].slice(-ln);kt.value=s;try{localStorage.setItem(cn,JSON.stringify(s))}catch{}}function I(t,s){let e=s||"error",n={id:++on,message:t,type:e,createdAt:Date.now()},o=[...dt.value,n];if(o.length>3){let a=o.findIndex(i=>i.type!=="error");if(a>=0){let[i]=o.splice(a,1);be(i)}else{let i=o.shift();be(i)}}dt.value=o,n.type!=="error"&&setTimeout(()=>_e(n.id),rn)}function _e(t){let s=dt.value.find(e=>e.id===t);s&&be(s),dt.value=dt.value.filter(e=>e.id!==t)}function Fe({id:t,message:s,type:e}){let n=e==="error"?"var(--tn-red)":e==="info"?"var(--accent)":"var(--tn-green)";return yt`
    <div class="toast" data-testid="toast" style=${{borderColor:n,position:"relative",pointerEvents:"auto"}}>
      <span class="t" style=${{color:n}}>${e==="error"?"\u2715":e==="info"?"\u2139":"\u2713"}</span>
      <span style="margin-left: 6px;">${s}</span>
      <button type="button"
        onClick=${()=>_e(t)}
        aria-label="Dismiss"
        data-testid="toast-dismiss"
        style="background: transparent; border: 0; color: var(--muted); cursor: pointer;
               margin-left: 10px; padding: 0 4px; font-size: 12px;">✕</button>
    </div>
  `}function Ge(){let t=dt.value;if(t.length===0)return null;let s=t.filter(n=>n.type==="error"),e=t.filter(n=>n.type!=="error");return yt`
    <div style=${{position:"fixed",bottom:"40px",right:"14px",zIndex:70,display:"flex",flexDirection:"column",gap:"6px",pointerEvents:"none",maxWidth:"420px"}}>
      ${s.length>0&&yt`
        <div role="alert" aria-live="assertive" style=${{display:"flex",flexDirection:"column",gap:"6px"}}>
          ${s.map(n=>yt`<${Fe} key=${n.id} ...${n}/>`)}
        </div>
      `}
      ${e.length>0&&yt`
        <div role="status" aria-live="polite" style=${{display:"flex",flexDirection:"column",gap:"6px"}}>
          ${e.map(n=>yt`<${Fe} key=${n.id} ...${n}/>`)}
        </div>
      `}
    </div>
  `}async function y(t,s,e){let n={"Content-Type":"application/json",Accept:"application/json"},o=K.value;o&&(n.Authorization="Bearer "+o);let a;try{a=await fetch(s,{method:t,headers:n,body:e?JSON.stringify(e):void 0})}catch(l){let d="Network error: "+(l.message||"request failed");throw I(d),new Error(d)}let i=await a.json();if(!a.ok){let l=i?.error?.message||a.statusText;throw t!=="GET"&&I(l),new Error(l)}return i}var ut=k([]),Bt=k([]),T=k(null),B=k("connecting"),no=k(localStorage.getItem("theme")||"system"),ye=k(null),K=k(""),We=k({});function dn(){try{let t=localStorage.getItem("agentdeck.sidebarOpen");if(t==="true")return!0;if(t==="false")return!1}catch{}return typeof window<"u"&&window.innerWidth>=768}var ao=k(dn()),Ue=200,je=480,He=280;function un(t){return Number.isFinite(t)?t<Ue?Ue:t>je?je:Math.round(t):He}function pn(){try{let t=localStorage.getItem("sidebar-width");if(t!=null){let s=parseInt(t,10);return un(s)}}catch{}return He}var oo=k(pn());var io=k(null),j=k(!1),H=k(null),xt=k(null),Nt=k(null),st=k("disconnected"),qt=k(!1),lo=k(null),ro=k(!1),co=k(!1),uo=k(""),ot=k(!1),po=k(""),vo=k(!1),dt=k([]),nt=k(!1);function vn(){try{let t=localStorage.getItem("agentdeck_toast_history");if(t){let s=JSON.parse(t);if(Array.isArray(s))return s.slice(-50)}}catch{}return[]}var kt=k(vn()),pt=k(!1),D=k(!0),Ke=k(!1),Be=k([]),Vt=k(!1),qe=k([]),wt=k([]),ke=k(!1),Jt=k(null),Yt=k(null),Q=k(null),St=k(null),Ct=k(null),at=k([]);function Ve(t){let s=[t,...at.value].slice(0,8);at.value=s}function Tt(t,s){at.value=at.value.map(e=>e.correlationId===t?{...e,...s}:e)}async function Xt(){try{let t=await y("GET","/api/sessions/archived");Bt.value=t.sessions||[]}catch{Bt.value=[]}}function fn(t){return!t||!t.tool?"agent":t.groupPath==="conductor"||/conductor/i.test(t.title||"")?"conductor":["webhook","ntfy","slack-watcher"].includes(t.tool)?"watcher":"agent"}function gn(t){let s=t.session||{},e=s.id||"",n=s.groupPath||"";return{id:e,kind:fn(s),title:s.title||e,group:n,tool:s.tool||"",modelId:s.modelId||"",model:s.model||"",modelVersion:s.modelVersion||"",canFork:!!s.canFork,status:s.status||"idle",branch:s.branch||"\u2014",path:s.projectPath||"",cost:0,tokens:0,mcps:[],skills:[],children:[],worktree:!!(s.worktreeBranch&&s.worktreeRepoRoot),worktreeBranch:s.worktreeBranch||"",sandbox:!1,parent:null,pendingNeeds:0,watcherType:null,routes:"",events1h:0,meta:"",raw:s}}function hn(t){let s=t.group||{};return{path:s.path||"",label:(s.name||s.path||"").toUpperCase(),expanded:!!s.expanded,sessionCount:s.sessionCount||0,order:s.order||0,kind:s.path==="conductor"?"conductor":s.path==="watchers"?"watcher":null}}var L=mn(()=>{let t=ut.value||[],s=We.value||{},e=[],n=[];for(let i of t)if(i){if(i.type==="group")e.push(hn(i));else if(i.type==="session"){let l=gn(i),d=s[l.id];typeof d=="number"&&(l.cost=d),n.push(l)}}let o=new Set(e.map(i=>i.path));for(let i of n)i.group&&!o.has(i.group)&&(e.push({path:i.group,label:i.group.toUpperCase(),expanded:!0,sessionCount:0,order:999,kind:null}),o.add(i.group));e.sort((i,l)=>i.order-l.order);let a={};for(let i of n)(a[i.group]||=[]).push(i);return{groups:e,sessions:n,byGroup:a}});import{signal as Z,effect as Je}from"@preact/signals";function It(t,s){try{let e=localStorage.getItem(t);return e==null?s:JSON.parse(e)}catch{return s}}function Ot(t,s){Je(()=>{try{localStorage.setItem(s,JSON.stringify(t.value))}catch{}})}var M=Z(It("agentdeck.tab","fleet"));Ot(M,"agentdeck.tab");var it=Z(!1),tt=Z(!1),At=Z(It("agentdeck.accent","blue"));Ot(At,"agentdeck.accent");var Et=Z(It("agentdeck.density","balanced"));Ot(Et,"agentdeck.density");var q=Z(It("agentdeck.rail","visible"));Ot(q,"agentdeck.rail");var Qt=Z(It("agentdeck.rightRailPanels",{overview:!0,usage:!0,mcps:!0,skills:!0,children:!0,events:!0}));Ot(Qt,"agentdeck.rightRailPanels");var Zt=Z([]),bo=Z("fleet"),te=Z(It("agentdeck.showCols",{tool:!0,cost:!0,branch:!1,attach:!1,sandbox:!1,lastSeen:!1}));Ot(te,"agentdeck.showCols");var lt=Z("");Je(()=>{typeof document>"u"||(document.documentElement.dataset.accent=At.value,document.documentElement.dataset.density=Et.value,document.documentElement.dataset.rail=q.value,document.body.dataset.accent=At.value,document.body.dataset.density=Et.value,document.body.dataset.rail=q.value)});import{html as Lt}from"htm/preact";function $n(t){if(!t)return"";try{return new Date(t).toLocaleTimeString([],{hour:"2-digit",minute:"2-digit",second:"2-digit"})}catch{return""}}function Ye(){let t=kt.value.length;return Lt`
    <button type="button"
      class=${`icon-btn ${pt.value?"active":""}`}
      onClick=${()=>{pt.value=!pt.value}}
      aria-label=${"Toast history ("+t+" entries)"}
      aria-expanded=${pt.value}
      title="Toast history"
      data-testid="toast-history-toggle"
      style="position: relative;">
      <svg width="14" height="14" fill="none" stroke="currentColor" stroke-width="2"
           stroke-linecap="round" stroke-linejoin="round" viewBox="0 0 24 24">
        <path d="M12 8v4l3 3"/>
        <circle cx="12" cy="12" r="9"/>
      </svg>
      ${t>0&&Lt`<span class="pip" style="background: var(--accent); box-shadow: 0 0 6px var(--accent);"/>`}
    </button>
  `}function Xe(){if(!pt.value)return null;let t=kt.value,s=()=>{pt.value=!1};return Lt`
    <div class="overlay" role="dialog" aria-modal="true" aria-label="Toast history"
         data-testid="toast-history-drawer"
         style="justify-content: flex-end; padding: 0;"
         onClick=${e=>{e.target===e.currentTarget&&s()}}>
      <div class="dialog" style="width: 420px; max-width: 100vw; height: 100vh; max-height: 100vh; border-radius: 0; border-right: 0;"
           onClick=${e=>e.stopPropagation()}>
        <div class="dh">
          <span class="kicker">HISTORY</span>
          <div class="t">Toast history</div>
          <button type="button" class="icon-btn" onClick=${s} aria-label="Close toast history">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db" style="padding: 0;">
          ${t.length===0&&Lt`
            <div style="padding: 20px; font-family: var(--mono); font-size: 12px; color: var(--muted); text-align: center;">
              No dismissed toasts yet.
            </div>
          `}
          ${t.slice().reverse().map(e=>Lt`
            <div key=${e.id}
                 data-testid="toast-history-entry"
                 style=${{padding:"10px 14px",borderBottom:"1px solid var(--border)",background:e.type==="error"?"rgba(247,118,142,0.06)":"transparent"}}>
              <div style=${{fontFamily:"var(--mono)",fontSize:"10px",color:e.type==="error"?"var(--tn-red)":"var(--muted)",letterSpacing:"0.06em",marginBottom:"4px"}}>
                ${$n(e.createdAt)} · ${e.type}
              </div>
              <div style=${{fontSize:"12.5px",color:e.type==="error"?"var(--tn-red)":"var(--text)"}}>${e.message}</div>
            </div>
          `)}
        </div>
      </div>
    </div>
  `}var bn=[{id:"command-center",label:"Command Center"},{id:"genui",label:"GenUI"},{id:"fleet",label:"Fleet"},{id:"terminal",label:"Terminal"},{id:"mcp",label:"MCPs"},{id:"skills",label:"Skills"},{id:"conductor",label:"Conductor"},{id:"watchers",label:"Watchers"},{id:"costs",label:"Costs"},{id:"search",label:"Search"},{id:"archived",label:"Archived"}];function Qe(){let t=M.value,s=B.value,e=q.value,{sessions:n}=L.value,o=n.filter(c=>c.status==="waiting"||c.status==="error").length,a=n.reduce((c,h)=>c+(h.pendingNeeds||0),0),i=Q.value,l=i&&Array.isArray(i.decisionsWaiting)?i.decisionsWaiting.length:0,d=s==="connected"?"":"off",r=s==="connected"?{}:{background:"var(--tn-red)",boxShadow:"0 0 6px var(--tn-red)"};return vt`
    <header class="topbar">
      <div class="top-brand">
        <${bt}/>
        <div class="brand-text">agent-deck<span class="dim">web</span></div>
      </div>
      <div class="top-mid">
        <button class="top-search" onClick=${()=>it.value=!0}>
          <${S} d=${C.search} size=${13}/>
          <input readonly placeholder="Jump to session, search conversations, run command…"/>
          <span class="kbd">⌘K</span>
        </button>
        <div class="top-tabs">
          ${bn.map(c=>vt`
            <button key=${c.id}
              class=${`top-tab ${t===c.id?"active":""}`}
              onClick=${()=>M.value=c.id}>
              ${c.label}
              ${c.id==="conductor"&&a>0&&vt`<span class="badge">${a}</span>`}
              ${c.id==="fleet"&&o>0&&vt`<span class="badge">${o}</span>`}
              ${c.id==="command-center"&&l>0&&vt`<span class="badge">${l}</span>`}
            </button>
          `)}
        </div>
      </div>
      <div class="top-right">
        <div class=${`conn-pill ${d}`}>
          <span class="dot" style=${r}/>ws · ${s==="connected"?"live":s}
        </div>
        ${(()=>{let c=Jt.value,h=c&&Array.isArray(c.profiles)?c.profiles:null;if(!h||h.length===0)return null;let v=lt.value||c.current||h[0];return vt`
            <span class="icon-btn"
              style=${{width:"auto",padding:"0 8px",fontFamily:"var(--mono)",fontSize:"11px",cursor:"default"}}
              title="Active profile (bound at startup; not switchable from the web UI)">
              ${v}
            </span>
          `})()}
        <${Ye}/>
        <button
          class=${`icon-btn ${e==="visible"?"active":""}`}
          onClick=${()=>q.value=e==="visible"?"hidden":"visible"}
          title=${e==="visible"?"Hide right rail (])":"Show right rail (])"}
          aria-label="Toggle right rail">
          <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor"
               stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
            <rect x="3" y="3" width="18" height="18" rx="2"/>
            <line x1="15" y1="3" x2="15" y2="21"/>
            ${e==="visible"&&vt`<line x1="18" y1="8" x2="18" y2="16" opacity="0.5"/>`}
          </svg>
        </button>
        <button class="icon-btn" onClick=${()=>tt.value=!tt.value} title="Tweaks" aria-label="Tweaks">
          <${S} d=${C.settings}/>
        </button>
      </div>
    </header>
  `}import{html as N}from"htm/preact";import{useState as ee,useMemo as yn}from"preact/hooks";var kn=[{id:"running",sym:"\u25CF"},{id:"waiting",sym:"\u25D0"},{id:"error",sym:"\u2715"},{id:"idle",sym:"\u25CB"}],xn=[{id:"tool",label:"Tool badge"},{id:"cost",label:"Cost"},{id:"branch",label:"Git branch"},{id:"attach",label:"MCPs / skills"},{id:"sandbox",label:"Docker / worktree"},{id:"lastSeen",label:"Last activity"}];function rt(t,s){if(!D.value){I("mutations disabled");return}let e=s.id;if(t==="start")return y("POST",`/api/sessions/${e}/start`).catch(()=>{});if(t==="stop")return y("POST",`/api/sessions/${e}/stop`).catch(()=>{});if(t==="restart")return y("POST",`/api/sessions/${e}/restart`).catch(()=>{});if(t==="fork")return y("POST",`/api/sessions/${e}/fork`,{title:s.title+"-fork"}).catch(()=>{});if(t==="archive"&&(H.value={message:`Archive session "${s.title}"? The process will be stopped and hidden from the active list.`,onConfirm:()=>y("POST",`/api/sessions/${e}/archive`).then(()=>{T.value===e&&(T.value=null,window.location.pathname.startsWith("/s/")&&history.replaceState(null,"","/"))}).catch(()=>{})}),t==="delete"&&(H.value={message:`Delete session "${s.title}"? This stops the tmux session and removes metadata.`,onConfirm:()=>y("DELETE",`/api/sessions/${e}`).catch(()=>{})}),t==="worktreeFinish"){let n=s.worktreeBranch||s.branch;H.value={message:`Finish worktree for "${s.title}"? Merges branch "${n}" into default branch, removes worktree, deletes branch, and removes session.`,onConfirm:()=>y("POST",`/api/sessions/${e}/worktree/finish`).catch(()=>{})}}t==="edit"&&(Nt.value={sessionId:e})}function wn({s:t,sel:s,onSelect:e,showCols:n}){let[o,a]=ee(!1),i=(t.mcps||[]).length,l=(t.skills||[]).length,d=n.branch&&t.branch&&t.branch!=="\u2014"||n.attach&&(i>0||l>0)||n.sandbox&&(t.sandbox||t.worktree)||n.lastSeen;return N`
    <div class=${`sess ${s?"sel":""} ${t.kind} ${o?"exp":""}`} onClick=${()=>e(t.id)}>
      <span class="sig">${ze(t.kind)}</span>
      <div class="titleline">
        <${Kt} status=${t.status}/>
        <span class="tt">${t.title}</span>
      </div>
      <div class="meta">
        ${n.tool&&t.tool&&N`<span class="tag">${t.tool}</span>`}
        ${n.cost&&t.cost>0&&N`<span class="cost">$${t.cost.toFixed(2)}</span>`}
        <button class="row-chev" title="Details" onClick=${r=>{r.stopPropagation(),a(c=>!c)}}>
          ${o?"\u25BE":"\u25B8"}
        </button>
      </div>
      ${d&&N`
        <div class="subline">
          ${n.branch&&t.branch&&t.branch!=="\u2014"&&N`<span class="trunc"><span class="b">git</span> ${t.branch}</span>`}
          ${n.attach&&i>0&&N`<span class="att-count">${i} mcp${i>1?"s":""}</span>`}
          ${n.attach&&l>0&&N`<span class="att-count skill">${l} skill${l>1?"s":""}</span>`}
          ${n.sandbox&&t.sandbox&&N`<span class="att-count warn">docker</span>`}
          ${n.sandbox&&t.worktree&&N`<span class="att-count">worktree</span>`}
        </div>
      `}
      ${o&&N`
        <div class="row-detail" onClick=${r=>r.stopPropagation()}>
          <div class="rd-row"><span class="rd-k">tool</span><span class="rd-v">${t.tool||"\u2014"}</span></div>
          ${t.branch&&t.branch!=="\u2014"&&N`<div class="rd-row"><span class="rd-k">branch</span><span class="rd-v">${t.branch}</span></div>`}
          ${t.path&&N`<div class="rd-row"><span class="rd-k">path</span><span class="rd-v" title=${t.path}>${t.path}</span></div>`}
          ${t.cost>0&&N`<div class="rd-row"><span class="rd-k">cost</span><span class="rd-v ok">$${t.cost.toFixed(2)}</span></div>`}
        </div>
      `}
      <div class="actions" onClick=${r=>r.stopPropagation()}>
        ${t.status==="running"||t.status==="waiting"?N`<button class="mini" title="Stop" data-testid="session-stop-btn" onClick=${()=>rt("stop",t)}><${S} d=${C.stop} size=${12}/></button>`:N`<button class="mini good" title="Start" data-testid="session-start-btn" onClick=${()=>rt("start",t)}><${S} d=${C.play} size=${12}/></button>`}
        <button class="mini good" title="Restart" data-testid="session-restart-btn" onClick=${()=>rt("restart",t)}><${S} d=${C.restart} size=${12}/></button>
        <button class="mini" title="Edit" data-testid="edit-session-btn" onClick=${()=>rt("edit",t)}><${S} d=${C.edit} size=${12}/></button>
        ${t.canFork&&N`<button class="mini fork" title="Fork" data-testid="session-fork-btn" onClick=${()=>rt("fork",t)}><${S} d=${C.fork} size=${12}/></button>`}
        ${t.worktree&&N`<button class="mini" title="Finish worktree (merge + cleanup)" onClick=${()=>rt("worktreeFinish",t)} data-action="worktree-finish" data-testid="session-worktree-finish-btn">⎇✓</button>`}
        <button class="mini" title="Archive" onClick=${()=>rt("archive",t)}>⌂</button>
        <button class="mini danger" title="Delete" data-testid="session-delete-btn" onClick=${()=>rt("delete",t)}><${S} d=${C.trash} size=${12}/></button>
      </div>
    </div>
  `}function Ze(){let{groups:t,byGroup:s,sessions:e}=L.value,n=T.value,o=Zt.value,a=te.value,[i,l]=ee(""),[d,r]=ee(!1),[c,h]=ee(()=>Object.fromEntries(t.map(u=>[u.path,u.expanded!==!1]))),v=u=>{if(o.length&&!o.includes(u.status))return!1;if(!i)return!0;let b=i.toLowerCase();return((u.title||"")+" "+(u.group||"")+" "+(u.path||"")+" "+(u.tool||"")+" "+(u.branch||"")).toLowerCase().includes(b)},x=yn(()=>e.filter(v).length,[e,i,o]),w=u=>{let b=Zt.value;Zt.value=b.includes(u)?b.filter($=>$!==u):[...b,u]},m=u=>h(b=>({...b,[u]:b[u]===!1})),A=u=>{T.value=u,M.value="terminal"},f=u=>{te.value={...a,[u]:!a[u]}};return N`
    <div class="sidebar">
      <div class="side-head">
        <span class="label">SESSIONS</span>
        <span class="count">${x}</span>
        <div class="spacer"/>
        <div style="position: relative;">
          <button class=${`icon-btn ${d?"active":""}`} title="Show columns" aria-label="Show columns"
                  data-testid="show-cols-btn"
                  onClick=${()=>r(u=>!u)}>
            <${S} d=${C.filter}/>
          </button>
          ${d&&N`
            <div class="show-menu" data-testid="show-cols-menu" onClick=${u=>u.stopPropagation()}>
              <div class="sm-head">SHOW IN ROW</div>
              ${xn.map(u=>N`
                <label key=${u.id} class="sm-row" data-testid=${`show-col-${u.id}`}>
                  <input type="checkbox" checked=${!!a[u.id]} onChange=${()=>f(u.id)}/>
                  <span>${u.label}</span>
                </label>
              `)}
              <div class="sm-foot" onClick=${()=>r(!1)}>done</div>
            </div>
          `}
        </div>
        ${D.value&&N`
          <button class="icon-btn" title="New session (n)" aria-label="New session"
                  onClick=${()=>j.value=!0}>
            <${S} d=${C.plus}/>
          </button>
        `}
      </div>
      <div class="side-filter">
        <input
          placeholder="/ filter"
          data-testid="sidebar-filter-input"
          value=${i}
          onInput=${u=>l(u.target.value)}
        />
        ${kn.map(u=>N`
          <span key=${u.id}
                class=${`side-chip ${o.includes(u.id)?"on":""}`}
                data-testid=${`status-chip-${u.id}`}
                onClick=${()=>w(u.id)}
                title=${u.id}>
            ${u.sym}
          </span>
        `)}
      </div>
      <div class="side-list">
        ${t.map(u=>{let b=(s[u.path]||[]).filter(v);if(i&&b.length===0)return null;let $=c[u.path]!==!1;return N`
            <div key=${u.path}>
              <div class=${`side-group-head ${u.kind||""}`} data-testid=${`group-head-${u.path}`} onClick=${()=>m(u.path)}>
                <span class="chev">${$?"\u25BE":"\u25B8"}</span>
                <span class="name">${u.label}</span>
                <span class="badge">(${b.length})</span>
              </div>
              ${$&&b.map(E=>N`
                <${wn} key=${E.id} s=${E} sel=${n===E.id} onSelect=${A} showCols=${a}/>
              `)}
            </div>
          `})}
        ${e.length===0&&N`
          <div style="padding: 16px; font-family: var(--mono); font-size: 11px; color: var(--muted); text-align: center;">
            No sessions yet. Press <span class="kbd" style="border:1px solid var(--border); padding: 0 4px; border-radius: 3px;">n</span> to create one.
          </div>
        `}
      </div>
    </div>
  `}import{html as se}from"htm/preact";function ts(){let{sessions:t}=L.value,s=B.value,e=t.filter(r=>r.status==="running").length,n=t.filter(r=>r.status==="waiting").length,o=t.filter(r=>r.status==="error").length,a=s==="connected"?{}:{background:"var(--tn-red)",boxShadow:"0 0 6px var(--tn-red)"},i=Yt.value,l=i?.cpu?.usage_percent,d=i?.memory?.usage_percent;return se`
    <div class="footer">
      <span class="fseg"><span class="d" style=${a}/>ws · ${s}</span>
      ${lt.value&&se`<span class="fseg">profile · ${lt.value}</span>`}
      <span class="fseg">sessions · ${t.length}</span>
      <span class="fseg" style="color: var(--tn-green);">● ${e}</span>
      <span class="fseg" style="color: var(--tn-yellow);">◐ ${n}</span>
      <span class="fseg" style="color: var(--tn-red);">✕ ${o}</span>
      ${typeof l=="number"&&se`<span class="fseg">cpu · ${l.toFixed(0)}%</span>`}
      ${typeof d=="number"&&se`<span class="fseg">mem · ${d.toFixed(0)}%</span>`}
      <span class="fspacer"/>
      <span class="fkbd"><span class="k">⌘K</span> palette</span>
      <span class="fkbd"><span class="k">/</span> filter</span>
      <span class="fkbd"><span class="k">n</span> new</span>
      <span class="fkbd"><span class="k">?</span> tweaks</span>
      <span class="fkbd"><span class="k">]</span> rail</span>
    </div>
  `}import{html as P}from"htm/preact";import{signal as Sn}from"@preact/signals";var es=Sn({}),Cn=[{id:"overview",label:"Overview"},{id:"usage",label:"Usage & activity"},{id:"mcps",label:"MCPs"},{id:"skills",label:"Skills"},{id:"children",label:"Children (conductor)"},{id:"events",label:"Events (watcher)"}];function V({title:t,badge:s,testid:e,children:n}){return P`
    <div class="card" data-testid=${e}>
      <div class="card-head">
        <span class="name">${t}</span>
        ${s&&P`<span class="pill">${s}</span>`}
      </div>
      <div class="card-body">${n}</div>
    </div>
  `}function Rt({msg:t}){return P`<div style="font-family: var(--mono); font-size: 11px; color: var(--muted);">${t}</div>`}function Tn(t,s){let e=new Map;for(let a of s){let i=a.raw&&a.raw.parentSessionId;i&&(e.has(i)||e.set(i,[]),e.get(i).push(a))}let n=new Set([t]),o=a=>(e.get(a)||[]).filter(l=>n.has(l.id)?!1:(n.add(l.id),!0)).map(l=>({session:l,children:o(l.id)}));return o(t)}function ss({node:t,depth:s,rootId:e}){let n=es.value,o=e+":"+t.session.id,a=!n[o],i=t.children.length>0,l=()=>{es.value={...n,[o]:a}};return P`
    <div class="child-node" data-session-id=${t.session.id} data-depth=${s}
         style="font-family: var(--mono); font-size: 11px; line-height: 1.7; padding-left: ${s*12}px;">
      <span class="child-row" style="display: inline-flex; align-items: center; gap: 4px;">
        <span class="child-toggle"
              onClick=${i?l:null}
              style=${`width: 10px; display: inline-block; cursor: ${i?"pointer":"default"}; color: var(--muted);`}>
          ${i?a?"\u25BE":"\u25B8":" "}
        </span>
        <span class="child-status pill" data-status=${t.session.status}
              style="font-size: 9px; padding: 0 4px;">${t.session.status}</span>
        <span class="child-title" style="color: var(--text-hi);">${t.session.title}</span>
        ${t.session.tool&&P`<span class="child-tool" style="color: var(--muted);">· ${t.session.tool}</span>`}
      </span>
      ${i&&a&&t.children.map(d=>P`
        <${ss} key=${d.session.id} node=${d} depth=${s+1} rootId=${e}/>
      `)}
    </div>
  `}function An({rootId:t,sessions:s}){let e=Tn(t,s);return e.length===0?P`<${Rt} msg="No child sessions yet."/>`:P`
    <div class="children-tree" data-children-count=${e.length}>
      ${e.map(n=>P`
        <${ss} key=${n.session.id} node=${n} depth=${0} rootId=${t}/>
      `)}
    </div>
  `}function ns(){let{sessions:t}=L.value,s=T.value,e=t.find(a=>a.id===s)||t[0],n=Qt.value;if(!e)return P`
      <div class="rightrail" data-testid="right-rail">
        <div class="rail-head"><span class="t">SESSION</span></div>
        <div class="rail-body">
          <div style="padding: 18px; font-family: var(--mono); font-size: 11px; color: var(--muted);">
            no session selected
          </div>
        </div>
      </div>
    `;let o=a=>{Qt.value={...n,[a]:!n[a]}};return P`
    <div class="rightrail" data-testid="right-rail">
      <div class="rail-head">
        <span class="t">SESSION</span>
        <div class="spacer"/>
        <span class="t" style="color: var(--text-hi);">${e.title}</span>
      </div>
      <div class="rail-body">
        ${n.overview&&P`
          <${V} title="OVERVIEW" badge=${e.status} testid="rail-card-overview">
            <div class="kv"><span class="k">kind</span><span class="v">${e.kind}</span></div>
            <div class="kv"><span class="k">tool</span><span class="v">${e.tool||"\u2014"}</span></div>
            ${e.model&&P`
              <div class="kv"><span class="k">model</span><span class="v">${e.model}</span></div>`}
            ${e.modelVersion&&P`
              <div class="kv"><span class="k">version</span><span class="v">${e.modelVersion}</span></div>`}
            ${e.modelId&&P`
              <div class="kv"><span class="k">model id</span><span class="v" title=${e.modelId}>${e.modelId}</span></div>`}
            <div class="kv"><span class="k">group</span><span class="v">${e.group||"\u2014"}</span></div>
            ${e.branch&&e.branch!=="\u2014"&&P`
              <div class="kv"><span class="k">branch</span><span class="v">${e.branch}</span></div>`}
            ${e.path&&P`
              <div class="kv"><span class="k">path</span><span class="v" title=${e.path}>${e.path}</span></div>`}
            ${e.sandbox&&P`<div class="kv"><span class="k">sandbox</span><span class="v warn">docker</span></div>`}
            ${e.worktree&&P`<div class="kv"><span class="k">worktree</span><span class="v ok">yes</span></div>`}
          </${V}>
        `}
        ${n.usage&&P`
          <${V} title="USAGE" testid="rail-card-usage">
            ${e.cost>0?P`<div class="kv"><span class="k">cost</span><span class="v ok">$${e.cost.toFixed(2)}</span></div>`:P`<${Rt} msg="cost data not available for this session"/>`}
            ${e.tokens>0&&P`<div class="kv"><span class="k">tokens</span><span class="v">${(e.tokens/1e3).toFixed(1)}k</span></div>`}
          </${V}>
        `}
        ${n.mcps&&P`
          <${V} title="MCPS" testid="rail-card-mcps">
            <${Rt} msg="MCP attachments not exposed via web API. Use TUI (m key)."/>
          </${V}>
        `}
        ${n.skills&&P`
          <${V} title="SKILLS" testid="rail-card-skills">
            <${Rt} msg="Skill attachments not exposed via web API. Use TUI (s key)."/>
          </${V}>
        `}
        ${n.children&&e.kind==="conductor"&&P`
          <${V} title="CHILDREN" badge="conductor" testid="rail-card-children">
            <${An} rootId=${e.id} sessions=${t}/>
          </${V}>
        `}
        ${n.events&&e.kind==="watcher"&&P`
          <${V} title="EVENTS" testid="rail-card-events">
            <${Rt} msg="Watcher event stream not exposed via web API."/>
          </${V}>
        `}
        <div class="rail-add">
          <div>Right-rail panels</div>
          <div class="opts">
            ${Cn.map(a=>P`
              <span key=${a.id}
                    data-testid=${`rail-panel-toggle-${a.id}`}
                    class=${`opt ${n[a.id]?"on":""}`}
                    onClick=${()=>o(a.id)}>
                ${a.label}
              </span>
            `)}
          </div>
        </div>
      </div>
    </div>
  `}import{html as as}from"htm/preact";var En=[{id:"command-center",label:"Command",icon:"\u2605"},{id:"fleet",label:"Fleet",icon:"\u25A6"},{id:"terminal",label:"Session",icon:"\u203A_"},{id:"watchers",label:"Watchers",icon:"\u25C7"},{id:"costs",label:"Costs",icon:"$"}];function os(){let t=M.value;return as`
    <div class="mob-tabs" data-testid="mobile-tabs">
      ${En.map(s=>as`
        <button key=${s.id}
                class=${`mob-tab ${t===s.id?"on":""}`}
                data-testid=${`mobile-tab-${s.id}`}
                onClick=${()=>M.value=s.id}>
          <span class="mt-ic">${s.icon}</span><span>${s.label}</span>
        </button>
      `)}
    </div>
  `}import{html as ne}from"htm/preact";import{useState as In,useEffect as On,useMemo as Pn,useRef as Dn}from"preact/hooks";function is(){let t=it.value,[s,e]=In(""),n=Dn(null);if(On(()=>{t&&(e(""),setTimeout(()=>n.current?.focus(),0))},[t]),!t)return null;let o=()=>it.value=!1,{sessions:a}=L.value,i=Pn(()=>{let c=[{id:"cmd-fleet",sec:"COMMANDS",label:"Open Fleet",tool:"\u25A6",run:()=>{M.value="fleet",o()}},{id:"cmd-terminal",sec:"COMMANDS",label:"Open Terminal",tool:"\u203A_",run:()=>{M.value="terminal",o()}},{id:"cmd-costs",sec:"COMMANDS",label:"Costs dashboard",tool:"$",run:()=>{M.value="costs",o()}},{id:"cmd-search",sec:"COMMANDS",label:"Session search",tool:"/",run:()=>{M.value="search",o()}},{id:"cmd-archived",sec:"COMMANDS",label:"Archived sessions",tool:"\u2302",run:()=>{M.value="archived",o()}},{id:"cmd-tweaks",sec:"COMMANDS",label:"Open Tweaks",tool:"T",run:()=>{tt.value=!0,o()}},{id:"cmd-shortcuts",sec:"COMMANDS",label:"Keyboard shortcuts",tool:"?",run:()=>{nt.value=!0,o()}},{id:"cmd-settings",sec:"COMMANDS",label:"Settings drawer",tool:"S",run:()=>{ot.value=!0,o()}}];return D.value&&c.unshift({id:"cmd-new",sec:"COMMANDS",label:"New session",tool:"n",run:()=>{j.value=!0,o()}}),c},[]),l=a.map(c=>({id:c.id,sec:"SESSIONS",label:c.title,tool:c.tool||c.kind,run:()=>{T.value=c.id,M.value="terminal",o()}})),d=[...i,...l].filter(c=>!s||c.label.toLowerCase().includes(s.toLowerCase())),r={};return d.forEach(c=>{(r[c.sec]||=[]).push(c)}),ne`
    <div class="overlay" onClick=${o}>
      <div class="cmdk" data-testid="command-palette" onClick=${c=>c.stopPropagation()}>
        <div class="inp">
          <${S} d=${C.search}/>
          <input ref=${n} data-testid="palette-input" value=${s} onInput=${c=>e(c.target.value)}
                 placeholder="Type a command or session name…"
                 onKeyDown=${c=>{c.key==="Escape"&&o()}}/>
          <span class="kbd">esc</span>
        </div>
        <div class="list">
          ${Object.entries(r).map(([c,h])=>ne`
            <div key=${c}>
              <div class="sec">${c}</div>
              ${h.map((v,x)=>ne`
                <div key=${v.id} data-testid=${v.sec==="SESSIONS"?"palette-session-row":"palette-cmd-row"} class=${`row ${x===0&&c===Object.keys(r)[0]?"f":""}`} onClick=${v.run}>
                  <span>${v.label}</span>
                  <span class="tool">${v.tool||""}</span>
                </div>
              `)}
            </div>
          `)}
          ${d.length===0&&ne`
            <div data-testid="palette-empty" style="padding: 16px; font-family: var(--mono); font-size: 12px; color: var(--muted); text-align: center;">
              No matches.
            </div>
          `}
        </div>
      </div>
    </div>
  `}import{html as xe}from"htm/preact";var Mn=[{id:"blue",color:"var(--tn-blue)"},{id:"amber",color:"var(--tn-yellow)"},{id:"green",color:"var(--tn-green)"},{id:"purple",color:"var(--tn-purple)"}];function ls(){if(!tt.value)return null;let t=At.value,s=Et.value,e=q.value,n=()=>tt.value=!1;return xe`
    <div class="tweaks" role="dialog" aria-label="Tweaks" data-testid="tweaks-panel">
      <div class="th">
        <${S} d=${C.settings} size=${14}/>
        <div class="t">Tweaks</div>
        <button class="icon-btn" data-testid="tweaks-close" onClick=${n} aria-label="Close tweaks">
          <${S} d=${C.x}/>
        </button>
      </div>
      <div class="tb">
        <div>
          <label>ACCENT</label>
          <div class="swatch-row">
            ${Mn.map(o=>xe`
              <div key=${o.id}
                   data-testid=${`tweaks-accent-${o.id}`}
                   class=${`swatch ${t===o.id?"on":""}`}
                   style=${{background:o.color}}
                   onClick=${()=>At.value=o.id}/>
            `)}
          </div>
        </div>
        <div>
          <label>DENSITY</label>
          <div class="seg-row">
            ${["compact","balanced","comfortable"].map(o=>xe`
              <button key=${o}
                      data-testid=${`tweaks-density-${o}`}
                      class=${`seg-btn ${s===o?"on":""}`}
                      onClick=${()=>Et.value=o}>${o}</button>
            `)}
          </div>
        </div>
        <div>
          <label>RIGHT RAIL</label>
          <div style="display: flex; align-items: center; gap: 8px;">
            <div class=${`switch ${e==="visible"?"on":""}`}
                 data-testid="tweaks-rail-switch"
                 onClick=${()=>q.value=e==="visible"?"hidden":"visible"}/>
            <span style="font-family: var(--mono); font-size: 11px; color: var(--text-dim);">
              ${e==="visible"?"visible":"hidden"}
            </span>
          </div>
        </div>
      </div>
    </div>
  `}import{html as Un}from"htm/preact";import{html as ae}from"htm/preact";import{useEffect as ds,useRef as us,useCallback as Nn,useState as ps}from"preact/hooks";import{Terminal as Ln}from"@xterm/xterm";import{FitAddon as Rn}from"@xterm/addon-fit";import{WebglAddon as zn}from"@xterm/addon-webgl";import{html as rs}from"htm/preact";function cs(){let e=ut.value.filter(o=>o.type==="session"&&o.session).length,n=D.value;return rs`
    <div style="flex: 1; min-height: 0; display: flex; align-items: center; justify-content: center; padding: 32px;">
      <div data-testid="empty-state-dashboard"
           style="display: flex; flex-direction: column; align-items: center; gap: 18px; max-width: 420px; text-align: center;">
        <${bt}/>
        <div>
          <div style="font-size: 16px; font-weight: 600; color: var(--text-hi); margin-bottom: 6px;">
            No session selected
          </div>
          <div style="font-family: var(--mono); font-size: 12px; color: var(--muted); line-height: 1.55;">
            ${e===0?"Your deck is empty. Create a session to get started, or browse the fleet view from the sidebar.":`You have ${e} session${e===1?"":"s"}. Pick one from the sidebar, or open the Fleet tab.`}
          </div>
        </div>
        <div style="display: flex; gap: 8px;">
          <button class="btn ghost" onClick=${()=>M.value="fleet"}>
            Open Fleet
          </button>
          ${n&&rs`
            <button class="btn primary" onClick=${()=>j.value=!0}>
              <${S} d=${C.plus} size=${12}/>New session <span class="kbd">n</span>
            </button>
          `}
        </div>
      </div>
    </div>
  `}function Fn(){return typeof window.matchMedia=="function"&&window.matchMedia("(pointer: coarse)").matches}function _n(t,s){let e=window.location.protocol==="https:"?"wss":"ws",n=new URL(e+"://"+window.location.host+"/ws/session/"+encodeURIComponent(t));return s&&n.searchParams.set("token",s),n.toString()}function Gn(t,s,e){if(!t||!s)return;let n=!1,o=0;function a(d){!d.touches||d.touches.length!==1||(n=!0,o=d.touches[0].clientY)}function i(d){if(!n||!d.touches||d.touches.length!==1)return;d.preventDefault();let r=d.touches[0].clientY,c=o-r;o=r,s&&c!==0&&s.dispatchEvent(new WheelEvent("wheel",{deltaY:c,deltaMode:0,bubbles:!0,cancelable:!0}))}function l(){n=!1}t.addEventListener("touchstart",a,{capture:!0,passive:!0,signal:e.signal}),t.addEventListener("touchmove",i,{capture:!0,passive:!1,signal:e.signal}),t.addEventListener("touchend",l,{capture:!0,passive:!0,signal:e.signal}),t.addEventListener("touchcancel",l,{capture:!0,passive:!0,signal:e.signal})}function vs(){let t=us(null),s=us(null),e=T.value,[n,o]=ps(null),[a,i]=ps(0);ds(()=>(window.__preactTerminalActive=!0,()=>{window.__preactTerminalActive=!1}),[]);let l=Nn(()=>{let r=s.current;r&&(r.reconnectTimer&&clearTimeout(r.reconnectTimer),r.ws&&(r.ws.close(),r.ws=null),r.resizeObserver&&r.resizeObserver.disconnect(),r.controller&&r.controller.abort(),r.terminal&&r.terminal.dispose(),s.current=null,st.value="disconnected")},[]);if(ds(()=>{if(!t.current||!e){l();return}if(s.current&&s.current.sessionId===e&&s.current.reconnectKey===a)return;l(),o(null);let r=t.current,c=K.value,h=Fn(),v=new Ln({convertEol:!1,cursorBlink:!h,disableStdin:!1,fontFamily:"IBM Plex Mono, Menlo, Consolas, monospace",fontSize:13,scrollback:1e4,theme:{background:"#0a1220",foreground:"#d9e2ec",cursor:"#9ecbff"}}),x=new Rn;v.loadAddon(x),v.open(r);try{let g=new zn;g.onContextLoss(()=>{g.dispose(),typeof window.CanvasAddon<"u"&&v.loadAddon(new window.CanvasAddon.CanvasAddon)}),v.loadAddon(g)}catch{if(typeof window.CanvasAddon<"u")try{v.loadAddon(new window.CanvasAddon.CanvasAddon)}catch{}}r.offsetWidth&&r.offsetHeight&&x.fit();let w=new AbortController;if(!h&&typeof document<"u"){let g=document.createElement("link");g.rel="preload",g.as="script",g.crossOrigin="anonymous",g.href="/static/vendor/addon-webgl.mjs",document.head.appendChild(g),w.signal.addEventListener("abort",()=>{g.parentNode&&g.parentNode.removeChild(g)})}let m={sessionId:e,reconnectKey:a,terminal:v,fitAddon:x,ws:null,resizeObserver:null,controller:w,decoder:new TextDecoder,reconnectTimer:null,reconnectAttempt:0,wsReconnectEnabled:!0,terminalAttached:!1};s.current=m;let A=null;function f(g){clearTimeout(A),A=setTimeout(()=>{if(!r.offsetWidth||!r.offsetHeight)return;x.fit();let{cols:p,rows:F}=v;p>=10&&F>=3&&m.ws&&m.ws.readyState===WebSocket.OPEN&&m.terminalAttached&&m.ws.send(JSON.stringify({type:"resize",cols:p,rows:F}))},g)}if(typeof ResizeObserver=="function"){let g=new ResizeObserver(()=>f(90));g.observe(r),m.resizeObserver=g}window.addEventListener("resize",()=>f(120),{signal:w.signal}),Gn(r,v.element,w);let u=v.onData(g=>{!m.ws||m.ws.readyState!==WebSocket.OPEN||!m.terminalAttached||qt.value||m.ws.send(JSON.stringify({type:"input",data:g}))});h||r.addEventListener("paste",g=>{if(qt.value||!m.ws||m.ws.readyState!==WebSocket.OPEN||!m.terminalAttached)return;let p=g.clipboardData;if(!p)return;let F=p.getData("text/plain");F&&(F=F.replace(/\r\n?/g,`
`),g.preventDefault(),g.stopPropagation(),m.ws.send(JSON.stringify({type:"input",data:F})))},{capture:!0,signal:w.signal}),v.writeln("Connecting to terminal...");function b(g){let p=Math.min(g,8);return Math.min(8e3,Math.round(350*Math.pow(1.8,p-1)))}function $(){if(!m.wsReconnectEnabled||m.reconnectTimer||m.ws)return;m.reconnectAttempt+=1;let g=b(m.reconnectAttempt);st.value="connecting",m.reconnectTimer=setTimeout(()=>{m.reconnectTimer=null,E(!0)},g)}function E(g){m.ws&&(m.ws.close(),m.ws=null),m.terminalAttached=!1,m.wsReconnectEnabled=!0,st.value="connecting";let p=new WebSocket(_n(e,c));p.binaryType="arraybuffer",m.ws=p;function F(){m.ws===p&&(m.reconnectTimer&&(clearTimeout(m.reconnectTimer),m.reconnectTimer=null),m.reconnectAttempt=0,st.value="connected",p.send(JSON.stringify({type:"ping"})))}function W($t){if(m.ws===p){if(typeof $t.data=="string"){try{let _=JSON.parse($t.data);if(_.type==="status")_.event==="connected"?(qt.value=!!_.readOnly,v&&(v.options.disableStdin=!!_.readOnly),st.value="connected"):_.event==="terminal_attached"?(m.terminalAttached=!0,f(0)):_.event==="session_closed"&&(m.terminalAttached=!1);else if(_.type==="error"){if((_.code==="TERMINAL_ATTACH_FAILED"||_.code==="TMUX_SESSION_NOT_FOUND")&&(m.terminalAttached=!1),_.code==="TMUX_SESSION_NOT_FOUND"){m.wsReconnectEnabled=!1,o({code:_.code,message:_.message||"tmux session is not available",hint:_.hint||""}),st.value="disconnected";return}v.write(`\r
[error:`+(_.code||"unknown")+"] "+(_.message||"unknown error")+`\r
`)}}catch{}return}if($t.data instanceof ArrayBuffer){let _=m.decoder.decode(new Uint8Array($t.data),{stream:!0});v.write(_)}}}function X(){m.ws===p&&(st.value="error")}function Ht(){if(m.ws===p){if(m.ws=null,m.terminalAttached=!1,m.wsReconnectEnabled){$();return}st.value="disconnected"}}p.addEventListener("open",F,{signal:w.signal}),p.addEventListener("message",W,{signal:w.signal}),p.addEventListener("error",X,{signal:w.signal}),p.addEventListener("close",Ht,{signal:w.signal})}return E(!1),h||v.focus(),()=>{u.dispose(),clearTimeout(A),l()}},[e,a,l]),!e)return ae`<${cs} />`;async function d(){try{await y("POST","/api/sessions/"+e+"/restart"),o(null),i(r=>r+1)}catch{}}return ae`
    <div class="term-frame" style="position: relative;">
      <div class="term-strip">
        <span class="tdots"><i/><i/><i/></span>
        <span class="tpath">session · ${e}</span>
        <span style="flex: 1;"/>
      </div>
      <div style="flex: 1; min-height: 0; min-width: 0; overflow: hidden; padding: 14px 16px;">
        <div ref=${t} style="height: 100%; width: 100%; overflow: hidden;"/>
      </div>
      ${n&&ae`
        <div role="alert"
             style=${{position:"absolute",inset:"12px 12px auto 12px",border:"1px solid rgba(247,118,142,0.4)",background:"rgba(22,22,30,0.95)",borderRadius:"var(--radius-lg)",boxShadow:"0 30px 60px -20px rgba(0,0,0,0.55)",padding:"14px 16px"}}>
          <div style="display: flex; align-items: flex-start; gap: 12px;">
            <span style="color: var(--tn-red); font-size: 18px; line-height: 1;">⚠</span>
            <div style="flex: 1; min-width: 0;">
              <div style="font-weight: 600; color: var(--text-hi);">Terminal disconnected</div>
              <div style="font-size: 12.5px; color: var(--text); margin-top: 4px;">${n.message}</div>
              ${n.hint&&ae`<div style="font-size: 11.5px; color: var(--muted); margin-top: 6px;">${n.hint}</div>`}
              <div style="display: flex; gap: 8px; margin-top: 10px;">
                <button type="button" class="btn primary" onClick=${d}>Restart session</button>
                <button type="button" class="btn ghost" onClick=${()=>o(null)}>Dismiss</button>
              </div>
            </div>
          </div>
        </div>
      `}
    </div>
  `}function ms(){return Un`
    <div class="term-wrap">
      <${vs}/>
    </div>
  `}import{html as Hn}from"htm/preact";import{html as we}from"htm/preact";import{useEffect as Se,useRef as oe,useState as Ce}from"preact/hooks";var zt=null;function jn(){return typeof window>"u"?Promise.reject(new Error("no window")):window.Chart?Promise.resolve(window.Chart):zt||(zt=new Promise((t,s)=>{let e=document.createElement("script");e.src="/static/chart.umd.min.js",e.async=!0,e.onload=()=>{window.Chart?t(window.Chart):s(new Error("chart.umd.min.js loaded but window.Chart missing"))},e.onerror=()=>{zt=null,s(new Error("failed to load chart.umd.min.js"))},document.head.appendChild(e)}),zt)}var fs=new Intl.NumberFormat(navigator.language,{style:"currency",currency:"USD"});function ie(t){return fs.format(t||0)}function Wn(){let t=getComputedStyle(document.documentElement),s=(e,n)=>t.getPropertyValue(e).trim()||n;return{text:s("--chart-text","#6b7280"),grid:s("--chart-grid","#e5e7eb"),legend:s("--chart-legend","#374151"),primary:s("--chart-primary","#2959aa"),primaryFill:s("--chart-primary-fill","rgba(41, 89, 170, 0.1)"),categorical:[s("--chart-categorical-1","#7aa2f7"),s("--chart-categorical-2","#bb9af7"),s("--chart-categorical-3","#7dcfff"),s("--chart-categorical-4","#9ece6a"),s("--chart-categorical-5","#e0af68"),s("--chart-categorical-6","#f7768e"),s("--chart-categorical-7","#73daca"),s("--chart-categorical-8","#ff9e64")]}}function gs(){let[t,s]=Ce(null),[e,n]=Ce(null),[o,a]=Ce(!0),i=oe(null),l=oe(null),d=oe(null),r=oe(null);return Se(()=>{y("GET","/api/costs/summary").then(c=>{s(c),a(!1)}).catch(c=>{n(c.message||"Failed to load cost data"),a(!1)})},[]),Se(()=>{if(o||e||!i.current||!l.current)return;let c=!1;async function h(){try{let[x,w,m]=await Promise.all([jn(),y("GET","/api/costs/daily?days=30"),y("GET","/api/costs/models")]);if(c||(d.current&&(d.current.destroy(),d.current=null),r.current&&(r.current.destroy(),r.current=null),!i.current||!l.current))return;let A=Wn(),f=w||[],u=f.map(p=>p.date.slice(5)),b=f.map(p=>p.cost_usd);d.current=new x(i.current,{type:"line",data:{labels:u,datasets:[{label:"Daily Cost ($)",data:b,borderColor:A.primary,backgroundColor:A.primaryFill,fill:!0,tension:.3}]},options:{responsive:!0,plugins:{legend:{display:!1}},scales:{x:{ticks:{color:A.text},grid:{color:A.grid}},y:{ticks:{color:A.text,callback:p=>fs.format(p||0)},grid:{color:A.grid}}}}});let $=m||{},E=Object.keys($),g=Object.values($);r.current=new x(l.current,{type:"doughnut",data:{labels:E,datasets:[{data:g,backgroundColor:A.categorical.slice(0,E.length)}]},options:{responsive:!0,plugins:{legend:{position:"bottom",labels:{color:A.legend,font:{size:11}}}}}})}catch{}}h();let v=new MutationObserver(()=>{h()});return v.observe(document.documentElement,{attributes:!0,attributeFilter:["class"]}),()=>{c=!0,v.disconnect()}},[o,e]),Se(()=>()=>{d.current&&(d.current.destroy(),d.current=null),r.current&&(r.current.destroy(),r.current=null)},[]),o?we`
      <div style="padding: 18px; font-family: var(--mono); font-size: 12px; color: var(--muted);">
        Loading cost data…
      </div>
    `:e?we`
      <div class="chart-card" style="margin: 14px;">
        <div class="title">Cost tracking unavailable</div>
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); line-height: 1.6;">
          Start agent-deck with the cost tracker enabled to see spend, daily history, and per-model
          breakdowns here. The fixture binary intentionally runs without it.
        </div>
      </div>
    `:we`
    <div style="display: flex; flex-direction: column; gap: 12px; flex: 1; min-height: 0; overflow: auto;">
      <div class="stat-grid">
        <div class="stat">
          <div class="lab">TODAY</div>
          <div class="val">${ie(t.today_usd)}</div>
          <div class="delta">${t.today_events} events</div>
        </div>
        <div class="stat">
          <div class="lab">THIS WEEK</div>
          <div class="val">${ie(t.week_usd)}</div>
          <div class="delta">${t.week_events} events</div>
        </div>
        <div class="stat">
          <div class="lab">THIS MONTH</div>
          <div class="val">${ie(t.month_usd)}</div>
          <div class="delta">${t.month_events} events</div>
        </div>
        <div class="stat">
          <div class="lab">PROJECTED</div>
          <div class="val">${ie(t.projected_usd)}</div>
          <div class="delta">based on 7-day avg</div>
        </div>
      </div>
      <div style="display: grid; grid-template-columns: 2fr 1fr; gap: 12px;">
        <div class="chart-card">
          <div class="title">Daily spend · last 30 days</div>
          <canvas ref=${i}></canvas>
        </div>
        <div class="chart-card">
          <div class="title">Cost by model</div>
          <canvas ref=${l}></canvas>
        </div>
      </div>
    </div>
  `}function hs(){return Hn`
    <div class="costs">
      <${gs}/>
    </div>
  `}import{html as mt}from"htm/preact";import{useMemo as Kn}from"preact/hooks";function Bn({name:t,items:s,onSelect:e}){let n=s.filter(l=>l.status==="running").length,o=s.filter(l=>l.status==="waiting").length,a=s.filter(l=>l.status==="error").length,i=a?"error":o?"waiting":n?"running":"";return mt`
    <div class=${`group-card ${i}`} data-testid="fleet-group-card" data-group-name=${t}>
      <div class="gc-head">
        <span class="t">${t}</span>
        <span class="health"><span class=${`d ${i||"idle"}`}/></span>
        <span class="cost"></span>
      </div>
      <div class="gc-tiles">
        ${s.slice(0,6).map(l=>mt`
          <button key=${l.id} class="tile" data-testid="fleet-session-tile" data-session-id=${l.id} onClick=${()=>e(l.id)}>
            <span class=${`tdot ${l.status}`}/>
            <span class="tn">${l.title}</span>
            ${l.tool&&mt`<span class="ttool">${l.tool}</span>`}
          </button>
        `)}
      </div>
      <div class="gc-foot">
        <span class="cn"><span class="d running"/>${n}</span>
        <span class="cn"><span class="d waiting"/>${o}</span>
        <span class="cn"><span class="d error"/>${a}</span>
        <span class="path" data-testid="fleet-group-session-count">${s.length} session${s.length===1?"":"s"}</span>
      </div>
    </div>
  `}function $s(){let{groups:t,byGroup:s,sessions:e}=L.value,n=Kn(()=>({running:e.filter(i=>i.status==="running").length,waiting:e.filter(i=>i.status==="waiting").length,error:e.filter(i=>i.status==="error").length,idle:e.filter(i=>i.status==="idle").length}),[e]),o=e.reduce((i,l)=>i+(l.cost||0),0),a=i=>{T.value=i,M.value="terminal"};return mt`
    <div class="fleet" data-testid="fleet-pane">
      <div class="fleet-stats">
        <div class="stat" data-testid="fleet-stat-running"><div class="lbl">RUNNING</div><div class="num running">${n.running}</div></div>
        <div class="stat" data-testid="fleet-stat-waiting"><div class="lbl">WAITING</div><div class="num waiting">${n.waiting}</div></div>
        <div class="stat" data-testid="fleet-stat-error"><div class="lbl">ERROR</div><div class="num error">${n.error}</div></div>
        <div class="stat" data-testid="fleet-stat-idle"><div class="lbl">IDLE</div><div class="num idle">${n.idle}</div></div>
        <div class="stat" data-testid="fleet-stat-cost"><div class="lbl">SPEND · TODAY</div><div class="num cost">$${o.toFixed(2)}</div></div>
        <div class="stat" data-testid="fleet-stat-sessions"><div class="lbl">SESSIONS</div><div class="num">${e.length}</div></div>
      </div>

      <div class="fleet-section">
        <div class="fleet-section-head">
          <span class="kicker">GROUPS</span>
          <span class="sub-kicker">${t.length} group${t.length===1?"":"s"} · ${e.length} session${e.length===1?"":"s"}</span>
        </div>
        ${t.length===0||e.length===0?mt`<div style="font-family: var(--mono); font-size: 11px; color: var(--muted); padding: 16px;">
              No sessions yet. Use the sidebar to create one.
            </div>`:mt`<div class="fleet-grid">
              ${t.map(i=>{let l=s[i.path]||[];return l.length===0?null:mt`<${Bn} key=${i.path} name=${i.label} items=${l} onSelect=${a}/>`})}
            </div>`}
      </div>
    </div>
  `}import{html as U}from"htm/preact";import{useState as Pt,useEffect as Qn,useRef as Zn}from"preact/hooks";async function le({text:t,target:s,context:e}){let n=(t||"").trim();if(!n)return null;let o=s||"maestro",a="cc-pending-"+Date.now();Ve({correlationId:a,target:o,text:n,stage:"received",ack:"got it\u2026",reply:"",at:new Date().toISOString()});let i;try{i=await y("POST","/api/command-center/ask",{target:o,text:n,context:e||{}})}catch(d){return Tt(a,{stage:"failed",ack:"sent, but delivery failed: "+(d.message||"error")}),a}let l=i.correlationId||a;return Tt(a,{correlationId:l,target:i.routedTo||o,stage:i.stage||"routed",ack:i.ack||"got it \u2014 routed to "+(i.routedTo||o)}),bs(l,i.routedTo||o),l}async function bs(t,s,e=0){if(!s||e>6)return;let n=2500+e*1500;setTimeout(async()=>{try{let o="?correlationId="+encodeURIComponent(t)+"&target="+encodeURIComponent(s),a=await y("GET","/api/command-center/reply"+o);if(a&&a.reply){Tt(t,{stage:"result",reply:a.reply});return}}catch{}bs(t,s,e+1)},n)}function re(t){return t.kind==="decision"?{text:`re ${t.id||t.question?.slice(0,24)}: `,target:t.route||"conductor-agent-deck",context:{decisionId:t.id||""}}:t.kind==="session"?{text:`re session ${t.title}: `,target:t.conductorTarget||"maestro",context:{sessionTitle:t.title,project:t.project||""}}:{text:`re ${t.name}: `,target:t.target||"conductor-"+t.name,context:{project:t.name}}}import{html as Te}from"htm/preact";var qn={received:"received",routed:"routed","session-created":"session created",result:"replied",failed:"delivery failed"};function Vn(t){return t==="result"?"\u2705":t==="session-created"?"\u{1F680}":t==="failed"?"\u26A0\uFE0F":t==="received"?"\u23F3":"\u{1F4E8}"}function ce(){let t=at.value;return!t||t.length===0?null:Te`
    <section class="ccd-sec cc-acks" data-testid="cc-acks">
      <h2>🧾 Your asks</h2>
      ${t.map(s=>Te`
        <div class="cc-ack" key=${s.correlationId} data-testid="cc-ack" data-stage=${s.stage}>
          <div class="cc-ack-line">
            <span class="cc-ack-ico">${Vn(s.stage)}</span>
            <span class="cc-ack-text" title=${s.text}>${s.text}</span>
          </div>
          <div class="cc-ack-meta">
            got it → ${s.target} → <span class="cc-ack-stage">${qn[s.stage]||s.stage}</span>
          </div>
          ${s.reply&&Te`<div class="cc-ack-reply" data-testid="cc-ack-reply">${s.reply}</div>`}
        </div>
      `)}
    </section>
  `}import{html as G}from"htm/preact";import{useState as de,useEffect as ks}from"preact/hooks";var ys={running:"\u{1F7E2}",waiting:"\u{1F7E1}",idle:"\u26AA",error:"\u{1F534}",stopped:"\u26AB",absent:"\u26AB"};function Jn(t){if(!t)return"";let s=Date.parse(t);if(isNaN(s))return"";let e=Math.max(0,Math.floor((Date.now()-s)/1e3));return e<60?e+"s ago":e<3600?Math.floor(e/60)+"m ago":e<86400?Math.floor(e/3600)+"h ago":Math.floor(e/86400)+"d ago"}function Yn(t,s){ks(()=>{let e=!1;if(!t){Ct.value=null;return}return y("GET","/api/command-center/detail/"+encodeURIComponent(t)).then(n=>{e||(Ct.value=n)}).catch(()=>{e||(Ct.value=null)}),()=>{e=!0}},[t,s])}function Xn({doc:t}){let[s,e]=de(!0);return G`
    <div class="ccd-doc" data-testid="ccd-doc">
      <button class="ccd-doc-head" onClick=${()=>e(n=>!n)}>
        <span class="ccd-doc-caret">${s?"\u25BE":"\u25B8"}</span>
        <span class="ccd-doc-title">${t.title||t.name}</span>
        <span class="ccd-doc-when">${Jn(t.updatedAt)}</span>
      </button>
      ${s&&G`<div class="ccd-doc-body md" dangerouslySetInnerHTML=${{__html:t.html}}></div>`}
    </div>
  `}function xs(){let t=St.value,s=Q.value,e=Ct.value,n=D.value,[o,a]=de(""),[i,l]=de(!1),[d,r]=de("ready");Yn(t,s&&s.generatedAt);let c=()=>{St.value=null,Ct.value=null};ks(()=>{let $=E=>{E.key==="Escape"&&(document.activeElement&&document.activeElement.tagName==="TEXTAREA"?document.activeElement.blur():c()),E.key==="/"&&document.activeElement?.tagName!=="TEXTAREA"&&(E.preventDefault(),document.querySelector(".ccd-input textarea")?.focus())};return window.addEventListener("keydown",$),()=>window.removeEventListener("keydown",$)},[]);let h=e&&e.target||"conductor-"+t,v=async $=>{let E=o.trim();if(!(!E||i)){if(!n){I("Two-way input is disabled (web mutations off)","info");return}l(!0),r("sending\u2026");try{await le({text:E,target:h,context:$||{project:t}}),r("\u2713 routed to "+h),a("")}catch(g){r("\u2717 "+(g.message||"send failed"))}finally{l(!1)}}},x=$=>{let E=re($);a(E.text),document.querySelector(".ccd-input textarea")?.focus()},w=$=>{$.key==="Enter"&&($.ctrlKey||$.metaKey)&&($.preventDefault(),v())},m=e&&e.sessions||[],A=e&&e.docs||[],f=e&&e.decisions||[],u=e&&e.inProgress||[],b=e&&e.recentlyDone||[];return G`
    <div class="ccd" data-testid="command-center-detail-pane">
      <div class="ccd-top">
        <button class="ccd-back" onClick=${c} data-testid="ccd-back" title="Back (Esc)">← back</button>
        <span class="ccd-dot">${ys[e&&e.status]||"\u26AA"}</span>
        <h1>${t}</h1>
        <span class="ccd-headline" data-testid="ccd-headline">${e&&e.headline||""}</span>
      </div>

      ${!e&&G`<div class="cc-empty" data-testid="ccd-loading">loading ${t}…</div>`}

      ${e&&G`
        <div class="ccd-grid">
          <div class="ccd-main">
            ${(u.length>0||b.length>0)&&G`
              <section class="ccd-sec">
                ${u.length>0&&G`
                  <h2>🛠 In progress</h2>
                  <ul class="ccd-list">${u.map(($,E)=>G`<li key=${E}>${$}</li>`)}</ul>`}
                ${b.length>0&&G`
                  <h2>✅ Recently done</h2>
                  <ul class="ccd-list">${b.map(($,E)=>G`<li key=${E}>${$}</li>`)}</ul>`}
              </section>`}

            <section class="ccd-sec" data-testid="ccd-docs">
              <h2>📄 Produced docs ${A.length?G`<span class="ccd-count">${A.length}</span>`:""}</h2>
              ${A.length?A.map($=>G`<${Xn} key=${$.name} doc=${$}/>`):G`<div class="cc-sdone" data-testid="ccd-no-docs">no docs yet — drops here from ${t}'s outputs/</div>`}
            </section>
          </div>

          <div class="ccd-side">
            <section class="ccd-sec">
              <h2>👉 Decisions</h2>
              ${f.length?f.map(($,E)=>G`
                    <div class="cc-ask" key=${$.id||E}>
                      ${$.id&&G`<span class="cc-ask-id">${$.id}</span>`}
                      <span class="cc-ask-text">${$.question}</span>
                      <button class="cc-cmt" title="Answer this"
                        onClick=${()=>x({kind:"decision",...$})}>💬</button>
                    </div>`):G`<div class="cc-sdone">none waiting</div>`}
            </section>

            <section class="ccd-sec" data-testid="ccd-sessions">
              <h2>🛰️ Live sessions</h2>
              ${m.length?m.map($=>G`
                    <div class="cc-srow" key=${$.id} data-testid="ccd-session" data-status=${$.status}>
                      <span class="cc-sd">${ys[$.status]||"\u26AA"}</span>
                      <span class="cc-stt" title=${$.workingOn||$.title}>${$.title}</span>
                      <button class="cc-cmt" title="Comment on this session"
                        onClick=${()=>x({kind:"session",title:$.title,conductorTarget:h,project:t})}>💬</button>
                    </div>`):G`<div class="cc-sdone">no active sessions</div>`}
            </section>

            <${ce}/>
          </div>
        </div>

        <div class="ccd-input cc-input" data-testid="ccd-input">
          <span class="ccd-scope" title="This input routes to ${h}">→ ${h}</span>
          <textarea
            placeholder=${"talk to "+t+"\u2026 \u2318/Ctrl+Enter to send, Esc to go back"}
            value=${o}
            onInput=${$=>a($.target.value)}
            onKeyDown=${w}></textarea>
          <button class="cc-send" disabled=${!o.trim()||i} onClick=${()=>v()} data-testid="ccd-send">➤ Send</button>
          <span class=${`cc-st ${d.startsWith("\u2713")?"ok":d.startsWith("\u2717")?"err":""}`} data-testid="ccd-status">${d}</span>
        </div>
      `}
    </div>
  `}var ws={running:"\u{1F7E2}",waiting:"\u{1F7E1}",idle:"\u26AA",error:"\u{1F534}",stopped:"\u26AB",absent:"\u26AB"},Ss={"model-unavailable":"model unavailable","auth-401":"auth error (401)","idle-at-empty-prompt":"idle (empty prompt)"};function ta(t,s){return t==="maestro"&&s==="running"?"\u{1F535}":ws[s]||"\u26AA"}function ea(t){return t?["running","waiting","idle"].filter(s=>t[s]).map(s=>`${t[s]} ${s}`).join(" \xB7 "):""}function sa({decision:t,onComment:s}){return U`
    <div class="cc-ask" data-testid="cc-decision">
      ${t.id&&U`<span class="cc-ask-id">${t.id}</span>`}
      <span class="cc-ask-text">${t.question}</span>
      <button class="cc-cmt" title="Comment / answer this"
        onClick=${()=>s({kind:"decision",...t})}>💬</button>
    </div>
  `}function na({sess:t,conductorTarget:s,project:e,onComment:n}){let o=t.substate&&Ss[t.substate];return U`
    <div class="cc-srow" data-testid="cc-session" data-status=${t.status}>
      <span class="cc-sd">${ws[t.status]||"\u26AA"}</span>
      <span class="cc-stt" title=${t.workingOn||t.title}>${t.title}</span>
      ${o&&U`<span class="cc-sub" title=${"honest-status: "+t.substate}>${o}</span>`}
      <button class="cc-cmt" title="Comment on this session"
        onClick=${()=>n({kind:"session",title:t.title,conductorTarget:s,project:e})}>💬</button>
    </div>
  `}function aa({cd:t,index:s,focused:e,onComment:n,onOpen:o}){let[a,i]=Pt(!1),l=t.substate&&Ss[t.substate];return U`
    <div class=${`cc-cd ${a?"open":""} ${e?"focused":""}`}
      data-testid="cc-conductor" data-name=${t.name}>
      <div class="cc-cd-head">
        <button class="cc-cd-toggle" title="Expand sessions" onClick=${()=>i(d=>!d)}>
          <span class="cc-jump">${s<9?s+1:""}</span>
          <span class="cc-dot">${ta(t.name,t.status)}</span>
          <span class="cc-nm">${t.name}</span>
          <span class="cc-ac" title=${t.currentlyWorkingOn||""}>
            ${t.currentlyWorkingOn||(t.status==="absent"?"no conductor session":t.status)}
            ${l&&U` · ${l}`}
          </span>
          <span class="cc-lc">${ea(t.counts)}</span>
          ${t.docCount>0&&U`<span class="cc-docs" title=${t.docCount+" docs"}>📄${t.docCount}</span>`}
        </button>
        <button class="cc-cmt" title="Comment on this project"
          onClick=${()=>n({kind:"conductor",name:t.name,target:t.target})}>💬</button>
        <button class="cc-open" title="Open detail page" data-testid="cc-open-detail"
          onClick=${()=>o(t.name)}>open →</button>
      </div>
      ${a&&U`
        <div class="cc-cd-body">
          ${t.sessions&&t.sessions.length?t.sessions.map(d=>U`<${na} key=${d.id} sess=${d}
                conductorTarget=${t.target} project=${t.name} onComment=${n}/>`):U`<div class="cc-sdone">no active sessions</div>`}
        </div>
      `}
    </div>
  `}function Cs(){if(St.value)return U`<${xs}/>`;let t=Q.value,s=B.value,e=D.value,[n,o]=Pt(""),[a,i]=Pt("maestro"),[l,d]=Pt(!1),[r,c]=Pt("ready"),[h,v]=Pt(-1),x=Zn([]),w=t&&Array.isArray(t.conductors)?t.conductors:[];x.current=w;let m=g=>{St.value=g};Qn(()=>{let g=p=>{let F=document.activeElement?.tagName==="TEXTAREA"||document.activeElement?.tagName==="INPUT"||document.activeElement?.tagName==="SELECT";if(p.key==="/"&&!F){p.preventDefault(),document.querySelector(".cc-input textarea")?.focus();return}if(p.key==="Escape"&&F){document.activeElement.blur();return}if(F)return;let W=x.current;if(p.key==="ArrowDown")p.preventDefault(),v(X=>Math.min(W.length-1,X+1));else if(p.key==="ArrowUp")p.preventDefault(),v(X=>Math.max(0,X-1));else if(p.key==="Enter"&&h>=0&&W[h])p.preventDefault(),m(W[h].name);else if(/^[1-9]$/.test(p.key)){let X=parseInt(p.key,10)-1;W[X]&&(p.preventDefault(),v(X),m(W[X].name))}};return window.addEventListener("keydown",g),()=>window.removeEventListener("keydown",g)},[h]);let A=g=>{let p=re(g);o(p.text),p.target&&i(p.target),document.querySelector(".cc-input textarea")?.focus()},f=async()=>{let g=n.trim();if(!(!g||l)){if(!e){I("Two-way input is disabled (web mutations off)","info");return}d(!0),c("sending\u2026");try{await le({text:g,target:a}),c("\u2713 routed to "+a),o("")}catch(p){c("\u2717 "+(p.message||"send failed"))}finally{d(!1)}}},u=g=>{g.key==="Enter"&&(g.ctrlKey||g.metaKey)&&(g.preventDefault(),f())},b=t&&Array.isArray(t.askTargets)?t.askTargets:["maestro"];if(!t)return U`
      <div class="cc" data-testid="command-center-pane">
        <div class="cc-top">
          <h1>Command Center</h1>
          <span class=${`cc-live ${s==="connected"?"":"stale"}`}>
            ${s==="connected"?"\u25CF connecting\u2026":"\u25CF offline"}
          </span>
        </div>
        <div class="cc-empty" data-testid="cc-loading">Waiting for the first fleet snapshot…</div>
      </div>
    `;let $=Array.isArray(t.decisionsWaiting)?t.decisionsWaiting:[],E=t.totals||{};return U`
    <div class="cc" data-testid="command-center-pane">
      <div class="cc-top">
        <h1>Command Center</h1>
        <span class=${`cc-live ${s==="connected"?"":"stale"}`} data-testid="cc-live">
          ${s==="connected"?"\u25CF live":"\u25CF offline"}
        </span>
        <span class="cc-hint">↑↓ move · Enter / 1–9 open · / type · Esc back</span>
        <span class="cc-totals" data-testid="cc-totals">
          ${E.running||0} running · ${E.waiting||0} waiting · ${E.idle||0} idle
        </span>
      </div>

      <div class="cc-cols">
        <div class="cc-col">
          <h2>👉 Needs you</h2>
          ${$.length?$.map((g,p)=>U`<${sa} key=${g.id||p} decision=${g} onComment=${A}/>`):U`<div class="cc-sdone" data-testid="cc-no-decisions">nothing waiting on you 🎉</div>`}
          <${ce}/>
        </div>

        <div class="cc-col">
          <h2>🛰️ The fleet — what each is doing</h2>
          ${w.length?w.map((g,p)=>U`<${aa} key=${g.name} cd=${g} index=${p}
                focused=${p===h} onComment=${A} onOpen=${m}/>`):U`<div class="cc-sdone" data-testid="cc-no-conductors">no conductors detected</div>`}
        </div>
      </div>

      <div class="cc-input" data-testid="cc-input">
        <select value=${a} onChange=${g=>i(g.target.value)} title="Route to" data-testid="cc-target">
          ${b.map(g=>U`<option key=${g} value=${g==="conductor-maestro"?"maestro":g}>
            ${g==="conductor-maestro"||g==="maestro"?"Maestro (default)":g}
          </option>`)}
        </select>
        <textarea
          placeholder="answer a decision, comment, or instruct… ⌘/Ctrl+Enter to send"
          value=${n}
          onInput=${g=>o(g.target.value)}
          onKeyDown=${u}></textarea>
        <button class="cc-send" disabled=${!n.trim()||l} onClick=${f} data-testid="cc-send">➤ Send</button>
        <span class=${`cc-st ${r.startsWith("\u2713")?"ok":r.startsWith("\u2717")?"err":""}`} data-testid="cc-status">${r}</span>
      </div>
    </div>
  `}import{html as Ft}from"htm/preact";import{useState as gt,useEffect as Os}from"preact/hooks";import{html as R}from"htm/preact";var oa=new Set(["col","row","grid","stack","section","text","heading"]),ia=new Set(["status-list","session-list","conductor-card","decision-list","stat"]),ue={sm:"8px",md:"16px",lg:"28px"},Ts={ok:"genui-tone-ok",warn:"genui-tone-warn",danger:"genui-tone-danger",info:"genui-tone-info",neutral:"genui-tone-neutral","":"genui-tone-neutral"},Ae={running:"\u{1F7E2}",waiting:"\u{1F7E1}",idle:"\u26AA",error:"\u{1F534}",stopped:"\u26AB",absent:"\u26AB"};function ft(t,s){if(!t)return;let e=s;for(let n of String(t).split(".")){if(e==null||typeof e!="object")return;e=e[n]}return e}function pe(t){return R`<div class="genui-error" data-testid="genui-error">⚠️ ${t}</div>`}function Ee(t,s,e,n){if(e>14)return pe("max render depth exceeded");if(!t||typeof t!="object")return pe("invalid node");let o=t.type;if(t.when&&!ft(t.when,s))return null;if(t.repeat){let a=ft(t.repeat.over,s);if(!Array.isArray(a))return null;let i=t.repeat.as||"item",d=a.slice(0,200).map((r,c)=>{let h=Object.assign({},s,{[i]:r});return Ee(t.repeat.template,h,e+1,`${n}-r${c}`)});return Es(t,d,s,n)}return oa.has(o)?la(t,s,e,n):ia.has(o)?ra(t,s,n):R`<div class="genui-unknown" data-testid="genui-unknown" key=${n}>unsupported widget: ${String(o)}</div>`}function As(t,s,e,n){return(Array.isArray(t.children)?t.children:[]).map((a,i)=>Ee(a,s,e+1,`${n}-${i}`))}function Es(t,s,e,n){let o=ue[t.gap]||ue.md;if(t.type==="grid"){let i=t.cols&&t.cols>=1&&t.cols<=6?t.cols:2;return R`<div class="genui-grid" key=${n}
      style=${{display:"grid",gridTemplateColumns:`repeat(${i}, minmax(0,1fr))`,gap:o}}>${s}</div>`}let a=t.type==="row"?"row":"column";return R`<div class="genui-stack" key=${n}
    style=${{display:"flex",flexDirection:a,gap:o}}>${s}</div>`}function la(t,s,e,n){let o=t.type;if(o==="text")return R`<div class="genui-text" key=${n}>${t.text||""}</div>`;if(o==="heading"){let a=t.level>=1&&t.level<=3?t.level:2,i=`genui-h genui-h${a}`;return a===1?R`<h1 class=${i} key=${n}>${t.text||""}</h1>`:a===3?R`<h3 class=${i} key=${n}>${t.text||""}</h3>`:R`<h2 class=${i} key=${n}>${t.text||""}</h2>`}if(o==="section"){let a=ue[t.gap]||ue.md;return R`<section class="genui-section" key=${n}>
      ${t.text&&R`<h2 class="genui-section-title">${t.text}</h2>`}
      <div class="genui-stack" style=${{display:"flex",flexDirection:"column",gap:a}}>
        ${As(t,s,e,n)}
      </div>
    </section>`}return Es(t,As(t,s,e,n),s,n)}function ra(t,s,e){let n=t.type;if(n==="stat"){let o=ft(t.bind,s),a=o??"\u2014";return R`<div class=${`genui-stat ${Ts[t.tone]||Ts[""]}`}
      data-testid="genui-stat" key=${e}>
      <div class="genui-stat-val">${a}</div>
      <div class="genui-stat-lbl">${t.label||""}</div>
    </div>`}if(n==="conductor-card"){let o=ft(t.bind,s)||{},a=o.name==="maestro"&&o.status==="running"?"\u{1F535}":Ae[o.status]||"\u26AA";return R`<div class="genui-card" data-testid="genui-conductor-card" data-name=${o.name||""} key=${e}>
      <span class="genui-card-dot">${a}</span>
      <span class="genui-card-name">${o.name||"\u2014"}</span>
      <span class="genui-card-work">${o.currentlyWorkingOn||o.status||""}</span>
    </div>`}if(n==="status-list"){let o=ft(t.bind,s),a=Array.isArray(o)?o:[];return R`<div class="genui-list" data-testid="genui-status-list" key=${e}>
      ${a.length?a.map((i,l)=>{let d=i.name==="maestro"&&i.status==="running"?"\u{1F535}":Ae[i.status]||"\u26AA",r=i.counts||{},c=["running","waiting","idle"].filter(h=>r[h]).map(h=>`${r[h]} ${h}`).join(" \xB7 ");return R`<div class="genui-row" data-testid="genui-status-row" data-name=${i.name} key=${"s"+l}>
          <span class="genui-row-dot">${d}</span>
          <span class="genui-row-name">${i.name}</span>
          <span class="genui-row-work">${i.currentlyWorkingOn||i.status||""}</span>
          <span class="genui-row-live">${c}</span>
        </div>`}):R`<div class="genui-empty">no conductors</div>`}
    </div>`}if(n==="session-list"){let o=ft(t.bind,s),a=Array.isArray(o)?o:[];return R`<div class="genui-list" data-testid="genui-session-list" key=${e}>
      ${a.length?a.map((i,l)=>R`
        <div class="genui-srow" data-testid="genui-session-row" data-status=${i.status} key=${"x"+l}>
          <span class="genui-row-dot">${Ae[i.status]||"\u26AA"}</span>
          <span class="genui-row-name">${i.title}</span>
          <span class="genui-row-work">${i.workingOn||""}</span>
        </div>`):R`<div class="genui-empty">no active sessions</div>`}
    </div>`}if(n==="decision-list"){let o=ft(t.bind,s),a=Array.isArray(o)?o:[];return R`<div class="genui-list" data-testid="genui-decision-list" key=${e}>
      ${a.length?a.map((i,l)=>R`
        <div class="genui-drow" data-testid="genui-decision-row" key=${"d"+l}>
          ${i.id&&R`<span class="genui-drow-id">${i.id}</span>`}
          <span class="genui-drow-q">${i.question}</span>
        </div>`):R`<div class="genui-empty">nothing waiting on you 🎉</div>`}
    </div>`}return pe("unhandled widget "+n)}function Is(t,s){if(!t||typeof t!="object"||!t.root)return pe("no spec");let e=s||{};return R`<div class="genui-root" data-testid="genui-root" data-spec-id=${t.specId||""}>
    ${Ee(t.root,e,1,"root")}
  </div>`}async function ca(t){let s={Accept:"application/json"},e=K.value;e&&(s.Authorization="Bearer "+e);let n=await fetch(t,{headers:s});if(!n.ok)throw new Error("HTTP "+n.status);return n.json()}async function da(t){let s={Accept:"application/json","Content-Type":"application/json"},e=K.value;e&&(s.Authorization="Bearer "+e);let n=await fetch("/api/command-center/genui/compose",{method:"POST",headers:s,body:JSON.stringify({intent:t})}),o=null;try{o=await n.json()}catch{}if(!n.ok){let a=o&&o.error?o.error:"HTTP "+n.status;throw new Error(a)}return o}async function ua(t){let s={Accept:"application/json"},e=K.value;e&&(s.Authorization="Bearer "+e);let n=await fetch("/api/command-center/genui/spec/"+encodeURIComponent(t),{headers:s});if(!n.ok)throw new Error("HTTP "+n.status);return n.json()}function pa(t){if(!t)return{totals:{},conductors:[],sessions:[],decisionsWaiting:[],stuckSessions:[]};let s=Array.isArray(t.conductors)?t.conductors:[],e=[],n=[];for(let o of s)for(let a of o.sessions||[])e.push(a),(a.status==="error"||a.status==="stopped")&&n.push(a);return{totals:t.totals||{},conductors:s,sessions:e,stuckSessions:n,decisionsWaiting:Array.isArray(t.decisionsWaiting)?t.decisionsWaiting:[]}}function Ps(){let t=Q.value,s=B.value,[e,n]=gt([]),[o,a]=gt(null),[i,l]=gt(null),[d,r]=gt(""),[c,h]=gt(""),[v,x]=gt(!1),[w,m]=gt(null);Os(()=>{ca("/api/command-center/genui/views").then(b=>{let $=b&&Array.isArray(b.views)?b.views:[];n($),$.length&&!o&&a($[0].id)}).catch(b=>r("Could not load views: "+b.message))},[]),Os(()=>{o&&(l(null),ua(o).then(b=>{l(b),r(""),m(null)}).catch(b=>r("Could not load spec: "+b.message)))},[o]);function A(b){a(b),m(null),r("")}function f(){let b=c.trim();!b||v||(x(!0),r(""),da(b).then($=>{l($.spec),a(null),m($.trace||null)}).catch($=>{r("Could not compose that view: "+$.message),m(null)}).finally(()=>x(!1)))}let u=pa(t);return Ft`
    <div class="genui-pane" data-testid="genui-pane">
      <div class="genui-bar">
        <h1 class="genui-bar-title">Generative Command Center</h1>
        <span class=${`genui-live ${s==="connected"?"":"stale"}`} data-testid="genui-live">
          ${s==="connected"?"\u25CF live":"\u25CF offline"}
        </span>
        <span class="genui-bar-hint">type an intent → the LLM emits a validated spec → it renders</span>
        <div class="genui-switch" data-testid="genui-switch">
          ${e.map(b=>Ft`
            <button key=${b.id}
              class=${`genui-switch-btn ${b.id===o?"active":""}`}
              data-testid=${"genui-view-"+b.id}
              data-active=${b.id===o?"true":"false"}
              onClick=${()=>A(b.id)}>${b.title}</button>
          `)}
        </div>
      </div>
      <form class="genui-intent" data-testid="genui-intent"
        onSubmit=${b=>{b.preventDefault(),f()}}>
        <input class="genui-intent-input" type="text"
          data-testid="genui-intent-input"
          placeholder="e.g. show me what's blocked · group everything by project · just the conductors"
          value=${c}
          disabled=${v}
          onInput=${b=>h(b.currentTarget.value)} />
        <button class="genui-intent-go" type="submit"
          data-testid="genui-intent-go"
          disabled=${v||!c.trim()}>
          ${v?"Composing\u2026":"Compose"}
        </button>
      </form>
      ${w&&Ft`
        <div class="genui-trace" data-testid="genui-trace">
          ✨ composed by <strong>${w.composer}</strong> · ${w.tries} ${w.tries===1?"try":"tries"}${w.repaired?" \xB7 repaired after validation":""}
        </div>`}
      <div class="genui-body" data-testid="genui-body">
        ${d&&Ft`<div class="genui-error" data-testid="genui-load-error">⚠️ ${d}</div>`}
        ${!d&&!i&&Ft`<div class="genui-loading" data-testid="genui-loading">Loading view…</div>`}
        ${!d&&i&&Is(i,u)}
      </div>
    </div>
  `}import{html as Ie}from"htm/preact";import{useState as va,useMemo as Ds,useEffect as ma}from"preact/hooks";function fa(t){let s=t||{};return{id:s.id||"",title:s.title||s.id,tool:s.tool||"",status:s.status||"idle",group:s.groupPath||"",path:s.projectPath||"",archivedAt:s.archivedAt||null}}function ga(t){if(!t)return"\u2014";try{return new Date(t).toLocaleString(void 0,{dateStyle:"medium",timeStyle:"short"})}catch{return String(t)}}function Ms(t){T.value===t&&(T.value=null,typeof window<"u"&&window.location.pathname.startsWith("/s/")&&history.replaceState(null,"","/"))}function Ns(){let t=Bt.value||[],[s,e]=va("");ma(()=>{Xt()},[]);let n=Ds(()=>t.map(fa),[t]),o=Ds(()=>{if(!s)return n;let l=s.toLowerCase();return n.filter(d=>((d.title||"")+" "+(d.path||"")+" "+(d.tool||"")+" "+(d.group||"")).toLowerCase().includes(l))},[n,s]),a=l=>{if(!D.value){I("mutations disabled");return}y("POST",`/api/sessions/${l.id}/unarchive`).then(()=>{Ms(l.id),Xt(),I(`Unarchived "${l.title}"`,"success")}).catch(()=>{})},i=l=>{if(!D.value){I("mutations disabled");return}H.value={message:`Delete archived session "${l.title}"? This removes it permanently.`,onConfirm:()=>y("DELETE",`/api/sessions/${l.id}`).then(()=>{Ms(l.id),Xt()}).catch(()=>{})}};return Ie`
    <div class="search-wrap archived-wrap">
      <div class="field">
        <label>ARCHIVED SESSIONS</label>
        <input placeholder="Filter by title, path, tool, group…"
               value=${s} onInput=${l=>e(l.target.value)}/>
      </div>
      <div style="font-family: var(--mono); font-size: 10.5px; color: var(--muted); letter-spacing: 0.08em;">
        ${o.length} ARCHIVED · unarchive to return to the active list
      </div>
      ${o.length===0&&Ie`
        <div class="archived-empty">No archived sessions.</div>
      `}
      ${o.map(l=>Ie`
        <div key=${l.id} class="sr archived-row">
          <div class="sr-h">
            <${Kt} status=${l.status}/>
            <span class="s">${l.title}</span>
            <span class="w">${l.tool||"\u2014"} · archived ${ga(l.archivedAt)}</span>
          </div>
          <div class="sr-b">${l.path||l.group||""}</div>
          <div class="archived-actions" onClick=${d=>d.stopPropagation()}>
            <button class="mini good" title="Unarchive" onClick=${()=>a(l)}>Unarchive</button>
            <button class="mini danger" title="Delete" onClick=${()=>i(l)}>Delete</button>
          </div>
        </div>
      `)}
    </div>
  `}import{html as Ls}from"htm/preact";function Oe({title:t,message:s,hotkey:e}){return Ls`
    <div class="costs">
      <div class="chart-card" style="display: flex; flex-direction: column; align-items: center; justify-content: center; text-align: center; gap: 14px; padding: 48px 24px; min-height: 320px;">
        <${bt}/>
        <div class="title" style="font-size: 16px;">${t}</div>
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); line-height: 1.6; max-width: 460px;">
          ${s}
        </div>
        <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); padding-top: 8px;">
          No data yet — see TUI for now${e?" ":"."}
          ${e&&Ls`<span class="kbd" style="border:1px solid var(--border); padding: 1px 6px; border-radius: 3px; color: var(--text); margin-left: 4px;">${e}</span>`}
        </div>
      </div>
    </div>
  `}import{html as Rs}from"htm/preact";import{useState as ha,useMemo as $a}from"preact/hooks";function zs(){let{sessions:t}=L.value,[s,e]=ha(""),n=$a(()=>{if(!s)return t;let a=s.toLowerCase();return t.filter(i=>((i.title||"")+" "+(i.path||"")+" "+(i.tool||"")+" "+(i.group||"")).toLowerCase().includes(a))},[t,s]),o=a=>{T.value=a,M.value="terminal"};return Rs`
    <div class="search-wrap" data-testid="search-pane">
      <div class="field">
        <label>SESSION SEARCH</label>
        <input autofocus placeholder="Search sessions by title, path, tool, group…"
               data-testid="search-input"
               value=${s} onInput=${a=>e(a.target.value)}/>
      </div>
      <div data-testid="search-result-count" style="font-family: var(--mono); font-size: 10.5px; color: var(--muted); letter-spacing: 0.08em;">
        ${n.length} MATCH${n.length===1?"":"ES"} · cross-profile search not exposed via web API
      </div>
      ${n.map(a=>Rs`
        <div key=${a.id} class="sr" data-testid="search-result" data-session-id=${a.id} onClick=${()=>o(a.id)}>
          <div class="sr-h">
            <span class="s">${a.title}</span>
            <span class="w">${a.tool||"\u2014"} · ${a.status}</span>
          </div>
          <div class="sr-b">${a.path||a.group||""}</div>
        </div>
      `)}
    </div>
  `}import{html as J}from"htm/preact";import{useEffect as ba,useState as ve,useCallback as ya}from"preact/hooks";var me=["local","global","user"];async function _t(t,s={}){let e=await fetch(t,{headers:{"Content-Type":"application/json"},...s});if(!e.ok){let n=`${e.status} ${e.statusText}`;try{let o=await e.json();o&&o.error&&(n=o.error)}catch{}throw new Error(n)}return e.status===204?null:e.json()}function Fs(){let{sessions:t}=L.value,s=T.value,e=D.value,n=t.find(f=>f.id===s),[o,a]=ve([]),[i,l]=ve({local:[],global:[],user:[]}),[d,r]=ve(!1),[c,h]=ve(""),v=ya(async()=>{if(n){r(!0),h("");try{let[f,u]=await Promise.all([_t("/api/mcps"),_t(`/api/sessions/${encodeURIComponent(n.id)}/mcps`)]);a(f.mcps||[]),l({local:u.local||[],global:u.global||[],user:u.user||[]})}catch(f){h(f.message)}finally{r(!1)}}},[n&&n.id]);ba(()=>{v()},[v]);let x=f=>{for(let u of me)if(i[u].includes(f))return u;return null},w=async(f,u)=>{if(n)try{await _t(`/api/sessions/${encodeURIComponent(n.id)}/mcps/${encodeURIComponent(f)}`,{method:"POST",body:JSON.stringify({scope:u})}),I(`Attached ${f} (${u})`,"success"),await v()}catch(b){I(`Attach failed: ${b.message}`,"error")}},m=async f=>{if(!n)return;let u=x(f);try{await _t(`/api/sessions/${encodeURIComponent(n.id)}/mcps/${encodeURIComponent(f)}`,{method:"DELETE",body:u?JSON.stringify({scope:u}):""}),I(`Detached ${f}`,"success"),await v()}catch(b){I(`Detach failed: ${b.message}`,"error")}},A=async(f,u)=>{if(n)try{await _t(`/api/sessions/${encodeURIComponent(n.id)}/mcps/${encodeURIComponent(f)}`,{method:"PATCH",body:JSON.stringify({scope:u})}),I(`Moved ${f} \u2192 ${u}`,"success"),await v()}catch(b){I(`Move failed: ${b.message}`,"error")}};return n?J`
    <div class="costs" data-testid="mcp-pane">
      <div class="chart-card" style="padding: 24px;">
        <div class="title" style="font-size: 16px; margin-bottom: 4px;">MCP Manager</div>
        <div style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); margin-bottom: 16px;">
          ${n.title} · ${n.path||""}
        </div>

        ${c&&J`
          <div style="font-family: var(--mono); font-size: 11px; color: var(--err); background: var(--err-bg); padding: 8px 12px; border-radius: 4px; margin-bottom: 12px;" data-testid="mcp-error">
            ${c}
          </div>
        `}

        <div style="display: grid; grid-template-columns: 1fr; gap: 24px;">
          <${ka}
            attached=${i}
            mutationsEnabled=${e}
            onDetach=${m}
            onMove=${A}/>

          <${xa}
            catalog=${o}
            attached=${i}
            mutationsEnabled=${e}
            onAttach=${w}
            loading=${d}/>
        </div>
      </div>
    </div>
  `:J`
      <div class="costs">
        <div class="chart-card" style="text-align: center; padding: 48px 24px;">
          <div class="title" style="font-size: 16px;">MCP Manager</div>
          <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding-top: 8px;">
            Select a session in the sidebar to manage MCPs.
          </div>
        </div>
      </div>
    `}function ka({attached:t,mutationsEnabled:s,onDetach:e,onMove:n}){let o=me.flatMap(a=>t[a].map(i=>({name:i,scope:a})));return J`
    <div data-testid="mcp-attached">
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); letter-spacing: 0.08em; margin-bottom: 8px;">
        ATTACHED (${o.length})
      </div>
      ${o.length===0&&J`
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding: 12px;">
          No MCPs attached. Use the catalog below to attach.
        </div>
      `}
      ${o.map(({name:a,scope:i})=>J`
        <div key=${`${i}-${a}`} data-testid=${`mcp-attached-${a}`}
             style="display: flex; align-items: center; justify-content: space-between; padding: 8px 12px; border: 1px solid var(--border); border-radius: 4px; margin-bottom: 6px;">
          <div>
            <span style="font-family: var(--mono); font-size: 13px; color: var(--text);">${a}</span>
            <span style="font-family: var(--mono); font-size: 10px; color: var(--muted); margin-left: 8px; letter-spacing: 0.08em;">
              ${i.toUpperCase()}
            </span>
          </div>
          <div style="display: flex; gap: 6px;">
            <select disabled=${!s}
                    data-testid=${`mcp-scope-${a}`}
                    value=${i}
                    onChange=${l=>n(a,l.target.value)}
                    style="font-family: var(--mono); font-size: 11px; background: var(--bg); color: var(--text); border: 1px solid var(--border); padding: 2px 6px; border-radius: 3px;">
              ${me.map(l=>J`<option value=${l} key=${l}>${l}</option>`)}
            </select>
            <button disabled=${!s}
                    data-testid=${`mcp-detach-${a}`}
                    onClick=${()=>e(a)}
                    style="font-family: var(--mono); font-size: 11px; background: transparent; color: var(--err); border: 1px solid var(--err); padding: 2px 8px; border-radius: 3px; cursor: pointer;">
              Detach
            </button>
          </div>
        </div>
      `)}
    </div>
  `}function xa({catalog:t,attached:s,mutationsEnabled:e,onAttach:n,loading:o}){let a=i=>me.some(l=>s[l].includes(i));return J`
    <div data-testid="mcp-catalog">
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); letter-spacing: 0.08em; margin-bottom: 8px;">
        CATALOG (${t.length})
      </div>
      ${o&&J`<div style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); padding: 8px;">Loading…</div>`}
      ${!o&&t.length===0&&J`
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); padding: 12px;">
          No MCPs in the catalog. Add some to <code>~/.agent-deck/config.toml</code>.
        </div>
      `}
      ${t.map(i=>{let l=a(i.name);return J`
          <div key=${i.name} data-testid=${`mcp-catalog-${i.name}`}
               style="display: flex; align-items: center; justify-content: space-between; padding: 8px 12px; border: 1px solid var(--border); border-radius: 4px; margin-bottom: 6px;">
            <div style="display: flex; flex-direction: column;">
              <span style="font-family: var(--mono); font-size: 13px; color: var(--text);">${i.name}</span>
              ${i.description&&J`<span style="font-family: var(--mono); font-size: 11px; color: var(--text-dim); margin-top: 2px;">${i.description}</span>`}
              <span style="font-family: var(--mono); font-size: 10px; color: var(--muted); margin-top: 2px; letter-spacing: 0.06em;">
                ${(i.transport||"stdio").toUpperCase()}${i.command?` \xB7 ${i.command}`:""}
              </span>
            </div>
            <button disabled=${!e||l}
                    data-testid=${`mcp-attach-${i.name}`}
                    onClick=${()=>n(i.name,"local")}
                    style="font-family: var(--mono); font-size: 11px; background: ${l?"transparent":"var(--accent)"}; color: ${l?"var(--muted)":"var(--bg)"}; border: 1px solid ${l?"var(--border)":"var(--accent)"}; padding: 4px 12px; border-radius: 3px; cursor: ${l?"default":"pointer"};">
              ${l?"Attached":"Attach"}
            </button>
          </div>
        `})}
    </div>
  `}import{html as et}from"htm/preact";import{useEffect as wa,useState as fe}from"preact/hooks";var Sa=new Set(["claude","gemini","codex","pi"]);function _s(){let t=T.value,{sessions:s}=L.value,e=s.find(f=>f.id===t),[n,o]=fe([]),[a,i]=fe([]),[l,d]=fe(!1),[r,c]=fe("");async function h(){d(!0);try{let f=await y("GET","/api/skills");if(o(f?.skills||[]),e){let u=await y("GET",`/api/sessions/${encodeURIComponent(e.id)}/skills`);i(u?.skills||[])}else i([])}catch(f){I("Failed to load skills: "+(f.message||"request failed"))}finally{d(!1)}}if(wa(()=>{h()},[e&&e.id]),!e)return et`
      <div class="costs">
        <div class="chart-card" style="padding: 32px; text-align: center;">
          <div class="title">No session selected</div>
          <div style="color: var(--text-dim); margin-top: 12px;">
            Pick a session from the sidebar to manage its skills.
          </div>
        </div>
      </div>`;if(!Sa.has((e.tool||"").toLowerCase()))return et`
      <div class="costs">
        <div class="chart-card" style="padding: 32px; text-align: center;">
          <div class="title">Skills not supported for ${e.tool}</div>
          <div style="color: var(--text-dim); margin-top: 12px;">
            Project-scoped skills are available for Claude, Gemini, Codex, and Pi sessions only.
          </div>
        </div>
      </div>`;let x=new Set(a.map(f=>f.id)),w=n.filter(f=>!x.has(f.id)&&(f.kind||"dir")==="dir");async function m(f){if(!r){c(f.id);try{let u=`/api/sessions/${encodeURIComponent(e.id)}/skills/${encodeURIComponent(f.name)}?source=${encodeURIComponent(f.source)}`;await y("POST",u),I(`Attached ${f.name}`),await h()}catch{}finally{c("")}}}async function A(f){if(!r){c(f.id);try{let u=`/api/sessions/${encodeURIComponent(e.id)}/skills/${encodeURIComponent(f.name)}?source=${encodeURIComponent(f.source)}`;await y("DELETE",u),I(`Detached ${f.name}`),await h()}catch{}finally{c("")}}}return et`
    <div class="skills-pane" data-testid="skills-pane" style="padding: 16px; display: flex; flex-direction: column; gap: 16px; height: 100%; overflow: auto;">
      <div style="display: flex; justify-content: space-between; align-items: center;">
        <div class="title" style="font-size: 14px;">Skills · ${e.title}</div>
        <button class="btn" data-testid="skills-refresh" onClick=${h} disabled=${l}>${l?"Loading\u2026":"Refresh"}</button>
      </div>

      <section data-testid="skills-attached" style="border: 1px solid var(--border); border-radius: 6px; padding: 12px;">
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); margin-bottom: 8px;">
          ATTACHED (${a.length})
        </div>
        ${a.length===0?et`<div data-testid="skills-attached-empty" style="color: var(--muted); font-size: 12px;">No skills attached.</div>`:et`<ul style="list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px;">
              ${a.map(f=>et`
                <li data-testid="skill-attached-row" data-skill-id=${f.id} style="display: flex; justify-content: space-between; gap: 8px; align-items: center; padding: 6px 8px; background: var(--surface); border-radius: 4px;">
                  <span><strong>${f.name}</strong> <span style="color: var(--muted); font-size: 11px;">${f.source}</span></span>
                  <button class="btn btn-danger" data-testid="skill-detach-btn" disabled=${r===f.id} onClick=${()=>A(f)}>Detach</button>
                </li>`)}
            </ul>`}
      </section>

      <section data-testid="skills-catalog" style="border: 1px solid var(--border); border-radius: 6px; padding: 12px;">
        <div style="font-family: var(--mono); font-size: 12px; color: var(--text-dim); margin-bottom: 8px;">
          CATALOG (${w.length})
        </div>
        ${w.length===0?et`<div data-testid="skills-catalog-empty" style="color: var(--muted); font-size: 12px;">No additional skills available to attach.</div>`:et`<ul style="list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 6px;">
              ${w.map(f=>et`
                <li data-testid="skill-catalog-row" data-skill-id=${f.id} style="display: flex; justify-content: space-between; gap: 8px; align-items: center; padding: 6px 8px;">
                  <span>
                    <strong>${f.name}</strong>
                    <span style="color: var(--muted); font-size: 11px;"> ${f.source}</span>
                    ${f.description&&et`<div style="color: var(--text-dim); font-size: 11px;">${f.description}</div>`}
                  </span>
                  <button class="btn" data-testid="skill-attach-btn" disabled=${r===f.id} onClick=${()=>m(f)}>Attach</button>
                </li>`)}
            </ul>`}
      </section>
    </div>
  `}import{html as ct}from"htm/preact";import{useState as ht}from"preact/hooks";var Gs=["claude","codex","gemini","opencode","shell"],Ca={codex:"ChatGPT"};function ge(t){return Ca[t]||t}function Ta(t){return t.filter((s,e,n)=>n.indexOf(s)===e)}function Us(t){let s=t.length>0?t:Gs;return Ta(s)}function js(t,s){let n=[...t.length>0?t:Gs];return s&&!n.includes(s)&&n.push(s),n}var Pe="__custom__",Aa={claude:[{value:"claude-sonnet-4-6",label:"Claude Sonnet 4.6"},{value:"claude-opus-4-8",label:"Claude Opus 4.8"},{value:"claude-opus-4-7",label:"Claude Opus 4.7"},{value:"claude-haiku-4-5",label:"Claude Haiku 4.5 alias"},{value:"claude-haiku-4-5-20251001",label:"Claude Haiku 4.5 pinned"}],codex:[{value:"gpt-5.5",label:"GPT-5.5"},{value:"gpt-5.5-pro",label:"GPT-5.5 Pro"},{value:"gpt-5.4",label:"GPT-5.4"},{value:"gpt-5.4-pro",label:"GPT-5.4 Pro"},{value:"gpt-5.4-mini",label:"GPT-5.4 Mini"},{value:"gpt-5.4-nano",label:"GPT-5.4 Nano"},{value:"gpt-5.3-codex",label:"GPT-5.3 Codex"},{value:"gpt-5.2",label:"GPT-5.2"},{value:"gpt-5.2-pro",label:"GPT-5.2 Pro"},{value:"gpt-5.1",label:"GPT-5.1"},{value:"gpt-5-pro",label:"GPT-5 Pro"},{value:"gpt-5",label:"GPT-5"},{value:"gpt-5-mini",label:"GPT-5 Mini"},{value:"gpt-5-nano",label:"GPT-5 Nano"},{value:"gpt-4.1",label:"GPT-4.1"},{value:"gpt-4.1-mini",label:"GPT-4.1 Mini"},{value:"gpt-4o",label:"GPT-4o"},{value:"gpt-4o-mini",label:"GPT-4o Mini"},{value:"o3-pro",label:"o3 Pro"},{value:"o3",label:"o3"}],gemini:[{value:"gemini-3.1-pro-preview",label:"Gemini 3.1 Pro preview"},{value:"gemini-3.1-pro-preview-customtools",label:"Gemini 3.1 Pro custom tools"},{value:"gemini-3-flash-preview",label:"Gemini 3 Flash preview"},{value:"gemini-3.1-flash-lite",label:"Gemini 3.1 Flash Lite"},{value:"gemini-3.1-flash-lite-preview",label:"Gemini 3.1 Flash Lite preview"},{value:"gemini-2.5-pro",label:"Gemini 2.5 Pro"},{value:"gemini-2.5-flash",label:"Gemini 2.5 Flash"},{value:"gemini-2.5-flash-lite",label:"Gemini 2.5 Flash Lite"}],opencode:[{value:"openai/gpt-5.5",label:"OpenAI GPT-5.5"},{value:"openai/gpt-5.5-pro",label:"OpenAI GPT-5.5 Pro"},{value:"openai/gpt-5.4",label:"OpenAI GPT-5.4"},{value:"openai/gpt-5.4-pro",label:"OpenAI GPT-5.4 Pro"},{value:"openai/gpt-5.4-mini",label:"OpenAI GPT-5.4 Mini"},{value:"openai/gpt-5.3-codex",label:"OpenAI GPT-5.3 Codex"},{value:"openai/gpt-5",label:"OpenAI GPT-5"},{value:"openai/o3",label:"OpenAI o3"},{value:"anthropic/claude-sonnet-4-6",label:"Anthropic Claude Sonnet 4.6"},{value:"anthropic/claude-opus-4-8",label:"Anthropic Claude Opus 4.8"},{value:"anthropic/claude-opus-4-7",label:"Anthropic Claude Opus 4.7"},{value:"anthropic/claude-haiku-4-5",label:"Anthropic Claude Haiku 4.5"}]};function Ea(t){return Aa[t]||[]}function Ws(){let[t,s]=ht(""),[e,n]=ht("claude"),[o,a]=ht(""),[i,l]=ht(""),[d,r]=ht(""),[c,h]=ht(null),[v,x]=ht(!1);if(!D.value)return null;async function w(p){p.preventDefault(),h(null),x(!0);try{let F={title:t,tool:e,projectPath:d},W=A();W&&(F.modelId=W),await y("POST","/api/sessions",F),j.value=!1}catch(F){h(F.message)}finally{x(!1)}}function m(p){n(p),a(""),l("")}function A(){return o===Pe?i.trim():o||""}let f=()=>j.value=!1,u=p=>{p.target===p.currentTarget&&f()},b=Ea(e),$=Us(wt.value),E=o===Pe,g=v||!t||!d||E&&!i.trim();return ct`
    <div class="overlay" onClick=${u}>
      <form class="dialog" onClick=${p=>p.stopPropagation()} onSubmit=${w}>
        <div class="dh">
          <span class="kicker">NEW</span>
          <div class="t">New session</div>
          <button type="button" class="icon-btn" onClick=${f} aria-label="Close">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>TITLE</label>
            <input autofocus required value=${t} onInput=${p=>s(p.target.value)} placeholder="my-session"/>
          </div>
          <div class="field">
            <label>WORKING DIR</label>
            <input required value=${d} onInput=${p=>r(p.target.value)} placeholder="/absolute/path/to/project"/>
          </div>
          <div class="field">
            <label>TOOL</label>
            <div class="seg-row">
              ${$.map(p=>ct`
                <button type="button" key=${p}
                        class=${`seg-btn ${e===p?"on":""}`}
                        onClick=${()=>m(p)}>${ge(p)}</button>
              `)}
            </div>
            ${Vt.value&&ct`
              <div style="font-family: var(--mono); font-size: 11px; color: var(--tn-comment, #888);
                          margin-top: 6px;">
                No tools matched PATH; showing all. Set <code>show_only_installed_tools = false</code> to silence.
              </div>
            `}
          </div>
          ${b.length>0&&ct`
            <div class="field">
              <label>MODEL ID</label>
              <select value=${o} onInput=${p=>a(p.target.value)}>
                <option value="">Tool default</option>
                ${b.map(p=>ct`
                  <option key=${p.value} value=${p.value}>${p.value} — ${p.label}</option>
                `)}
                <option value=${Pe}>Custom model ID…</option>
              </select>
            </div>
            ${E&&ct`
              <div class="field">
                <label>MODEL ID</label>
                <input required value=${i} onInput=${p=>l(p.target.value)} placeholder="provider/model-or-version"/>
              </div>
            `}
          `}
          ${c&&ct`
            <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${c}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${f}>Cancel</button>
          <button type="submit" class="btn primary" disabled=${g}>
            ${v?"Creating\u2026":ct`Create session <span class="kbd">⏎</span>`}
          </button>
        </div>
      </form>
    </div>
  `}import{html as Gt}from"htm/preact";import{useState as Y,useMemo as Ia}from"preact/hooks";function Oa(t,s){let e={};return t.title!==s.title&&(e.title=t.title),t.notes!==(s.notes||"")&&(e.notes=t.notes),t.color!==(s.color||"")&&(e.color=t.color),t.tool!==(s.tool||"")&&(e.tool=t.tool),t.tool==="claude"&&(t.extraArgs!==(s.extraArgs||"")&&(e.extraArgs=t.extraArgs),t.plugins!==(s.plugins||"")&&(e.plugins=t.plugins),t.channels!==(s.channels||"")&&(e.channels=t.channels),t.skipPermissions!==!!s.skipPermissions&&(e.skipPermissions=t.skipPermissions),t.autoMode!==!!s.autoMode&&(e.autoMode=t.autoMode)),e}function Hs(){let t=Nt.value,{sessions:s}=L.value,e=Ia(()=>t?s.find(O=>O.id===t.sessionId):null,[t&&t.sessionId,s]),n=e||{title:"",notes:"",color:"",tool:"claude"},[o,a]=Y(n.title),[i,l]=Y(n.notes||""),[d,r]=Y(n.color||""),[c,h]=Y(n.tool||"claude"),[v,x]=Y(n.extraArgs||""),[w,m]=Y(n.plugins||""),[A,f]=Y(n.channels||""),[u,b]=Y(!!n.skipPermissions),[$,E]=Y(!!n.autoMode),[g,p]=Y(null),[F,W]=Y(!1),[X,Ht]=Y(t?t.sessionId:null);if(t&&e&&X!==t.sessionId&&(a(e.title||""),l(e.notes||""),r(e.color||""),h(e.tool||"claude"),x(e.extraArgs||""),m(e.plugins||""),f(e.channels||""),b(!!e.skipPermissions),E(!!e.autoMode),p(null),Ht(t.sessionId)),!t||!D.value||!e)return null;let $t=e.tool||"",_=js(wt.value,$t);async function sn(O){O.preventDefault(),p(null);let Le=Oa({title:o,notes:i,color:d,tool:c,extraArgs:v,plugins:w,channels:A,skipPermissions:u,autoMode:$},e);if(Object.keys(Le).length===0){Mt();return}W(!0);try{await y("PATCH",`/api/sessions/${encodeURIComponent(e.id)}`,Le),Mt()}catch(Re){p(Re.message||String(Re))}finally{W(!1)}}function Mt(){Nt.value=null,Ht(null)}let nn=O=>{O.target===O.currentTarget&&Mt()},an=F||!o.trim();return Gt`
    <div class="overlay" onClick=${nn} data-testid="edit-session-dialog">
      <form class="dialog" onClick=${O=>O.stopPropagation()} onSubmit=${sn}>
        <div class="dh">
          <span class="kicker">EDIT</span>
          <div class="t">Edit session</div>
          <button type="button" class="icon-btn" onClick=${Mt} aria-label="Close">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>TITLE</label>
            <input
              autofocus required
              data-testid="edit-session-title"
              value=${o}
              onInput=${O=>a(O.target.value)}
              placeholder="Session title"/>
          </div>
          <div class="field">
            <label>NOTES</label>
            <input
              data-testid="edit-session-notes"
              value=${i}
              onInput=${O=>l(O.target.value)}
              placeholder="Optional notes"/>
          </div>
          <div class="field">
            <label>COLOR</label>
            <input
              data-testid="edit-session-color"
              value=${d}
              onInput=${O=>r(O.target.value)}
              placeholder="#RRGGBB, 0-255, or blank to clear"/>
          </div>
          <div class="field">
            <label>TOOL (restart required)</label>
            <div class="seg-row">
              ${_.map(O=>Gt`
                <button type="button" key=${O}
                        class=${`seg-btn ${c===O?"on":""}`}
                        onClick=${()=>h(O)}>${ge(O)}</button>
              `)}
            </div>
          </div>
          ${c==="claude"&&Gt`
            <div class="field">
              <label>EXTRA ARGS (restart, claude)</label>
              <input
                data-testid="edit-session-extra-args"
                value=${v}
                onInput=${O=>x(O.target.value)}
                placeholder="--model opus --verbose"/>
            </div>
            <div class="field">
              <label>PLUGINS (restart, claude — comma-separated)</label>
              <input
                data-testid="edit-session-plugins"
                value=${w}
                onInput=${O=>m(O.target.value)}
                placeholder="octopus,discord"/>
            </div>
            <div class="field">
              <label>CHANNELS (restart, claude — comma-separated)</label>
              <input
                data-testid="edit-session-channels"
                value=${A}
                onInput=${O=>f(O.target.value)}
                placeholder="plugin:telegram@org/repo"/>
            </div>
            <div class="field">
              <label>
                <input type="checkbox"
                       data-testid="edit-session-skip-permissions"
                       checked=${u}
                       onChange=${O=>b(O.target.checked)}/>
                Skip permissions (restart, claude)
              </label>
            </div>
            <div class="field">
              <label>
                <input type="checkbox"
                       data-testid="edit-session-auto-mode"
                       checked=${$}
                       onChange=${O=>E(O.target.checked)}/>
                Auto mode (restart, claude)
              </label>
            </div>
          `}
          ${g&&Gt`
            <div data-testid="edit-session-error"
                 style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${g}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${Mt}>Cancel</button>
          <button type="submit" class="btn primary"
                  data-testid="edit-session-save"
                  disabled=${an}>
            ${F?"Saving\u2026":Gt`Save <span class="kbd">⏎</span>`}
          </button>
        </div>
      </form>
    </div>
  `}import{html as Pa}from"htm/preact";import{useEffect as Da,useRef as Ma}from"preact/hooks";function Ks({message:t,onConfirm:s}){let e=Ma(null);Da(()=>{e.current&&e.current.focus()},[]);let n=()=>H.value=null,o=()=>{s(),H.value=null};return Pa`
    <div class="overlay" onClick=${i=>i.target===i.currentTarget&&n()}>
      <div role="dialog" aria-modal="true" aria-label="Confirm action"
           class="dialog" style="max-width: 460px;"
           onClick=${i=>i.stopPropagation()}
           onKeyDown=${i=>{i.key==="Escape"&&(i.stopPropagation(),n())}}>
        <div class="dh">
          <span class="kicker" style="color: var(--tn-red); background: rgba(247,118,142,0.12);">CONFIRM</span>
          <div class="t">Are you sure?</div>
          <button type="button" class="icon-btn" onClick=${n} aria-label="Close">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db">
          <div style="font-family: var(--sans); color: var(--text); line-height: 1.55;">${t}</div>
        </div>
        <div class="df">
          <button type="button" class="btn ghost" ref=${e} onClick=${n}>Cancel</button>
          <button type="button" class="btn danger" onClick=${o}>Delete</button>
        </div>
      </div>
    </div>
  `}import{html as Bs}from"htm/preact";import{useState as De}from"preact/hooks";function qs({mode:t,groupPath:s,currentName:e,onSubmit:n}){let[o,a]=De(e||""),[i,l]=De(null),[d,r]=De(!1),c=t==="create",h=c?"New group":"Rename group",v=c?"Create":"Rename",x=()=>xt.value=null;async function w(m){m.preventDefault(),l(null),r(!0);try{c?await y("POST","/api/groups",{name:o}):await y("PATCH","/api/groups/"+encodeURIComponent(s),{name:o}),xt.value=null,n&&n()}catch(A){l(A.message)}finally{r(!1)}}return Bs`
    <div class="overlay" onClick=${m=>m.target===m.currentTarget&&x()}>
      <form class="dialog" style="max-width: 460px;"
            onClick=${m=>m.stopPropagation()}
            onSubmit=${w}>
        <div class="dh">
          <span class="kicker">${c?"NEW":"RENAME"}</span>
          <div class="t">${h}</div>
          <button type="button" class="icon-btn" onClick=${x} aria-label="Close">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db">
          <div class="field">
            <label>NAME</label>
            <input autofocus required value=${o} onInput=${m=>a(m.target.value)} placeholder="my-group"/>
          </div>
          ${i&&Bs`
            <div style="font-family: var(--mono); font-size: 11.5px; color: var(--tn-red); padding: 8px 10px;
                        border: 1px solid rgba(247,118,142,0.3); border-radius: 4px; background: rgba(247,118,142,0.06);">
              ${i}
            </div>
          `}
        </div>
        <div class="df">
          <button type="button" class="btn ghost" onClick=${x}>Cancel</button>
          <button type="submit" class="btn primary" disabled=${d||!o}>
            ${d?c?"Creating\u2026":"Renaming\u2026":v}
          </button>
        </div>
      </form>
    </div>
  `}import{html as Me}from"htm/preact";import{useState as Na,useEffect as La}from"preact/hooks";function Vs(){let[t,s]=Na(null),e=ye.value;return La(()=>{e||fetch("/api/settings").then(n=>{if(!n.ok)throw new Error("Settings request failed: "+n.status);return n.json()}).then(n=>{ye.value=n}).catch(n=>s(n.message||"Failed to load settings"))},[]),t?Me`<div style="font-family: var(--mono); font-size: 12px; color: var(--tn-red);">${t}</div>`:e?Me`
    <div data-testid="settings-panel" style="display: flex; flex-direction: column; gap: 2px;">
      <div class="kv" data-testid="settings-profile"><span class="k">profile</span><span class="v">${e.profile||"default"}</span></div>
      <div class="kv" data-testid="settings-version"><span class="k">version</span><span class="v">${e.version||"unknown"}</span></div>
      <div class="kv" data-testid="settings-read-only"><span class="k">read-only</span><span class=${`v ${e.readOnly?"warn":"ok"}`}>${e.readOnly?"yes":"no"}</span></div>
      <div class="kv" data-testid="settings-web-mutations"><span class="k">web mutations</span><span class=${`v ${e.webMutations?"ok":"warn"}`}>${e.webMutations?"enabled":"disabled"}</span></div>
      <div class="kv" data-testid="settings-hidden-tools"><span class="k">hidden tools</span><span class="v">${(e.hiddenTools||[]).join(", ")||"none"}</span></div>
      <div class="kv" data-testid="settings-picker-tools"><span class="k">picker tools</span><span class="v">${(e.pickerTools||[]).join(", ")||"loading\u2026"}</span></div>
      <div style="font-family: var(--mono); font-size: 11px; color: var(--muted); margin-top: 8px;">
        Edit <code>~/.agent-deck/config.toml</code> (<code>[ui] hidden_tools</code>) or use TUI Settings → Visible tools…
      </div>
    </div>
  `:Me`<div style="font-family: var(--mono); font-size: 12px; color: var(--muted);">Loading…</div>`}import{html as Ut}from"htm/preact";var Ra=[{keys:["/"],label:"Focus session filter / search"},{keys:["j"],label:"Move focus down (next session)"},{keys:["k"],label:"Move focus up (previous session)"},{keys:["Enter"],label:"Open focused session"},{keys:["Shift","Enter"],label:"Open focused session in new browser tab"},{keys:["n"],label:"New session dialog"},{keys:["r"],label:"Rename focused session (TUI-only today)"},{keys:["Shift","D"],label:"Close focused session (stop process, keep metadata)"},{keys:["Ctrl","Z"],label:"Undo last delete (within 30s)"},{keys:["q"],label:"Close current modal / overlay"},{keys:["Esc"],label:"Close modal / unfocus input"},{keys:["?"],label:"Toggle this help overlay"},{keys:["Ctrl","K"],label:"Command palette"},{keys:["]"],label:"Toggle right rail"}];function za({k:t}){return Ut`<span class="kbd kshort-kbd">${t}</span>`}function Js(){if(!nt.value)return null;let t=()=>nt.value=!1;return Ut`
    <div class="overlay kshort-overlay" role="dialog" aria-label="Keyboard shortcuts"
         data-testid="shortcuts-overlay"
         onClick=${t}>
      <div class="dialog kshort-dialog" onClick=${s=>s.stopPropagation()}>
        <div class="dh">
          <span class="kicker">HELP</span>
          <div class="t">Keyboard shortcuts</div>
          <button class="icon-btn" onClick=${t} aria-label="Close help">
            <${S} d=${C.x}/>
          </button>
        </div>
        <div class="db">
          <table class="kshort-table">
            <tbody>
              ${Ra.map(s=>Ut`
                <tr key=${s.keys.join("+")}>
                  <td class="kshort-keys">
                    ${s.keys.map((e,n)=>Ut`
                      ${n>0&&Ut`<span class="kshort-plus">+</span>`}
                      <${za} k=${e}/>
                    `)}
                  </td>
                  <td class="kshort-label">${s.label}</td>
                </tr>
              `)}
            </tbody>
          </table>
          <div class="kshort-foot">
            Web binds the most-used TUI keys (issue #780). Web-only actions
            (e.g. <span class="kbd">Ctrl</span>+<span class="kbd">K</span>) are
            included for completeness.
          </div>
        </div>
      </div>
    </div>
  `}function Fa(){let{sessions:t}=L.value,s=T.value,e=t.find(d=>d.id===s)||t[0];if(!e)return null;let n=(e.kind||"agent").toUpperCase(),o=lt.value||"",a=D.value,i=e.model?`${e.model}${e.modelVersion?` ${e.modelVersion}`:""}`:"",l=d=>{if(a)return d==="fork"?y("POST",`/api/sessions/${e.id}/fork`,{title:e.title+"-fork"}).catch(()=>{}):y("POST",`/api/sessions/${e.id}/${d}`).catch(()=>{})};return z`
    <div class="work-head">
      <div class="path">
        <span class=${`kind ${e.kind||""}`}>${n}</span>
        ${o&&z`<span class="seg">${o} /</span>`}
        <span class="seg">${e.group||"default"} /</span>
        <span class="cur">${e.title}</span>
      </div>
      <span class=${`status-chip ${e.status}`}><span class="d"/>${e.status}</span>
      ${i&&z`<span class="status-chip model" title=${e.modelId||i}>${i}</span>`}
      <span class="spacer"/>
      ${a&&z`
        <div class="actions">
          ${e.status==="running"||e.status==="waiting"?z`<button class="btn ghost" onClick=${()=>l("stop")}><${S} d=${C.stop} size=${12}/>Stop</button>`:z`<button class="btn ghost" onClick=${()=>l("start")}><${S} d=${C.play} size=${12}/>Start</button>`}
          <button class="btn ghost" onClick=${()=>l("restart")}><${S} d=${C.restart} size=${12}/>Restart</button>
          ${e.canFork&&z`<button class="btn" onClick=${()=>l("fork")}><${S} d=${C.fork} size=${12}/>Fork</button>`}
          <button class="btn primary" onClick=${()=>j.value=!0}>
            <${S} d=${C.plus} size=${12}/>New <span class="kbd">n</span>
          </button>
        </div>
      `}
    </div>
  `}function _a({tab:t}){return z`
    <div style=${{display:t==="terminal"?"flex":"none",flex:1,minHeight:0,flexDirection:"column"}}>
      <${ms}/>
    </div>
    ${t==="command-center"&&z`<${Cs}/>`}
    ${t==="genui"&&z`<${Ps}/>`}
    ${t==="fleet"&&z`<${$s}/>`}
    ${t==="costs"&&z`<${hs}/>`}
    ${t==="search"&&z`<${zs}/>`}
    ${t==="archived"&&z`<${Ns}/>`}
    ${t==="mcp"&&z`<${Fs}/>`}
    ${t==="skills"&&z`<${_s}/>`}
    ${t==="conductor"&&z`<${Oe} title="Conductor"
                              message="Conductor orchestration view is TUI-only. The web API does not expose child topology, bridges, or NEED escalation."/>`}
    ${t==="watchers"&&z`<${Oe} title="Watchers"
                              message="Watcher framework events are routed in the backend; the web API does not surface event streams or routing config."/>`}
  `}function Ys(){let t=M.value,s=j.value,e=H.value,n=xt.value,o=ot.value;return Dt(()=>{let a=document.querySelector("body > .app");return a&&a.id!=="app-root-grid"&&(a.style.display="none"),()=>{a&&(a.style.display="")}},[]),Dt(()=>{fetch("/api/settings").then(a=>a.ok?a.json():null).then(a=>{a&&(typeof a.webMutations=="boolean"&&(D.value=a.webMutations),typeof a.toolFilter=="boolean"&&(Ke.value=a.toolFilter),Array.isArray(a.visibleTools)&&(Be.value=a.visibleTools),typeof a.toolFilterFallback=="boolean"&&(Vt.value=a.toolFilterFallback),Array.isArray(a.hiddenTools)&&(qe.value=a.hiddenTools),Array.isArray(a.pickerTools)&&a.pickerTools.length>0&&(wt.value=a.pickerTools))}).catch(()=>{})},[]),Dt(()=>{fetch("/api/profiles").then(a=>a.ok?a.json():null).then(a=>{a&&Array.isArray(a.profiles)&&(Jt.value=a,a.current&&(lt.value=a.current))}).catch(()=>{})},[]),Dt(()=>{let a=!1,i=()=>{fetch("/api/system/stats").then(d=>d.ok?d.json():null).then(d=>{!a&&d&&(Yt.value=d)}).catch(()=>{})};i();let l=setInterval(i,5e3);return()=>{a=!0,clearInterval(l)}},[]),Dt(()=>{let a=r=>{let c=L.value?.sessions||[];if(c.length===0)return;let h=T.value,v=c.findIndex(w=>w.id===h);v===-1&&(v=r>0?-1:c.length);let x=c[Math.max(0,Math.min(c.length-1,v+r))];x&&(T.value=x.id)},i=()=>{let r=L.value?.sessions||[],c=T.value;return r.find(h=>h.id===c)||r[0]||null},l=()=>{it.value=!1,tt.value=!1,nt.value=!1,j.value=!1,H.value=null,xt.value=null,ot.value=!1},d=r=>{let c=r.target,h=c&&(c.tagName==="INPUT"||c.tagName==="TEXTAREA"||c.tagName==="SELECT"||c.isContentEditable);if((r.metaKey||r.ctrlKey)&&r.key.toLowerCase()==="k"){r.preventDefault(),it.value=!0;return}if(r.key==="Escape"){h&&typeof c.blur=="function"&&c.blur(),l();return}if(!h){if(r.key==="Enter"&&r.shiftKey){let v=i();if(v){r.preventDefault();let x=`${window.location.pathname}#session=${encodeURIComponent(v.id)}`;window.open(x,"_blank","noopener")}return}if(r.key==="?")r.preventDefault(),nt.value=!nt.value;else if(r.key==="/")r.preventDefault(),document.querySelector(".side-filter input")?.focus();else if(r.key==="j")r.preventDefault(),a(1);else if(r.key==="k")r.preventDefault(),a(-1);else if(r.key==="Enter"){let v=i();v&&(r.preventDefault(),T.value=v.id,M.value="terminal")}else if(r.key==="n"&&D.value)j.value=!0;else if(r.key==="r"){let v=i();v&&I(`Rename "${v.title}": use the TUI (web rename API not implemented yet)`,"info")}else if(r.key==="D"){if(!D.value)return;let v=i();if(!v)return;H.value={message:`Close session "${v.title}"? The tmux process will be killed; metadata is preserved.`,onConfirm:()=>y("POST",`/api/sessions/${v.id}/close`).catch(()=>{})}}else if((r.metaKey||r.ctrlKey)&&r.key.toLowerCase()==="z"){if(!D.value)return;r.preventDefault(),y("POST","/api/sessions/undelete").then(v=>{v&&v.sessionId?I(`Restored session ${v.sessionId}`,"success"):I("Restored last deleted session","success")}).catch(()=>I("Nothing to undo","info"))}else r.key==="q"?l():r.key==="]"&&(q.value=q.value==="visible"?"hidden":"visible")}};return window.addEventListener("keydown",d),()=>window.removeEventListener("keydown",d)},[]),Dt(()=>{if(!o)return;let a=i=>{i.key==="Escape"&&(ot.value=!1)};return document.addEventListener("keydown",a),()=>document.removeEventListener("keydown",a)},[o]),z`
    <div id="app-root-grid" class="app">
      <${Qe}/>
      <${Ze}/>
      <div class="main">
        <${Fa}/>
        <div class="work-body">
          <${_a} tab=${t}/>
        </div>
      </div>
      <${ns}/>
      <${ts}/>
      <${os}/>

      ${s&&z`<${Ws}/>`}
      <${Hs}/>
      ${e&&z`<${Ks} ...${e}/>`}
      ${n&&z`<${qs} ...${n}/>`}

      ${o&&z`
        <div class="overlay" onClick=${()=>ot.value=!1}>
          <div class="dialog" onClick=${a=>a.stopPropagation()}>
            <div class="dh">
              <span class="kicker">SETTINGS</span>
              <div class="t">Settings</div>
              <button class="icon-btn" onClick=${()=>ot.value=!1} aria-label="Close settings">
                <${S} d=${C.x}/>
              </button>
            </div>
            <div class="db">
              <${Vs}/>
            </div>
          </div>
        </div>
      `}

      <${is}/>
      <${ls}/>
      <${Js}/>
      <${Ge}/>
      <${Xe}/>
    </div>
  `}function Qs(){return Xs(()=>{function t(){let s=window.location.pathname||"/";if(s.startsWith("/s/")){let e=s.slice(3);if(e&&!e.includes("/")){try{T.value=decodeURIComponent(e)}catch{T.value=null}return}}s==="/"&&(T.value=null)}return window.addEventListener("popstate",t),()=>window.removeEventListener("popstate",t)},[]),Xs(()=>{let t=T.value,s=window.location.pathname,e=t?"/s/"+encodeURIComponent(t):"/";s!==e&&window.history.pushState(null,"",e)},[T.value]),Ga`
    <${Ys} />
  `}(function(){let s=new URLSearchParams(window.location.search),e=s.get("token");if(!e)return;K.value=e,s.delete("token");let n=s.toString(),o=window.location.pathname+(n?"?"+n:"")+window.location.hash;history.replaceState(null,"",o);let a=document.querySelector('meta[name="referrer"]');a||(a=document.createElement("meta"),a.name="referrer",document.head.appendChild(a)),a.content="no-referrer"})();var jt=null;function Zs(){if(jt)return;let t=K.value,s=t?"/events/menu?token="+encodeURIComponent(t):"/events/menu",e=new EventSource(s);jt=e,e.addEventListener("menu",n=>{try{let o=JSON.parse(n.data);o&&Array.isArray(o.items)&&(ut.value=o.items,ke.value=!0),B.value="connected"}catch{}}),e.addEventListener("error",()=>{B.value="disconnected"})}function $c(){jt&&(jt.close(),jt=null),Wt&&(Wt.close(),Wt=null)}var Wt=null,he=new Set;function tn(){if(Wt)return;let t=K.value,s=t?"/events/command-center?token="+encodeURIComponent(t):"/events/command-center",e=new EventSource(s);Wt=e,e.addEventListener("command-center",n=>{try{let o=JSON.parse(n.data);if(o&&typeof o=="object"){let a=Q.value;Q.value=o,Wa(a,o);let i=Array.isArray(o.recentlyCompleted)?o.recentlyCompleted:[];for(let l of i){let d=(l&&(l.id||""))+":"+(l&&(l.at||""));he.has(d)||(he.add(d),l&&l.title&&I(`\u2705 ${l.title} just finished`,"success"))}he.size>200&&he.clear()}}catch{}})}function en(t){let s=t&&t.totals;return s?(s.running||0)+(s.waiting||0)+(s.idle||0):0}function Wa(t,s){if(!t||en(s)<=en(t))return;let n=at.value.find(o=>o.stage==="routed");n&&Tt(n.correlationId,{stage:"session-created"})}async function Ha(){try{let t=await y("GET","/api/menu");ut.value=t.items||[],ke.value=!0,Zs(),tn()}catch{B.value="disconnected",Zs(),tn()}}function Ka(){let t=window.location.pathname||"/";if(t.startsWith("/s/")){let s=t.slice(3);if(s&&!s.includes("/")){try{T.value=decodeURIComponent(s)}catch{T.value=null}return}}}function Ba(){if(!("serviceWorker"in navigator))return;function t(){navigator.serviceWorker.register("/sw.js",{scope:"/"}).catch(()=>{})}document.readyState==="complete"||document.readyState==="interactive"?t():window.addEventListener("load",t,{once:!0})}var Ne=document.getElementById("app-root");Ne&&(Ne.style.cssText="position:fixed;inset:0;z-index:10;",Ka(),Ha(),Ba(),Ua(ja`<${Qs} />`,Ne));export{Ka as applyRouteSelection,Ha as loadMenu,Ba as registerServiceWorker,tn as startCommandCenterSSE,Zs as startSSE,$c as stopSSE};
