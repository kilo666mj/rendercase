(function(){
  'use strict';
  if(window.parent===window||window.__rendercaseBridge)return;
  window.__rendercaseBridge=true;
  var parentOrigin=null,picking=false,pins=[],host=null,root=null,outline=null,frame=0,observer=null;
  function send(type,data){if(!parentOrigin)return;var m=data||{};m.rc=1;m.type=type;window.parent.postMessage(m,parentOrigin)}
  function norm(v){return String(v||'').replace(/\s+/g,' ').trim()}
  function clamp(v){return Math.min(1,Math.max(0,Math.round(v*1000)/1000))}
  function cssEscape(v){return window.CSS&&CSS.escape?CSS.escape(v):String(v).replace(/[^a-zA-Z0-9_-]/g,'\\$&')}

  function ensureHost(){
    if(host)return;
    host=document.createElement('rendercase-comments');
    host.style.cssText='position:fixed;inset:0;pointer-events:none;z-index:2147483647';
    root=host.attachShadow({mode:'closed'});
    var style=document.createElement('style');
    style.textContent='.pin{position:fixed;transform:translate(-50%,-100%);min-width:22px;height:22px;padding:0 6px;border:0;border-radius:11px 11px 11px 2px;background:#2dd4bf;color:#03060f;font:600 12px/22px system-ui,sans-serif;text-align:center;pointer-events:auto;cursor:pointer;box-shadow:0 2px 8px rgba(0,0,0,.4)}.pin.resolved{background:#8292a8}.pin.focus{outline:3px solid #a78bfa}.outline{position:fixed;display:none;border:2px solid #2dd4bf;background:rgba(45,212,191,.12);pointer-events:none}';
    outline=document.createElement('div');outline.className='outline';
    root.append(style,outline);
    (document.body||document.documentElement).appendChild(host);
  }

  function selectorFor(el){
    var parts=[];
    while(el&&el.nodeType===1&&el!==document.body&&el!==document.documentElement){
      if(el.id&&document.querySelectorAll('#'+cssEscape(el.id)).length===1){parts.unshift('#'+cssEscape(el.id));break}
      var i=1,sib=el;
      while((sib=sib.previousElementSibling))if(sib.tagName===el.tagName)i++;
      parts.unshift(el.tagName.toLowerCase()+':nth-of-type('+i+')');el=el.parentElement;
    }
    if(!parts.length||parts[0].charAt(0)!=='#')parts.unshift('body');
    while(parts.length>1&&parts.join(' > ').length>512)parts.shift();
    return parts.join(' > ').slice(0,512);
  }
  function pointIn(el,x,y){var r=el.getBoundingClientRect();return{x:r.width?clamp((x-r.left)/r.width):0.5,y:r.height?clamp((y-r.top)/r.height):0.5}}
  function elementAnchor(el,x,y){
    if(el&&el.nodeType!==1)el=el.parentElement;
    if(!el||el===host)return null;
    var a={selector:selectorFor(el),point:pointIn(el,x,y)},text=norm(el.textContent).slice(0,120);
    if(text)a.quote={exact:text};
    return a;
  }
  function selectionAnchor(range){
    var exact=norm(range.toString()).slice(0,500);if(!exact)return null;
    var el=range.commonAncestorContainer;if(el.nodeType!==1)el=el.parentElement;if(!el)return null;
    var full=String(el.textContent||'').replace(/\s+/g,' '),at=full.indexOf(exact),a={selector:selectorFor(el),quote:{exact:exact}};
    if(at>=0){var p=full.slice(Math.max(0,at-32),at).trim(),s=full.slice(at+exact.length,at+exact.length+32).trim();if(p)a.quote.prefix=p;if(s)a.quote.suffix=s}
    var r=range.getBoundingClientRect(),er=el.getBoundingClientRect();
    a.point={x:er.width?clamp((r.left-er.left)/er.width):0,y:er.height?clamp((r.top-er.top)/er.height):0};
    return a;
  }

  function onMove(e){var el=e.target;if(!el||el===host||el.nodeType!==1)return;var r=el.getBoundingClientRect();outline.style.display='block';outline.style.left=r.left+'px';outline.style.top=r.top+'px';outline.style.width=r.width+'px';outline.style.height=r.height+'px'}
  function onClick(e){
    e.preventDefault();e.stopPropagation();e.stopImmediatePropagation();
    var sel=window.getSelection(),anchor=null;
    if(sel&&!sel.isCollapsed&&sel.rangeCount)anchor=selectionAnchor(sel.getRangeAt(0));
    if(!anchor)anchor=elementAnchor(e.target,e.clientX,e.clientY);
    stopPick();
    if(anchor)send('picked',{anchor:anchor});
  }
  function onKey(e){if(e.key==='Escape'){e.preventDefault();stopPick();send('pick-cancelled')}}
  function startPick(){ensureHost();if(picking)return;picking=true;document.addEventListener('mousemove',onMove,true);document.addEventListener('click',onClick,true);document.addEventListener('keydown',onKey,true)}
  function stopPick(){if(!picking)return;picking=false;if(outline)outline.style.display='none';document.removeEventListener('mousemove',onMove,true);document.removeEventListener('click',onClick,true);document.removeEventListener('keydown',onKey,true)}

  function findText(quote){
    var best=null,bestScore=-1,bestLen=Infinity,all=document.body?document.body.getElementsByTagName('*'):[];
    for(var i=0;i<all.length;i++){
      var el=all[i];if(el===host||el.tagName==='SCRIPT'||el.tagName==='STYLE')continue;
      var t=norm(el.textContent);if(t.indexOf(quote.exact)<0)continue;
      var score=(quote.prefix&&t.indexOf(quote.prefix)>=0?1:0)+(quote.suffix&&t.indexOf(quote.suffix)>=0?1:0);
      if(score>bestScore||(score===bestScore&&t.length<bestLen)){best=el;bestScore=score;bestLen=t.length}
    }
    return best?{el:best,point:null}:null;
  }
  function resolve(anchor){
    if(!anchor||typeof anchor.selector!=='string')return null;
    var el=null,q=anchor.quote&&typeof anchor.quote.exact==='string'?anchor.quote:null;
    try{el=document.querySelector(anchor.selector)}catch(_){el=null}
    if(el&&(!q||norm(el.textContent).indexOf(q.exact)>=0))return{el:el,point:anchor.point||null};
    return q?findText(q):null;
  }

  function place(){
    frame=0;
    pins.forEach(function(p){
      var r=p.el.getBoundingClientRect(),pt=p.point||{x:0,y:0},hidden=!p.el.isConnected||(r.width===0&&r.height===0);
      p.node.style.display=hidden?'none':'';p.node.style.left=(r.left+r.width*pt.x)+'px';p.node.style.top=(r.top+r.height*pt.y)+'px';
    });
  }
  function schedule(){if(!frame)frame=requestAnimationFrame(place)}
  function setPins(list){
    ensureHost();
    pins.forEach(function(p){p.node.remove()});pins=[];
    var unanchored=[];
    list.slice(0,500).forEach(function(p){
      var n=Number(p&&p.n);if(!(n>0))return;
      var hit=resolve(p.anchor);if(!hit){unanchored.push(n);return}
      var b=document.createElement('button');b.type='button';b.className='pin'+(p.resolved?' resolved':'');b.textContent=String(n);
      b.addEventListener('click',function(e){e.stopPropagation();send('pin-click',{n:n})});
      root.append(b);pins.push({n:n,el:hit.el,point:hit.point,node:b});
    });
    place();send('anchors',{unanchored:unanchored});
  }
  function focusPin(n){
    pins.forEach(function(p){p.node.classList.toggle('focus',p.n===n);if(p.n===n&&p.el.scrollIntoView)p.el.scrollIntoView({block:'center',behavior:'smooth'})});
    schedule();
  }

  window.addEventListener('message',function(e){
    if(e.source!==window.parent||!e.data||typeof e.data!=='object'||e.data.rc!==1)return;
    var m=e.data;
    if(m.type==='enable'){
      parentOrigin=e.origin;ensureHost();
      if(!observer&&window.MutationObserver){observer=new MutationObserver(schedule);observer.observe(document.documentElement,{subtree:true,childList:true,attributes:true,characterData:true})}
      send('enabled');return;
    }
    if(e.origin!==parentOrigin)return;
    if(m.type==='pick')startPick();
    else if(m.type==='cancel-pick')stopPick();
    else if(m.type==='pins')setPins(Array.isArray(m.pins)?m.pins:[]);
    else if(m.type==='focus')focusPin(Number(m.n));
  });
  window.addEventListener('scroll',schedule,true);
  window.addEventListener('resize',schedule);
  window.parent.postMessage({rc:1,type:'ready'},'*');
})();
