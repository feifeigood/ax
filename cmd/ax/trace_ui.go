// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	osConfig "os/exec"
	"runtime"
)

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = osConfig.Command("xdg-open", url).Start()
	case "windows":
		err = osConfig.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = osConfig.Command("open", url).Start()
	}
	if err != nil {
		fmt.Printf("Failed to open browser: %v\n", err)
	}
}

func serveTraceUI(listener net.Listener, data *TraceData, rawHTML string) error {
	addr := listener.Addr().String()
	host, port, err := net.SplitHostPort(addr)
	if err == nil && (host == "::" || host == "0.0.0.0" || host == "" || host == "[::]") {
		addr = fmt.Sprintf("localhost:%s", port)
	}
	url := fmt.Sprintf("http://%s", addr)

	fmt.Printf("Starting trace viewer for %s...\n", data.RootExecID)
	fmt.Printf("Opening browser to %s\n", url)
	fmt.Printf("Press Ctrl+C to exit.\n")

	mux := http.NewServeMux()
	mux.HandleFunc("/api/trace", handleTrace(data))
	mux.HandleFunc("/", handleIndex(rawHTML))

	go openBrowser(url)

	return http.Serve(listener, mux)
}

func handleTrace(data *TraceData) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(data)
	}
}

func handleIndex(rawHTML string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, rawHTML)
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Trace Viewer</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif;background:#f0f2f5;color:#1a1a2e;height:100vh;display:flex;flex-direction:column;overflow:hidden}

