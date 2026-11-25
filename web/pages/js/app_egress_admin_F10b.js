import { request, getCSRFHeaders, poolSettingsId } from './api.js';

// Separate settings, not a change to the existing pool settings document.
export function mountAppEgressPanel(content, canAdmin, pageId, showError, showSuccess) {
  const pool = poolSettingsId(pageId);
  if (!pool || !canAdmin || !content?.ownerDocument || typeof content.appendChild !== 'function') return;
  const doc=content.ownerDocument;
  const panel=doc.createElement('section');
  panel.className='admin-panel';
  panel.innerHTML='<h3>Application isolation and network access</h3><p>One approved application per physical tab. Idle release is measured from observed idle state.</p><label>Tab release mode <select data-release-mode><option value="idle-timeout">Idle timeout</option><option value="immediate-when-idle">Immediate when idle</option></select></label> <label>Idle seconds <input data-idle-seconds type="number" min="1" max="3600" value="30"></label> <button type="button" data-save-release disabled>Save release policy</button><p data-release-status role="status">Loading...</p><h4>Approve application</h4><label>Developer key owner <input data-app-owner></label> <label>App ID <input data-app-id></label> <label>Quota (1–64) <input data-app-quota type="number" min="1" max="64" value="1"></label><p><label>Approved HTTPS/WSS origins, one per line<br><textarea data-app-endpoints rows="4"></textarea></label></p><p><label>Optional HTTPS developer token service URL <input data-token-service type="url"></label></p><button type="button" data-read-app>Load approval</button> <button type="button" data-save-app>Approve / update</button><p data-app-status role="status"></p>';
  content.appendChild(panel);
  const get = name => panel.querySelector('[data-'+name+']');
  const mode=get('release-mode'), idle=get('idle-seconds'), save=get('save-release'), rs=get('release-status');
  const route='/admin/v1/pools/'+pool+'/tab-release';
  const render=v=>{mode.value=v.mode;idle.value=v.idleSeconds;rs.textContent='Active: '+v.mode+' ('+v.idleSeconds+' s)';save.disabled=false;};
  request(route).then(render).catch(err=>{rs.textContent='Release policy unavailable: '+err.message;});
  save.addEventListener('click',async()=>{
    save.disabled=true;
    try{render(await request(route,{method:'PUT',headers:getCSRFHeaders(),body:JSON.stringify({mode:mode.value,idleSeconds:Number(idle.value)})}));showSuccess?.('Tab release policy saved.');}
    catch(err){rs.textContent='Save failed: '+err.message;showError?.(err);save.disabled=false;}
  });
  const appRoute=()=>'/admin/v1/'+(pool==='tp'?'thread':'process')+'-apps/'+encodeURIComponent(get('app-id').value.trim());
  const owner=()=>get('app-owner').value.trim();
  const quota=pool==='tp'?'maxWorkers':'maxTabs';
  const status=get('app-status');
  get('read-app').addEventListener('click',async()=>{
    try{const v=await request(appRoute()+'?owner='+encodeURIComponent(owner()));get('app-quota').value=v[quota];get('app-endpoints').value=(v.egress?.endpoints||[]).join('\n');get('token-service').value=v.egress?.tokenServiceUrl||'';status.textContent='Approval loaded.';}
    catch(err){status.textContent='Load failed: '+err.message;}
  });
  get('save-app').addEventListener('click',async()=>{
    try{const body={owner:owner(),[quota]:Number(get('app-quota').value),egress:{endpoints:get('app-endpoints').value.split(/\r?\n/).map(x=>x.trim()).filter(Boolean),tokenServiceUrl:get('token-service').value.trim()}};
      await request(appRoute(),{method:'PUT',headers:getCSRFHeaders(),body:JSON.stringify(body)});status.textContent='Application approved.';showSuccess?.('Application approval saved.');}
    catch(err){status.textContent='Approval failed: '+err.message;showError?.(err);}
  });
}
