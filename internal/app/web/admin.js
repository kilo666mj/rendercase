(function(){
  var error=document.getElementById('admin-error');
  document.getElementById('theme-select').addEventListener('change',async function(){
    var response=await fetch('/api/v1/admin/branding',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify({activate_theme:this.value})});
    if(response.ok)location.reload();else document.getElementById('branding-status').textContent='Could not activate theme.'
  });
  document.getElementById('branding-form').addEventListener('submit',async function(event){
    event.preventDefault();
    var form=event.currentTarget,status=document.getElementById('branding-status'),file=document.getElementById('brand-logo-input').files[0],favicon=document.getElementById('brand-favicon-input').files[0],body={};
    status.textContent='Saving…';
    new FormData(form).forEach(function(value,key){if(typeof value==='string')body[key]=value});
    var removeFavicon=document.getElementById('remove-favicon');body.remove_favicon=!!(removeFavicon&&removeFavicon.checked);
    var remove=document.getElementById('remove-logo');body.remove_logo=!!(remove&&remove.checked);
    if(file){if(file.size>524288){status.textContent='Logo is larger than 512 KiB.';return}body.logo_data_url=await new Promise(function(resolve,reject){var reader=new FileReader();reader.onload=function(){resolve(reader.result)};reader.onerror=reject;reader.readAsDataURL(file)})}
    if(favicon){if(favicon.size>524288){status.textContent='Favicon is larger than 512 KiB.';return}body.favicon_data_url=await new Promise(function(resolve,reject){var reader=new FileReader();reader.onload=function(){resolve(reader.result)};reader.onerror=reject;reader.readAsDataURL(favicon)})}
    var response=await fetch('/api/v1/admin/branding',{method:'PUT',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
    if(!response.ok){var data=await response.json().catch(function(){return {}});status.textContent=data.error||'Could not save appearance.';return}
    location.reload()
  });
  document.addEventListener('click',async function(event){
    var share=event.target.closest('[data-revoke-share]'),artifact=event.target.closest('[data-delete-artifact]'),theme=event.target.closest('[data-delete-theme]');
    if(theme){if(!confirm('Delete saved theme '+theme.dataset.deleteTheme+'?'))return;var response=await fetch('/api/v1/admin/branding/themes/'+encodeURIComponent(theme.dataset.deleteTheme),{method:'DELETE'});if(!response.ok){error.textContent='Could not delete theme.';return}theme.remove();return}
    if(share){if(!confirm('Revoke this capability link?'))return;var response=await fetch('/api/v1/admin/shares/'+encodeURIComponent(share.dataset.revokeShare),{method:'DELETE'});if(!response.ok){error.textContent='Could not revoke share.';return}share.closest('[data-share]').remove()}
    if(artifact){if(!confirm('Remove this artifact and revoke all of its shares? This hides every version immediately.'))return;var response=await fetch('/api/v1/admin/artifacts/'+encodeURIComponent(artifact.dataset.deleteArtifact),{method:'DELETE'});if(!response.ok){error.textContent='Could not remove artifact.';return}artifact.closest('[data-artifact]').remove()}
  });
})();
