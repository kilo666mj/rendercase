(function(){
  var panel=document.getElementById('comments-panel');if(!panel)return;
  var artifact=panel.dataset.artifact,version=Number(panel.dataset.version),contentOrigin=panel.dataset.contentOrigin;
  var frame=document.querySelector('iframe.viewer'),openButton=document.getElementById('comments-open'),count=document.getElementById('comments-count');
  var list=document.getElementById('thread-list'),unanchoredList=document.getElementById('unanchored-list'),unanchoredHead=document.getElementById('unanchored-head'),status=document.getElementById('comments-status');
  var composer=document.getElementById('comment-composer'),composerBody=document.getElementById('composer-body'),composerQuote=document.getElementById('composer-quote'),composerError=document.getElementById('composer-error');
  var pick=document.getElementById('comment-pick'),state=document.getElementById('comments-state'),scope=document.getElementById('comments-scope');
  var threads=[],unanchored=[],pendingAnchor=null,bridgeReady=false,picking=false,loaded=false,focusedId=null,loadSeq=0,pinGen=0;
  var threadsURL='/api/v1/artifacts/'+encodeURIComponent(artifact)+'/threads';

  async function call(method,url,body){
    var opts={method:method,headers:{}};
    if(body!==undefined){opts.headers['Content-Type']='application/json';opts.body=JSON.stringify(body)}
    var response=await fetch(url,opts),type=response.headers.get('Content-Type')||'',data=null;
    if(type.includes('application/json'))data=await response.json();
    if(!response.ok)throw new Error((data&&data.error)||('Request failed with status '+response.status+'.'));
    return data;
  }
  function el(tag,cls,text){var n=document.createElement(tag);if(cls)n.className=cls;if(text!==undefined)n.textContent=text;return n}
  function button(cls,text){var b=el('button',cls,text);b.type='button';return b}
  function when(v){return v?new Date(v).toLocaleString():''}
  function isOpen(){return !panel.classList.contains('hidden')}
  function showError(err){status.textContent=err.message}

  function toFrame(type,data){var m=data||{};m.rc=1;m.type=type;if(frame&&frame.contentWindow)frame.contentWindow.postMessage(m,'*')}
  function fromFrame(e){return e.source===frame.contentWindow&&(e.origin==='null'||e.origin===contentOrigin)&&e.data!==null&&typeof e.data==='object'&&e.data.rc===1}
  function sendPins(){
    if(!bridgeReady)return;
    var pins=[];
    if(isOpen())threads.forEach(function(t,i){if(t.anchor)pins.push({n:i+1,anchor:t.anchor,resolved:!!t.resolved_at})});
    pinGen++;toFrame('pins',{gen:pinGen,pins:pins});
  }
  function setPicking(on){picking=on;pick.textContent=on?'Click an element… (Esc cancels)':'Add comment';pick.setAttribute('aria-pressed',on?'true':'false')}
  function cancelPick(){if(picking){toFrame('cancel-pick');setPicking(false)}}

  function str(v,n){return typeof v==='string'&&v.length>0&&v.length<=n?v:null}
  function printable(v){return str(v,512)&&new TextEncoder().encode(v).length<=512&&!/[\u0000-\u001f\u007f]/.test(v)?v:null}
  function unit(v){return typeof v==='number'&&v>=0&&v<=1?v:null}
  function cleanAnchor(a){
    if(!a||typeof a!=='object')return null;
    var selector=printable(a.selector);if(!selector)return null;
    var out={selector:selector};
    if(a.page!==undefined&&a.page!==''){var page=printable(a.page);if(!page)return null;out.page=page}
    if(a.quote&&typeof a.quote==='object'){var exact=str(a.quote.exact,500);if(exact){out.quote={exact:exact};var p=str(a.quote.prefix,32),s=str(a.quote.suffix,32);if(p)out.quote.prefix=p;if(s)out.quote.suffix=s}}
    if(a.point&&typeof a.point==='object'){var x=unit(a.point.x),y=unit(a.point.y);if(x!==null&&y!==null)out.point={x:x,y:y}}
    return out;
  }

  function threadNumber(id){for(var i=0;i<threads.length;i++)if(threads[i].id===id)return i+1;return 0}
  function findComment(id){for(var i=0;i<threads.length;i++)for(var j=0;j<threads[i].comments.length;j++)if(threads[i].comments[j].id===id)return threads[i].comments[j];return null}
  function focusThread(id,toPin){
    focusedId=id;
    panel.querySelectorAll('.thread.focused').forEach(function(n){n.classList.remove('focused')});
    var node=panel.querySelector('[data-thread="'+CSS.escape(id)+'"]');if(!node)return;
    node.classList.add('focused');node.scrollIntoView({block:'nearest'});
    var n=threadNumber(id);if(toPin&&n)toFrame('focus',{n:n});
  }

  function renderComment(c){
    var item=el('div','comment'+(c.deleted?' deleted':''));item.dataset.comment=c.id;
    var meta=el('div','comment-meta');
    meta.append(el('strong','',(c.author&&c.author.display_name)||'Unknown'),el('span','session',' · '+when(c.created_at)+(c.edited_at?' · edited':'')));
    item.append(meta,el('p','comment-body',c.deleted?'Comment deleted':c.body));
    if(c.can_edit||c.can_delete){
      var row=el('div','comment-actions');
      if(c.can_edit){var edit=button('link-button','Edit');edit.dataset.edit=c.id;row.append(edit)}
      if(c.can_delete){var del=button('link-button danger-text','Delete');del.dataset.delete=c.id;row.append(del)}
      item.append(row);
    }
    return item;
  }
  function renderThread(t,n){
    var li=el('li','thread'+(t.resolved_at?' resolved':''));li.dataset.thread=t.id;
    var head=el('div','thread-head');head.append(el('span','thread-number',String(n)));
    if(t.version!==version)head.append(el('span','thread-version','v'+t.version));
    if(t.anchor&&t.anchor.page)head.append(el('span','thread-page',t.anchor.page));
    if(t.anchor)head.append(el('span','thread-quote',(t.anchor.quote&&t.anchor.quote.exact)||t.anchor.selector));
    else head.append(el('span','thread-quote','Whole version'));
    li.append(head);
    t.comments.forEach(function(c){li.append(renderComment(c))});
    var actions=el('div','thread-actions'),reply=button('link-button','Reply');reply.dataset.reply=t.id;actions.append(reply);
    if(t.can_resolve){var resolve=button('link-button',t.resolved_at?'Reopen':'Resolve');resolve.dataset.resolve=t.id;resolve.dataset.resolved=t.resolved_at?'false':'true';actions.append(resolve)}
    li.append(actions);
    return li;
  }
  function render(){
    list.replaceChildren();unanchoredList.replaceChildren();
    threads.forEach(function(t,i){var n=i+1;(t.anchor&&unanchored.indexOf(n)>=0?unanchoredList:list).append(renderThread(t,n))});
    unanchoredHead.classList.toggle('hidden',!unanchoredList.children.length);
    var f=focusedId&&panel.querySelector('[data-thread="'+CSS.escape(focusedId)+'"]');if(f)f.classList.add('focused');
  }

  async function refreshCount(){
    try{var data=await call('GET',threadsURL+'?version=all&resolved=false');count.textContent=String((data.threads||[]).length)}catch(_){count.textContent='?'}
  }
  async function load(){
    var seq=++loadSeq;status.textContent='Loading…';
    try{
      var data=await call('GET',threadsURL+'?resolved='+encodeURIComponent(state.value)+'&version='+(scope.value==='all'?'all':String(version)));
      if(seq!==loadSeq)return;
      threads=data.threads||[];unanchored=[];
      status.textContent=threads.length?'':'No comments yet.';
      render();sendPins();
    }catch(err){if(seq===loadSeq)showError(err)}
    refreshCount();
  }

  function inlineForm(initial,label,submitAction){
    var form=el('form','inline-comment-form'),area=el('textarea'),actions=el('div','form-actions'),cancel=button('link-button','Cancel'),submit=el('button','button',label),error=el('div','form-error');
    area.rows=3;area.maxLength=10000;area.required=true;area.value=initial||'';area.setAttribute('aria-label',label);
    submit.type='submit';error.setAttribute('role','alert');
    cancel.addEventListener('click',function(){form.remove()});
    form.addEventListener('submit',async function(e){
      e.preventDefault();error.textContent='';submit.disabled=true;
      try{await submitAction(area.value);await load()}catch(err){error.textContent=err.message;submit.disabled=false}
    });
    actions.append(cancel,submit);form.append(area,actions,error);
    setTimeout(function(){area.focus()},0);
    return form;
  }
  function onListClick(e){
    if(e.target.closest('form'))return;
    var b=e.target.closest('button'),li=e.target.closest('[data-thread]');
    if(!b){if(li)focusThread(li.dataset.thread,true);return}
    if(b.dataset.reply){
      if(!li.querySelector('.inline-comment-form'))li.append(inlineForm('','Reply',function(body){return call('POST','/api/v1/threads/'+encodeURIComponent(b.dataset.reply)+'/comments',{body:body})}));
    }else if(b.dataset.edit){
      var item=b.closest('[data-comment]'),c=findComment(b.dataset.edit);
      if(c&&!item.querySelector('.inline-comment-form'))item.append(inlineForm(c.body,'Save',function(body){return call('PATCH','/api/v1/comments/'+encodeURIComponent(c.id),{body:body})}));
    }else if(b.dataset.delete){
      if(confirm('Delete this comment?'))call('DELETE','/api/v1/comments/'+encodeURIComponent(b.dataset.delete)).then(load).catch(showError);
    }else if(b.dataset.resolve){
      call('PUT','/api/v1/threads/'+encodeURIComponent(b.dataset.resolve)+'/resolution',{resolved:b.dataset.resolved==='true'}).then(load).catch(showError);
    }
  }

  function showPanel(open){
    panel.classList.toggle('hidden',!open);openButton.setAttribute('aria-expanded',open?'true':'false');
    if(!open)cancelPick();
    if(open&&!loaded){loaded=true;load()}else sendPins();
  }
  function openComposer(anchor){
    var wasOpen=!composer.classList.contains('hidden');
    pendingAnchor=anchor;if(!isOpen())showPanel(true);composerError.textContent='';
    composerQuote.textContent=anchor?((anchor.quote&&anchor.quote.exact)||'Selected element'):'Whole version';
    composerQuote.classList.remove('hidden');composer.classList.remove('hidden');if(!wasOpen)composerBody.value='';composerBody.focus();
  }
  function closeComposer(){pendingAnchor=null;composer.classList.add('hidden');composerBody.value=''}

  composer.addEventListener('submit',async function(e){
    e.preventDefault();composerError.textContent='';
    var body={version:version,body:composerBody.value};if(pendingAnchor)body.anchor=pendingAnchor;
    try{var data=await call('POST',threadsURL,body);closeComposer();state.value='false';await load();if(data&&data.thread)focusThread(data.thread.id,true)}catch(err){composerError.textContent=err.message}
  });
  document.getElementById('composer-cancel').addEventListener('click',closeComposer);
  document.getElementById('comment-general').addEventListener('click',function(){openComposer(null)});
  document.getElementById('comments-close').addEventListener('click',function(){showPanel(false)});
  openButton.addEventListener('click',function(){showPanel(!isOpen())});
  pick.addEventListener('click',function(){if(!bridgeReady)return;if(picking){cancelPick()}else{showPanel(true);toFrame('pick');setPicking(true)}});
  document.addEventListener('keydown',function(e){if(e.key==='Escape')cancelPick()});
  state.addEventListener('change',load);scope.addEventListener('change',load);
  list.addEventListener('click',onListClick);unanchoredList.addEventListener('click',onListClick);

  window.addEventListener('message',function(e){
    if(!fromFrame(e))return;
    var m=e.data;
    if(m.type==='ready')toFrame('enable');
    else if(m.type==='enabled'){bridgeReady=true;pick.disabled=false;pick.title='';sendPins()}
    else if(m.type==='picked'){if(!picking)return;setPicking(false);var anchor=cleanAnchor(m.anchor);if(anchor)openComposer(anchor)}
    else if(m.type==='pick-cancelled')setPicking(false);
    else if(m.type==='pin-click'){var n=m.n;if(Number.isInteger(n)&&n>0&&n<=threads.length){if(!isOpen())showPanel(true);focusThread(threads[n-1].id,false)}}
    else if(m.type==='anchors'){
      if(m.gen!==pinGen)return;
      var next=Array.isArray(m.unanchored)?m.unanchored.slice(0,500).filter(function(n){return Number.isInteger(n)&&n>0&&n<=threads.length}):[];
      next=next.filter(function(n,i){return next.indexOf(n)===i}).sort(function(a,b){return a-b});
      if(next.join(',')===unanchored.join(','))return;
      unanchored=next;render();
    }
  });
  frame.addEventListener('load',function(){bridgeReady=false;pick.disabled=true;setPicking(false);toFrame('enable')});
  pick.disabled=true;pick.title='Inline comments are available once the artifact loads. HTML pages over 5 MiB support whole-version comments only.';
  toFrame('enable');refreshCount();
})();
