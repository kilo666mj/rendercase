(function(){
  var dialog=document.getElementById('share-dialog'), artifact=dialog.dataset.artifact, version=Number(dialog.dataset.version);
  var list=document.getElementById('share-list'), error=document.getElementById('share-error'), result=document.getElementById('share-result'), url=document.getElementById('share-url'), currentShareID='';
  var artifactVisibility=document.getElementById('artifact-visibility'), visibilityError=document.getElementById('visibility-error');
  function status(text){var node=document.createElement('span');node.className='session';node.textContent=text;list.replaceChildren(node)}
  async function responseData(response){
    var type=response.headers.get('Content-Type')||'';
    if(type.includes('application/json'))return response.json();
    var message=(await response.text()).trim();
    return {error:message||('Request failed with status '+response.status+'.')};
  }
  function summary(share){
    var parts=['Version '+(share.Version||version), share.ViewCount+' view'+(share.ViewCount===1?'':'s')];
    if(share.ViewLimit)parts[1]+=' / '+share.ViewLimit;
    if(share.ExpiresAt)parts.push('expires '+new Date(share.ExpiresAt).toLocaleString());
    if((share.ExpiresAt&&new Date(share.ExpiresAt)<=new Date())||(share.ViewLimit&&share.ViewCount>=share.ViewLimit))parts.push('inactive');
    return parts.join(' · ');
  }
  async function load(){
    status('Loading…');
    var response=await fetch('/api/v1/artifacts/'+encodeURIComponent(artifact)+'/shares');
    if(!response.ok)throw new Error('Could not load existing links.');
    var shares=(await response.json()).shares||[];
    if(!shares.length){status('No active links.');return}
    list.replaceChildren();
    shares.forEach(function(s){
      var item=document.createElement('div'), details=document.createElement('div'), id=document.createElement('strong'), description=document.createElement('p'), button=document.createElement('button');
      item.className='share-item';id.textContent=String(s.ID);description.textContent=summary(s);
      button.className='danger-button';button.dataset.revoke=String(s.ID);button.type='button';button.textContent='Revoke';
      details.append(id,description);item.append(details,button);list.append(item);
    });
  }
  document.getElementById('share-open').addEventListener('click',function(){dialog.showModal();load().catch(function(e){error.textContent=e.message})});
  document.getElementById('share-close').addEventListener('click',function(){dialog.close()});
  dialog.addEventListener('click',function(e){if(e.target===dialog)dialog.close()});
  document.getElementById('share-form').addEventListener('submit',async function(e){
    e.preventDefault();error.textContent='';result.classList.add('hidden');
    var body={version:version}, expiry=document.getElementById('share-expiry').value, limit=document.getElementById('share-limit').value;
    if(expiry)body.expires_at=new Date(expiry).toISOString();
    if(limit)body.view_limit=Number(limit);
    try{
      var response=await fetch('/api/v1/artifacts/'+encodeURIComponent(artifact)+'/shares',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
      var data=await responseData(response);if(!response.ok)throw new Error(data.error||'Could not create link.');
      currentShareID=data.share_id;url.value=data.url;result.classList.remove('hidden');await load();
    }catch(err){error.textContent=err.message}
  });
  list.addEventListener('click',async function(e){
    var button=e.target.closest('[data-revoke]');if(!button||!confirm('Revoke this share link immediately?'))return;
    error.textContent='';var response=await fetch('/api/v1/shares/'+encodeURIComponent(button.dataset.revoke),{method:'DELETE'});
    if(!response.ok){var data=await responseData(response);error.textContent=data.error||'Could not revoke link.';return}
    if(button.dataset.revoke===currentShareID){currentShareID='';url.value='';result.classList.add('hidden')}
    await load();
  });
  document.getElementById('share-copy').addEventListener('click',async function(){await navigator.clipboard.writeText(url.value);this.textContent='Copied';var button=this;setTimeout(function(){button.textContent='Copy'},1500)});
  artifactVisibility.addEventListener('change',async function(){
    visibilityError.textContent='';artifactVisibility.disabled=true;var previous=artifactVisibility.value==='authenticated'?'private':'authenticated';
    try{var response=await fetch('/api/v1/artifacts/'+encodeURIComponent(artifact)+'/visibility',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({visibility:artifactVisibility.value})});var data=await responseData(response);if(!response.ok)throw new Error(data.error||'Could not update artifact access.')}catch(err){artifactVisibility.value=previous;visibilityError.textContent=err.message}finally{artifactVisibility.disabled=false}
  });
})();