.hdr{background:#1e1e2e;color:#cdd6f4;padding:10px 20px;display:flex;align-items:center;gap:10px;flex-shrink:0}
.hdr-logo{font-size:18px}
.hdr h1{font-size:15px;font-weight:600;letter-spacing:.3px}

.layout{display:flex;flex:1;overflow:hidden}

.main{flex:1;min-height:0;overflow-y:auto;padding:20px;display:flex;flex-direction:column;gap:14px}
.empty{display:flex;align-items:center;justify-content:center;flex:1;color:#6c7086;font-size:14px}
.loading{display:flex;align-items:center;justify-content:center;height:120px;color:#6c7086}

.trace-hdr .label{font-size:11px;color:#6c7086;text-transform:uppercase;letter-spacing:.5px;margin-bottom:4px}
.trace-hdr .tid{font-family:monospace;font-size:15px;font-weight:700;color:#1a1a2e}

.timeline-card{background:#fff;border-radius:10px;padding:14px 18px;box-shadow:0 1px 4px rgba(0,0,0,.08);flex-shrink:0}
.card-title{font-size:10px;text-transform:uppercase;letter-spacing:.8px;color:#6c7086;margin-bottom:10px;font-weight:600}
.tl-rows{display:flex;flex-direction:column;gap:6px}
.tl-row{display:flex;align-items:center;gap:10px}
.tl-label{width:220px;flex-shrink:0;font-size:11px;font-family:monospace;color:#4a4a6a;word-break:break-all}
.tl-track{flex:1;height:18px;background:#f0f0f0;border-radius:4px;position:relative}
.tl-bar{height:100%;border-radius:4px;position:absolute;display:flex;align-items:center;padding:0 5px;min-width:6px;cursor:default}
.tl-bar span{font-size:10px;color:#fff;white-space:nowrap;overflow:hidden;text-shadow:0 0 3px rgba(0,0,0,.3)}
.tl-bar.c0{background:#89b4fa}
.tl-bar.c1{background:#a6e3a1}
.tl-bar.c2{background:#fab387}
.tl-bar.c3{background:#cba6f7}
.tl-bar.c4{background:#f38ba8}
.tl-bar.c5{background:#94e2d5}
.tl-bar.c6{background:#f9e2af}

.task-card{background:#fff;border-radius:10px;box-shadow:0 1px 4px rgba(0,0,0,.08);overflow:hidden;flex-shrink:0}
.task-card-hdr{padding:11px 16px;display:flex;align-items:center;gap:8px;cursor:pointer;user-select:none;background:#fafafa;border-bottom:1px solid #f0f0f0}
.task-card-hdr:hover{background:#f4f4f8}
.expand-ico{color:#6c7086;font-size:11px;transition:transform .15s;flex-shrink:0}
.task-card-hdr.collapsed .expand-ico{transform:rotate(-90deg)}
.task-card-body.hidden{display:none}
.task-name{font-family:monospace;font-size:13px;font-weight:700;color:#1e1e2e}
.agent-badge{font-size:12px;padding:2px 8px;border-radius:10px;background:#ede9fe;color:#6d28d9;font-family:monospace}
.state-badge{font-size:11px;padding:2px 8px;border-radius:10px;margin-left:auto;font-weight:600}
.state-badge.completed{background:#dcfce7;color:#166534}
.state-badge.pending{background:#fef3c7;color:#92400e}
.state-badge.failed{background:#fee2e2;color:#991b1b}
.task-card-body{}

.event{border-top:1px solid #f5f5f5}
.state-evt{padding:7px 18px;font-size:11px;color:#7c7ca0;font-style:italic;background:#fafafe;display:flex;align-items:center;gap:8px}
.state-evt .etime{font-family:monospace;color:#a0a0c0}
.evt-meta{padding:8px 18px 2px;display:flex;align-items:center;gap:8px}
.etime{font-size:11px;color:#a0a0b0;font-family:monospace}
.evt-section-label{font-size:10px;text-transform:uppercase;letter-spacing:.5px;color:#a0a0b0}

.content-list{padding:4px 18px 10px;display:flex;flex-direction:column;gap:5px}
.content-item{border-radius:7px;overflow:hidden;border:1px solid transparent}

.role-user{border-color:#dbeafe}
.role-user .c-hdr{background:#dbeafe;color:#1d4ed8}
.role-user .c-body{background:#eff6ff}

.role-model{border-color:#dcfce7}
.role-model .c-hdr{background:#dcfce7;color:#166534}
.role-model .c-body{background:#f0fdf4}

.role-assistant{border-color:#f3e8ff}
.role-assistant .c-hdr{background:#f3e8ff;color:#7e22ce}
.role-assistant .c-body{background:#faf5ff}

.role-unknown{border-color:#e5e7eb}
.role-unknown .c-hdr{background:#f3f4f6;color:#4b5563}
.role-unknown .c-body{background:#f9fafb}

.c-hdr{font-size:10px;font-weight:700;padding:3px 10px;text-transform:uppercase;letter-spacing:.5px}
.c-body{padding:8px 10px;font-size:13px;line-height:1.6}

.txt{white-space:pre-wrap;word-break:break-word}
.txt pre{background:#1e1e2e;color:#cdd6f4;padding:10px 14px;border-radius:6px;overflow-x:auto;font-size:12px;line-height:1.5;margin:4px 0;font-family:'Fira Code','Cascadia Code',monospace}
.txt code{font-family:'Fira Code','Cascadia Code',monospace}

.conf-q{background:#fffbeb;border:1px solid #fcd34d;border-radius:6px;padding:7px 12px;font-size:13px;color:#92400e}
.conf-q::before{content:"? ";font-weight:700;color:#d97706}
.conf-approved{font-size:12px;color:#166534;font-weight:600}
.conf-approved::before{content:"✓ Approved"}
.conf-denied{font-size:12px;color:#991b1b;font-weight:600}
.conf-denied::before{content:"✗ Denied"}
</style>
</head>
<body>
<div class="hdr">
  <span class="hdr-logo">⬡</span>
  <h1>Trace Viewer</h1>
</div>
<div class="layout">
  <div class="main" id="main">
    <div class="loading">Loading trace...</div>
  </div>
</div>

<script>
function esc(s){
  if(s==null)return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
}

function fmtTime(ts){
  const d=new Date(ts);
  return d.toLocaleTimeString('en-US',{hour12:false,hour:'2-digit',minute:'2-digit',second:'2-digit',fractionalSecondDigits:3});
}

const BT=String.fromCharCode(96);
const FENCE=BT+BT+BT;
const reSplit=new RegExp('('+FENCE+'(?:[^\\n]*)?\\n[\\s\\S]*?'+FENCE+')');
const reMatch=new RegExp('^'+FENCE+'([^\\n]*)?\\n([\\s\\S]*)'+FENCE+'$');
function renderText(raw){
  const parts=raw.split(reSplit);
  return parts.map(part=>{
    const m=part.match(reMatch);
    if(m){return '<pre><code>'+esc(m[2])+'</code></pre>';}
    return '<span class="txt">'+esc(part)+'</span>';
  }).join('');
}

function renderContent(c){
  const role=c.role||'unknown';
  let body='';
  if(c.text){
    body='<div class="c-body"><div class="txt">'+renderText(c.text.text)+'</div></div>';
  } else if(c.confirmation){
    const cf=c.confirmation;
    if(cf.question){
      body='<div class="c-body"><div class="conf-q">'+esc(cf.question)+'</div></div>';
    } else if(cf.approval!=null){
      const cls=cf.approval.approved?'conf-approved':'conf-denied';
      body='<div class="c-body"><div class="'+cls+'"></div></div>';
    }
  }
  if(!body)return '';
  return '<div class="content-item role-'+esc(role)+'"><div class="c-hdr">'+esc(role)+'</div>'+body+'</div>';
}

function renderEvent(ev){
  const time=fmtTime(ev.timestamp);
  const hasIn=ev.inputs&&ev.inputs.length>0;
  const hasOut=ev.outputs&&ev.outputs.length>0;

  // State-only event
  if(!hasIn&&!hasOut&&ev.state){
    return '<div class="event"><div class="state-evt"><span class="etime">'+esc(time)+'</span>State → '+esc(ev.state)+'</div></div>';
  }

  let html='<div class="event"><div class="evt-meta"><span class="etime">'+esc(time)+'</span></div>';
  if(hasIn){
    html+='<div class="content-list">';
    ev.inputs.forEach(c=>{html+=renderContent(c);});
    html+='</div>';
  }
  if(hasOut){
    html+='<div class="content-list">';
    ev.outputs.forEach(c=>{html+=renderContent(c);});
    html+='</div>';
  }
  html+='</div>';
  return html;
}

function getLastState(events){
  for(let i=events.length-1;i>=0;i--){
    if(events[i].state)return events[i].state;
  }
  return null;
}

function renderTrace(data){
  // Build timeline
  const taskTimes=data.tasks.map(t=>{
    const ts=t.events.map(e=>new Date(e.timestamp).getTime());
    const start=ts.length?Math.min(...ts):null;
    const end=ts.length?Math.max(...ts):null;
    return{execID:t.exec_id,start,end};
  });
  taskTimes.sort((a,b)=>{
    if(a.start==null)return 1;
    if(b.start==null)return -1;
    return a.start-b.start;
  });
  let minT=null,maxT=null;
  taskTimes.forEach(({start,end})=>{
    if(start!=null){
      if(minT==null||start<minT)minT=start;
      if(maxT==null||end>maxT)maxT=end;
    }
  });
  const span=maxT-minT||1;

  let html='<div class="trace-hdr"><div class="label">Trace</div><div class="tid">'+esc(data.root_exec_id)+'</div></div>';

  html+='<div class="timeline-card"><div class="card-title">Timeline</div><div class="tl-rows">';
  taskTimes.forEach(({execID,start,end},i)=>{
    const shortName=execID===data.root_exec_id?'root':execID.slice(data.root_exec_id.length+1);
    const left=start!=null?((start-minT)/span*100).toFixed(2):0;
    const width=start!=null?Math.max((end-start)/span*100,0.4).toFixed(2):0;
    const dur=end-start;
    const durStr=dur<1000?dur+'ms':(dur/1000).toFixed(2)+'s';
    const colorCls='c'+(i%7);
    html+='<div class="tl-row">'
      +'<div class="tl-label" title="'+esc(execID)+'">'+esc(shortName)+'</div>'
      +'<div class="tl-track"><div class="tl-bar '+colorCls+'" style="left:'+left+'%;width:'+width+'%;cursor:pointer" title="'+esc(durStr)+'" onclick="scrollToCard(\''+esc(execID)+'\')"><span>'+esc(durStr)+'</span></div></div>'
      +'</div>';
  });
  html+='</div></div>';

  // Task sections
  data.tasks.forEach((task,i)=>{
    const shortName=task.exec_id===data.root_exec_id?'root':task.exec_id.slice(data.root_exec_id.length+1);
    const lastState=getLastState(task.events);
    const stateClass=lastState?lastState.toLowerCase().replace('state_',''):'';
    const stateBadge=lastState?'<span class="state-badge '+esc(stateClass)+'">'+esc(lastState.replace('STATE_',''))+'</span>':'';

    html+='<div class="task-card" id="card-'+esc(task.exec_id)+'">'
      +'<div class="task-card-hdr collapsed" onclick="toggleCard(this)">'
      +'<span class="expand-ico">▼</span>'
      +'<span class="task-name">'+esc(shortName)+'</span>'
      +'<span class="agent-badge">'+esc(task.agent_id||'—')+'</span>'
      +stateBadge
      +'</div>'
      +'<div class="task-card-body hidden">';
    task.events.forEach(ev=>{html+=renderEvent(ev);});
    html+='</div></div>';
  });

  document.getElementById('main').innerHTML=html;
}

function scrollToCard(execID){
  const el=document.getElementById('card-'+execID);
  if(!el)return;
  const hdr=el.querySelector('.task-card-hdr');
  const body=el.querySelector('.task-card-body');
  if(hdr&&hdr.classList.contains('collapsed')){
    hdr.classList.remove('collapsed');
    body.classList.remove('hidden');
  }
  el.scrollIntoView({behavior:'smooth',block:'start'});
}

function toggleCard(hdr){
  hdr.classList.toggle('collapsed');
  hdr.nextElementSibling.classList.toggle('hidden');
}

async function loadTrace(){
  document.getElementById('main').innerHTML='<div class="loading">Loading…</div>';
  const res=await fetch('/api/trace');
  const data=await res.json();
  renderTrace(data);
}

loadTrace();
</script>
</body>
</html>`
