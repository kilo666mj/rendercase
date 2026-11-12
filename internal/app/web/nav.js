(function(){
  // An artifact frame can't navigate the page (no allow-top-navigation), so it asks: {rc:1,type:'open',artifact:'a_…'}. Only this site's own viewer path is reachable.
  var frame=document.querySelector('iframe.viewer');if(!frame)return;
  var contentOrigin=frame.dataset.contentOrigin;
  window.addEventListener('message',function(e){
    if(e.source!==frame.contentWindow||(e.origin!=='null'&&e.origin!==contentOrigin))return;
    // A click in the frame activates this page too; without one an artifact could redirect on load.
    if(!navigator.userActivation||!navigator.userActivation.isActive)return;
    var m=e.data;
    if(m===null||typeof m!=='object'||m.rc!==1||m.type!=='open'||typeof m.artifact!=='string'||!/^a_[a-z0-9]{1,64}$/.test(m.artifact))return;
    window.location.assign('/a/'+m.artifact);
  });
})();
