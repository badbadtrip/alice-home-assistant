/* Self-contained HA card. All reads/writes use the signed-in HA WebSocket. */
(() => {
  const normalize = text => String(text).toLocaleLowerCase('ru').replaceAll('ё', 'е')
    .replaceAll(/\p{P}+/gu, ' ').replaceAll(/\s+/gu, ' ').trim().replace(/^алиса(?: |$)/u, '').trim();
  const escape = value => String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const supported = entity => /^(automation|script)\./.test(entity.entity_id);
  const aliases = entity => (entity?.aliases ?? []).filter(value => typeof value === 'string');
  class AliceVoiceCard extends HTMLElement {
    constructor() {
      super(); this.attachShadow({mode:'open'});
      this._registry = []; this._draftTarget = ''; this._draftPhrases = ''; this._message = '';
      this._expandedSpeakers=new Set();
      this._stationBusy=new Set();this._stationMessages={};this._mediaPlatforms={};this._utility=null;this._events=[];this._activityPage=0;
      this.shadowRoot.addEventListener('toggle',e=>{if(e.target.id==='home-journal'&&e.target.isConnected){this._journalOpen=e.target.open;if(e.target.open)this._loadActivity();}if(e.target.matches('details[data-speaker]')){const id=e.target.dataset.speaker;if(e.target.open)this._expandedSpeakers.add(id);else this._expandedSpeakers.delete(id);}},true);
      this._tab='commands'; this._speakers=[]; this._assignments={}; this._chosen=[]; this._all=true;
      this.shadowRoot.addEventListener('click', e => this._click(e));
      this.shadowRoot.addEventListener('submit', e => { e.preventDefault(); if(e.target.id==='utility-form')return this._submitUtility();e.target.id==='speaker-form' ? this._register(e.target) : this._save(); });
      this.shadowRoot.addEventListener('keydown',e=>{
        if(e.composedPath().some(el=>el?.localName==='ha-entity-picker'))return;
        if(e.key==='Escape' && this._dialog && !this._busy){this._dialog=false;this._render();}
        if(e.key==='Escape'&&this._utility&&!this._utility.busy){this._utility=null;this._render();}
      });
      this.shadowRoot.addEventListener('input', e => {
        if (e.target.id === 'phrases') this._draftPhrases = e.target.value;
        if(e.target.dataset.volume){this._volumeDragging=e.target.dataset.volume;e.target.style.setProperty('--volume',e.target.value+'%');const output=this.shadowRoot.querySelector('[data-volume-output="'+e.target.dataset.volume+'"]');if(output)output.textContent=e.target.value+' %';return;}
        if(this._utility&&e.target.dataset.utilityField)this._utility[e.target.dataset.utilityField]=e.target.value;
      });
      this.shadowRoot.addEventListener('change', e => {
        if(e.target.dataset.volume){this._volumeDragging=undefined;return this._mediaAction(e.target.dataset.volume,'volume_set',{volume_level:Number(e.target.value)/100});}
        if(e.target.name==='tts-speaker'&&this._utility){this._utility.speakers=[...this.shadowRoot.querySelectorAll('[name=tts-speaker]:checked')].map(el=>el.value);return;}
        if(e.target.id==='all-speakers'){this._all=e.target.checked;this._renderAudience();return;}
        if(e.target.name==='speaker'){this._chosen=[...this.shadowRoot.querySelectorAll('[name=speaker]:checked')].map(el=>el.value);return;}
      });
      this.shadowRoot.addEventListener('value-changed', e => {
        if(e.target.id==='settings-media'&&this._utility){this._utility.media_entity=e.detail.value??'';e.stopPropagation();return;}
        if(e.target.id==='settings-area'&&this._utility){this._utility.area_id=e.detail.value?.area??'';e.stopPropagation();return;}
        if(e.target.id!=='target')return;
        e.stopPropagation();
        this._draftTarget = typeof e.detail?.value==='string'?e.detail.value:'';
        this._chosen=this._assignments[this._draftTarget]??[];this._all=this._assignments[this._draftTarget]===undefined;
        this._renderAudience();
      });
    }
    setConfig(config) {
      if (!config.label) throw new Error('Укажите метку голосовых команд');
      this._config = config; if(config.view)this._tab=config.view; this._applyEmbeddedLayout(); this._render();
    }
    set hass(hass) {
      this._hass = hass;
      const picker=this.shadowRoot.getElementById('target');if(picker)picker.hass=hass;
      this._refreshMedia();
      if (!this._loading && !this._loaded) this._load();
      if (!this._subscribing && !this._unsubscribe && this.isConnected) this._subscribe();
    }
    _applyEmbeddedLayout() {
      this._embeddedStyle?.remove();this._embeddedStyle=undefined;
      this._embeddedDrawerStyle?.remove();this._embeddedDrawerStyle=undefined;
      if(!this._config?.app_embedded)return;
      let node=this;
      while(node){
        if(node.localName==='hui-root'&&node.shadowRoot){
          const style=document.createElement('style');
          style.dataset.aliceEmbedded='true';
          style.textContent=':host{--header-height:0px!important;--app-header-height:0px!important}app-header,.header{display:none!important}#view{padding-top:0!important;min-height:100dvh;background:#101218}hui-view-background{display:none!important}';
          node.shadowRoot.append(style);this._embeddedStyle=style;
        }
        if(node.localName==='ha-drawer'&&node.shadowRoot){
          const style=document.createElement('style');
          style.dataset.aliceEmbedded='true';
          style.textContent='.sidebar-shell{display:none!important}.app-content{padding:0!important;margin:0!important}.scrim{display:none!important}';
          node.shadowRoot.append(style);this._embeddedDrawerStyle=style;
        }
        node=node.parentNode??node.getRootNode()?.host;
      }
    }
    connectedCallback() {
      this._applyEmbeddedLayout();
      if (this._hass) {this._load(); this._subscribe();}
      clearInterval(this._connectionTimer);
      this._connectionTimer=setInterval(async()=>{
        if(this._busy||!this._hass)return;
        await this._loadManager();this._renderEditorStatus();
        if(['home','speakers'].includes(this._tab)&&!this._speakerDialog){
          for(const speaker of this._speakers){const region=this.shadowRoot.querySelector('[data-connection="'+speaker.id+'"]');if(region)region.innerHTML=this._connectionDetails(speaker)+this._delivery(speaker);}
          const summary=this.shadowRoot.getElementById('speaker-summary');if(summary)summary.textContent=this._speakerSummary();const setup=this.shadowRoot.getElementById('setup-notice-region');if(setup){const markup=this._setupNotice();if(setup.innerHTML!==markup)setup.innerHTML=markup;}
        }
      },15000);
    }
    disconnectedCallback() {
      this._embeddedStyle?.remove();this._embeddedStyle=undefined;
      this._embeddedDrawerStyle?.remove();this._embeddedDrawerStyle=undefined;
      this._unsubscribe?.(); this._unsubscribe = undefined;
      clearTimeout(this._refreshTimer);clearInterval(this._connectionTimer);
    }
    getCardSize() { return 9; }
    async _subscribe() {
      if (this._subscribing || this._unsubscribe) return;
      this._subscribing = true;
      try {
        const unsubscribe = await this._hass.connection.subscribeEvents(() => {
          clearTimeout(this._refreshTimer);
          this._refreshTimer = setTimeout(() => this._load(), 300);
        }, 'entity_registry_updated');
        if (this.isConnected) this._unsubscribe = unsubscribe; else unsubscribe();
      } catch (error) { this._message = 'Обновление по событию недоступно. Используйте «Обновить».'; }
      finally { this._subscribing = false; }
    }
    async _records() {
      const records = await this._hass.callWS({type:'config/entity_registry/list'});
      return Promise.all(records.filter(supported).map(item => this._hass.callWS({
        type:'config/entity_registry/get', entity_id:item.entity_id
      })));
    }
    async _load() {
      if (!this._hass || this._loading) return;
      this._loading = true;
      try {
        this._registry = await this._records();
        await this._loadManager();
        if(this._tab==='diagnostics'||this._journalOpen)await this._loadActivity();
        this._loaded = true;
      } catch(error) { this._message = 'Не удалось прочитать реестр: ' + (error.message ?? error); }
      finally { this._loading = false; if(!this._dialog&&!this._speakerDialog&&!this._utility&&!this._volumeDragging)this._render(); }
    }
    _name(item) { return this._hass?.states[item.entity_id]?.attributes.friendly_name ?? item.name ?? item.original_name ?? item.entity_id; }
    _selected(item) { return (item.labels ?? []).includes(this._config.label); }
    async _ensureNativePicker() {
      if(customElements.get('ha-entity-picker'))return;
      if(!this._pickerLoading)this._pickerLoading=(async()=>{
        if(typeof window.loadCardHelpers!=='function')throw Error('Загрузка компонентов Home Assistant недоступна');
        const helpers=await window.loadCardHelpers();
        const card=helpers.createCardElement({type:'entities',entities:[]});
        await customElements.whenDefined('hui-entities-card');
        await card.constructor.getConfigElement();
        await customElements.whenDefined('ha-entity-picker');
      })();
      let timeout;
      try{await Promise.race([this._pickerLoading,new Promise((_,reject)=>{timeout=setTimeout(()=>reject(Error('Истекло время загрузки')),12000);})]);}
      catch(error){this._pickerLoading=undefined;throw error;}
      finally{clearTimeout(timeout);}
    }
    async _bindNativePicker() {
      if(!this._dialog)return;
      try{
        await this._ensureNativePicker();
        const picker=this.shadowRoot.getElementById('target');if(!picker)return;
        picker.hass=this._hass;picker.includeDomains=['automation','script'];picker.allowCustomEntity=false;
        picker.label='Что выполнить';picker.value=this._draftTarget;picker.required=true;picker.disabled=!!this._editing||!!this._busy;
        this._pickerError='';
      }catch(error){this._pickerError='Не удалось загрузить выбор действия Home Assistant. Попробуйте ещё раз.';}
      this._renderEditorStatus();
    }
    _renderEditorStatus(){
      const region=this.shadowRoot.getElementById('editor-service-status');if(!region)return;
      const unavailable=this._managerError||!this._config.manager_url;
      region.innerHTML=unavailable?'<div class="notice" role="status">Нет связи с сервисом колонок. Проверьте подключение и попробуйте снова. Введённая команда сохранена в форме.<button type="button" data-action="retry-editor">Повторить</button></div>':this._pickerError?`<div class="notice" role="status">${escape(this._pickerError)}<button type="button" data-action="retry-editor">Повторить</button></div>`:'';
      const save=this.shadowRoot.getElementById('save-command');if(save)save.disabled=!!this._busy||!!unavailable||!!this._pickerError||!customElements.get('ha-entity-picker');
    }
    _renderAudience(){const region=this.shadowRoot.getElementById('audience');if(region)region.innerHTML=this._audienceFields();}
    _audienceFields(){return `<fieldset style="border:0;padding:0;margin:0"><legend style="font-size:14px;font-weight:500;margin-bottom:12px">На каких колонках</legend><label class="check"><input id="all-speakers" type="checkbox" ${this._all?'checked':''}>Все колонки, включая новые</label>${!this._all?this._speakers.map(s=>`<label class="check" style="margin-top:10px"><input name="speaker" value="${escape(s.id)}" type="checkbox" ${this._chosen.includes(s.id)?'checked':''}>${escape(s.name)}${s.area?' · '+escape(s.area):''}</label>`).join(''):''}</fieldset>`;}
    async _managerBase(){
      if(!this._config.manager_app)return this._config.manager_url?.replace(/\/$/,'');
      if(this._ingress&&Date.now()-this._ingress.created<20*60*1000)return this._ingress.url;
      if(!this._ingressPending)this._ingressPending=(async()=>{
        const [info,session]=await Promise.all([
          this._hass.callWS({type:'supervisor/api',endpoint:'/addons/'+this._config.manager_app+'/info',method:'GET'}),
          this._hass.callWS({type:'supervisor/api',endpoint:'/ingress/session',method:'POST',data:{}})
        ]);
        if(info.state!=='started'||!info.ingress_url?.startsWith('/api/hassio_ingress/')||!session.session?.match(/^[a-zA-Z0-9._-]+$/))throw Error('Подключение к сервису колонок ещё не готово.');
        document.cookie='ingress_session='+session.session+';path=/api/hassio_ingress/;SameSite=Strict'+(location.protocol==='https:'?';Secure':'');
        this._appPort=info.network?.['18080/tcp'];this._ingress={url:info.ingress_url.replace(/\/$/,''),created:Date.now()};return this._ingress.url;
      })().finally(()=>{this._ingressPending=undefined;});
      return this._ingressPending;
    }
    async _api(path,method='GET',body,retry=true) {
      if(!this._config.manager_url&&!this._config.manager_app)throw Error('Подключение к сервису колонок не настроено.');
      if(this._hass.auth?.expired)await this._hass.auth.refreshAccessToken();
      const token=this._hass.auth?.data?.access_token;
      if(!token)throw Error('Обновите вход в Home Assistant.');
      try{
        const base=await this._managerBase();
        const response=await fetch(base+path,{method,credentials:'same-origin',headers:{Authorization:'Bearer '+token,'Content-Type':'application/json'},body:body===undefined?undefined:JSON.stringify(body),signal:AbortSignal.timeout(15000)});
        if(retry&&this._config.manager_app&&(response.status===401||response.status===403)){this._ingress=undefined;return this._api(path,method,body,false);}
        const data=await response.json();if(!response.ok)throw Error(data.message??data.error??'Не удалось выполнить операцию.');return data;
      }catch(error){if(error instanceof TypeError||error.name==='TimeoutError'||error.name==='AbortError')throw Error('Нет связи с сервисом колонок. Повторите подключение.');throw error;}
    }
    async _loadManager() {
      if(!this._config.manager_url) return;
      try {const [data,assign]=await Promise.all([this._api('/api/speakers'),this._api('/api/assignments')]);this._speakers=data.speakers??[];this._key=data.public_key;this._installationReady=data.installation_ready;this._advertisedUrl=data.advertised_url;this._assignments=assign.assignments??{};this._managerError='';await Promise.all(this._speakers.filter(s=>s.media_entity).map(async speaker=>{try{const entry=await this._hass.callWS({type:'config/entity_registry/get',entity_id:speaker.media_entity});this._mediaPlatforms[speaker.media_entity]=entry?.platform??null;}catch{this._mediaPlatforms[speaker.media_entity]=null;}}));}
      catch(error){this._managerError=error.message;}
      this._resumeAddressSetup();
    }
    _addressResumeKey(){return 'alice-address-resume:'+this._config.manager_app;}
    _resumeAddressSetup(){
      try{const key=this._addressResumeKey(),saved=JSON.parse(sessionStorage.getItem(key)||'null');if(!saved)return;
        if(Date.now()-saved.at>600000){sessionStorage.removeItem(key);return;}
        if(this._managerError||!this._installationReady||this._advertisedUrl!==saved.url)return;
        if(!this._utility&&this._speakers.some(s=>s.id===saved.speakerId)){this._utility={type:'settings',section:'bridge',speakerId:saved.speakerId,message:'Адрес сохранён. Приложение перезапущено. Продолжим подключение колонки.'};this._render();}
        sessionStorage.removeItem(key);
      }catch{}
    }
    _audience(target){const ids=this._assignments[target];return ids===undefined?'Все колонки':ids.length?ids.map(id=>this._speakers.find(s=>s.id===id)?.name??id).join(' · '):'Не назначено ни одной колонке';}
    _render() {
      if(!this._config)return;
      const disabled=this._busy?'disabled':'';
      const rows=this._registry.filter(item=>this._selected(item));
      this.shadowRoot.innerHTML=`<style>
      :host{display:block;color:var(--primary-text-color,#202124);font-family:var(--ha-font-family-body,Roboto,Arial,sans-serif);font-size:16px;--surface:var(--ha-card-background,var(--card-background-color,#fff));--muted:color-mix(in srgb,var(--primary-text-color,#202124) 78%,transparent);--line:color-mix(in srgb,var(--primary-text-color,#202124) 22%,transparent);--accent:var(--primary-color,#03a9f4);--on-accent:var(--primary-background-color,#fff)}
      .app-url-label{margin:18px 0 10px}.app-url-label input{font-size:15px}.app-url-label input[aria-invalid=true]{border-color:var(--error-color,#e67676)}
      .media-source{font-size:11px!important;color:var(--muted);margin:0 0 10px!important}.settings-speaker{margin:0 0 14px;font-size:13px}.settings-tabs{display:flex;gap:6px;background:color-mix(in srgb,var(--primary-text-color) 4%,transparent);padding:4px;border-radius:12px;margin-bottom:22px}.settings-tabs button{flex:1;color:var(--muted);border-radius:9px}.settings-tabs button[aria-selected=true]{color:var(--accent);background:color-mix(in srgb,var(--accent) 12%,var(--surface))}.setup-tab-dot{color:light-dark(#8b641c,#e6bd7b);font-size:10px;margin-left:7px}.bridge-intro{padding:16px;border-radius:12px;background:color-mix(in srgb,var(--accent) 8%,var(--surface))}.bridge-intro p{font-size:12px;line-height:1.6;margin-bottom:0}.bridge-progress{display:flex;list-style:none;padding:0;margin:22px 0;gap:8px}.bridge-progress li{flex:1;display:flex;flex-direction:column;gap:6px;align-items:center;font-size:10px;color:var(--muted);min-width:0}.bridge-progress li>span{width:26px;height:26px;border-radius:50%;display:grid;place-items:center;background:color-mix(in srgb,var(--primary-text-color) 6%,transparent)}.bridge-progress .active{color:var(--accent);font-weight:650}.bridge-progress .active>span{background:var(--accent);color:var(--on-accent)}.bridge-progress .complete>span{color:light-dark(#29735d,#7ed5c4);background:#7ed5c420}.bridge-current{border:1px solid var(--line);border-radius:14px;padding:20px}.bridge-current>h3{font-size:19px;line-height:1.4;margin:4px 0 8px}.bridge-current p,.plain-steps{font-size:13px;line-height:1.75}.bridge-kicker{font-size:10px!important;letter-spacing:1.5px;color:var(--accent)!important;margin:0}.bridge-current .key{margin:12px 0}.bridge-subtitle{margin:20px 0 8px}.bridge-navigation{display:flex;justify-content:space-between;margin-top:12px}.bridge-guide{display:inline-block;font-size:12px;color:var(--accent);margin:16px 0;text-decoration:none}.bridge-maintenance{padding-top:18px;margin-top:18px;border-top:1px solid var(--line)}.bridge-maintenance .utility-grid{margin:14px 0}.bridge-ready{display:flex;gap:14px;align-items:center;padding:20px;border:1px solid #7ed5c433;background:#7ed5c40a;border-radius:14px}.bridge-ready>ha-icon{color:light-dark(#29735d,#7ed5c4)}.bridge-ready h3{font-size:17px}.bridge-ready p{font-size:13px}.plain-steps{padding-left:20px}.plain-steps li{margin:10px 0}
      .setup-notice{display:flex;align-items:center;gap:16px;margin:0 0 28px;padding:20px;border:1px solid #e6bd7b55;background:color-mix(in srgb,#e6bd7b 8%,var(--surface));border-radius:16px}.setup-notice>ha-icon{color:#e6bd7b;flex-shrink:0}.setup-notice>div{flex:1}.setup-notice strong{font-size:16px}.setup-notice p{font-size:13px;line-height:1.65;margin:6px 0 0}.setup-notice button{flex-shrink:0}.connection-state.setup{color:light-dark(#855a16,#e6bd7b)}.connection-state.setup .dot{background:currentColor}.setup-hint{font-size:12px!important;line-height:1.5;margin:8px 0 0}.setup-link{padding:6px 0!important;min-height:36px!important;font-size:12px!important}.setup-intro{padding:16px;background:color-mix(in srgb,var(--accent) 9%,var(--surface));border-radius:12px;margin:18px 0;font-size:14px;line-height:1.6}.setup-steps{list-style:none;margin:0;padding:0;display:grid;gap:22px}.setup-step{display:flex;gap:14px;align-items:flex-start}.setup-number{display:grid;place-items:center;flex-shrink:0;width:28px;height:28px;border:1px solid var(--line);border-radius:50%;color:var(--muted);font-size:12px}.setup-step.complete .setup-number{background:#69c5a522;color:light-dark(#26745b,#7ed5c4);border-color:#69c5a566}.setup-step>div{min-width:0;flex:1}.setup-step h3{font-size:15px;line-height:28px;margin:0 0 6px}.setup-step p{font-size:13px;line-height:1.65}.setup-step button{margin:6px 0}.setup-step a{color:var(--accent)}.setup-step .key{margin-top:12px}.setup-step details{margin-bottom:10px}@media(max-width:600px){.setup-notice{flex-wrap:wrap;padding:16px}.setup-notice>div{flex-basis:calc(100% - 48px)}.setup-notice>button{width:100%}}
      *{box-sizing:border-box}main{max-width:1120px;margin:0 auto;padding:36px 28px 64px}h1{font-size:28px;line-height:1.25;margin:0 0 10px;font-weight:600}h2{font-size:20px;margin:0}h3{font-size:16px;margin:0}p{line-height:1.5;margin:6px 0;color:var(--muted)}.heading,.row,.toolbar,.actions{display:flex;align-items:center;gap:12px}.heading,.between{justify-content:space-between}.heading{margin-bottom:26px}.intro{max-width:620px;font-size:14px}.eyebrow{font-size:12px;font-weight:600;letter-spacing:.09em;text-transform:uppercase;color:var(--accent);margin-bottom:10px}.tabs{display:flex;gap:24px;border-bottom:1px solid var(--line);margin-bottom:24px}.tabs button{border-radius:0;padding:14px 2px;color:var(--muted);border-bottom:3px solid transparent}.tabs [aria-selected=true]{color:var(--accent);border-bottom-color:var(--accent)}button{min-height:44px;cursor:pointer;border:1px solid transparent;border-radius:10px;padding:10px 15px;font:500 14px inherit;font-family:inherit;font-size:14px;background:transparent;color:var(--accent)}button.primary{background:var(--accent);color:var(--on-accent);font-weight:600}button.secondary{border-color:var(--line);color:inherit}button:hover{filter:brightness(.94)}button:disabled{opacity:.55;cursor:default}.toolbar{justify-content:space-between;margin:0 0 16px}.count{font-size:14px;color:var(--muted)}.list{display:grid;gap:14px}.command,.box{background:var(--surface);border:1px solid var(--line);border-radius:16px;padding:22px 24px}.command-top{display:flex;align-items:flex-start;justify-content:space-between;gap:16px}.phrase{font-size:19px;font-weight:500;line-height:1.45;margin:0 0 10px;overflow-wrap:anywhere}.target{font-size:14px;color:var(--muted);line-height:1.5}.chips{display:flex;flex-wrap:wrap;gap:7px;margin:12px 0}.chip{font-size:13px;padding:5px 10px;border:1px solid var(--line);border-radius:8px;overflow-wrap:anywhere}.meta{display:flex;gap:16px;flex-wrap:wrap;margin-top:16px;padding-top:14px;border-top:1px solid var(--line);font-size:13px;color:var(--muted)}.status{display:inline-flex;align-items:center;gap:6px}.dot{width:7px;height:7px;border-radius:50%;background:var(--muted)}.ready .dot{background:var(--success-color,#36986b)}details{font-size:13px;color:var(--muted);margin-top:10px}summary{cursor:pointer}code{overflow-wrap:anywhere;white-space:pre-wrap}.empty{text-align:center;padding:52px 24px;border:1px dashed var(--line);border-radius:16px}.empty h2{margin-bottom:8px}.empty p{max-width:440px;margin:0 auto 18px}.notice{padding:14px 18px;border:1px solid var(--line);background:var(--surface);border-radius:12px;margin-bottom:18px;font-size:14px;line-height:1.5}.error{color:var(--error-color,#d64545)}.overlay{position:fixed;inset:0;background:#0008;z-index:1000;display:flex;align-items:center;justify-content:center;padding:20px}.dialog{width:620px;max-height:90dvh;overflow:auto;background:var(--surface);border:1px solid var(--line);border-radius:20px;padding:28px;box-shadow:0 16px 60px #0004}.dialog-header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px}.fields{display:grid;gap:20px}label{display:flex;flex-direction:column;gap:8px;font-size:14px;font-weight:500}input,select,textarea{width:100%;background:var(--surface);color:inherit;font:400 16px inherit;font-family:inherit;font-size:16px;border:1px solid var(--line);border-radius:10px;padding:12px;min-height:48px}textarea{min-height:100px;resize:vertical;line-height:1.5}input[type=checkbox]{width:18px;min-height:18px;height:18px;accent-color:var(--accent)}.check{display:flex;flex-direction:row;align-items:center;font-weight:400}.help{font-size:13px;color:var(--muted);font-weight:400;line-height:1.5}.dialog .actions{justify-content:flex-end;margin-top:26px}.speaker-grid{display:grid;align-items:start;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px}.speaker-grid .box{min-width:0}.speaker-form{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-top:20px}.span{grid-column:1/-1}.section{margin-top:24px}.key{padding:14px;border:1px solid var(--line);border-radius:10px;font-size:12px;user-select:all;margin-top:12px}.box .actions{margin-top:18px;flex-wrap:wrap}.search{margin-top:10px}button:focus-visible,input:focus-visible,textarea:focus-visible,select:focus-visible,summary:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
      .speaker-heading{display:flex;gap:14px;align-items:center;margin-bottom:20px}.speaker-heading ha-icon{color:var(--accent);--mdc-icon-size:30px}.speaker-heading h2{font-size:20px}.speaker-heading p{font-size:14px;margin:4px 0 0}.connection-state{display:inline-flex;align-items:center;gap:7px;padding:7px 10px;border-radius:8px;background:color-mix(in srgb,var(--primary-text-color) 7%,transparent);font-size:14px;font-weight:500}.connection-state.online .dot{background:var(--success-color,#42b883)}.connection-state.offline .dot{background:var(--error-color,#e36565)}.speaker-facts{display:grid;grid-template-columns:1fr 1fr;gap:16px;margin-top:22px}.speaker-facts>div{display:flex;flex-direction:column;gap:7px}.speaker-facts span{font-size:13px;color:var(--muted)}.speaker-facts strong{font-size:15px;font-weight:500}.speaker-details>summary{min-height:44px;padding:12px 0}.speaker-details{padding-top:4px;border-top:1px solid var(--line);margin-top:18px}
      @media(max-width:650px){main{padding:24px 16px 40px}.heading{display:block}.heading>.primary{margin-top:18px}h1{font-size:24px}.heading .primary{white-space:nowrap}.command,.box{padding:18px}.command-top{display:block}.command-top>.actions{margin-top:12px}.phrase{font-size:18px}.speaker-grid,.speaker-form{grid-template-columns:1fr}.span{grid-column:auto}.overlay{padding:0;align-items:stretch}.dialog{width:100%;max-height:100dvh;border-radius:0;padding:24px 20px}.dialog-header{margin-bottom:20px}.intro{font-size:14px}.meta{gap:10px}.tabs{gap:20px}}
      .volume-control{margin-top:22px}.volume-label{display:flex;justify-content:space-between;gap:16px;font-size:14px;margin-bottom:8px}.volume-control input[type=range]{padding:0;min-height:44px;border:0;accent-color:var(--accent);background:transparent}.media-note{font-size:13px;min-height:20px;line-height:1.5;color:var(--muted)}.delivery{font-size:13px;display:inline-flex;gap:7px;align-items:center;margin-top:14px;color:var(--muted)}.delivery .dot{background:var(--muted)}.delivery.synced .dot{background:var(--success-color,#42b883)}.delivery.error .dot{background:var(--error-color,#e36565)}.utility-grid{display:grid;grid-template-columns:1fr 1fr;gap:10px;margin-top:18px}.utility-grid button{text-align:left;border:1px solid var(--line);color:inherit}.dialog .speaker-details{margin-top:22px}.activity-list{display:grid;gap:12px;margin-top:18px}.activity-item{padding:16px;border:1px solid var(--line);border-radius:12px;overflow-wrap:anywhere}.activity-item strong{display:block;font-size:15px;line-height:1.5}.activity-item p{font-size:13px}.result{padding:16px;border:1px solid var(--line);border-radius:12px;margin-top:12px}.result h3{margin-bottom:8px}.result p{font-size:14px}.utility-dialog{width:660px}.dialog ha-entity-picker,.dialog ha-form{display:block;width:100%}

      /* One visual system shared by the overview, controls and dialogs. */
      ${this._config?.app_embedded?`:host{color-scheme:dark;--primary-color:#b6a4ff;--primary-text-color:#edf0f7;--secondary-text-color:#a4adbf;--primary-background-color:#101218;--ha-card-background:#1b1e28;--card-background-color:#1b1e28}`:""}
      :host{--accent:var(--primary-color,#b6a4ff);--muted:var(--secondary-text-color,#9da3b4);--line:color-mix(in srgb,var(--primary-text-color,#fff) 11%,transparent)}
      main{max-width:1440px;padding:28px 32px 48px}button{border-radius:12px;display:inline-flex;align-items:center;justify-content:center;gap:7px;transition:background .15s,box-shadow .15s}button ha-icon{--mdc-icon-size:18px;pointer-events:none}button:hover:not(:disabled){background:color-mix(in srgb,var(--accent) 11%,var(--surface));filter:none}button.primary{box-shadow:none;color:var(--primary-background-color,#111218)}button.primary:hover:not(:disabled){background:color-mix(in srgb,var(--accent) 88%,white)}button.secondary{background:color-mix(in srgb,var(--primary-text-color) 4%,var(--surface));border-color:var(--line)}
      .home-heading{margin-bottom:28px;gap:24px}.home-title{display:flex;align-items:center;gap:15px}.home-mark{display:flex;align-items:center;justify-content:center;flex-shrink:0;width:52px;height:52px;border-radius:17px;background:color-mix(in srgb,var(--accent) 14%,var(--surface));color:var(--accent)}.home-mark ha-icon{--mdc-icon-size:27px}.home-heading h1{font-size:30px;letter-spacing:-.8px;font-weight:700;margin-bottom:5px}.home-heading .eyebrow{font-size:10px;letter-spacing:.16em;margin-bottom:8px}.home-heading .intro{font-size:14px}.home-heading>.actions{flex-wrap:wrap;justify-content:flex-end;gap:8px}
      .dashboard-layout{display:grid;grid-template-columns:minmax(0,1fr);gap:28px}.home-speakers,.home-commands{min-width:0}.section-title{display:flex;align-items:center;gap:8px;margin-bottom:16px;min-height:48px}.section-title>div{flex:1;min-width:0}.section-title h2{font-size:19px;font-weight:600;letter-spacing:-.3px}.section-title p{font-size:12px;margin:5px 0 0}.section-title>button{flex-shrink:0}.section-title>.actions{gap:6px;flex:0 0 auto}
      .dashboard-layout .speaker-grid{display:grid;grid-template-columns:repeat(auto-fit,minmax(min(100%,310px),1fr));gap:16px;align-items:stretch;grid-auto-rows:1fr}.home-speakers .box{min-width:0;display:flex;flex-direction:column;padding:22px;border-radius:20px;background:linear-gradient(145deg,color-mix(in srgb,var(--accent) 6%,var(--surface)),var(--surface) 60%);border:1px solid var(--line);box-shadow:0 4px 16px #00000006}.home-speakers .box:nth-child(3n+2){--accent:light-dark(#237e73,#7ed5c4)}.home-speakers .box:nth-child(3n){--accent:light-dark(#a07022,#e6bd7b)}.speaker-heading{gap:12px;margin-bottom:18px}.speaker-heading ha-icon{display:flex;align-items:center;justify-content:center;flex-shrink:0;width:44px;height:44px;border-radius:14px;background:color-mix(in srgb,var(--accent) 14%,var(--surface));--mdc-icon-size:25px}.home-speakers .speaker-heading h2{font-size:21px;letter-spacing:-.4px;overflow-wrap:anywhere}.home-speakers .speaker-heading p{font-size:13px}.home-speakers [data-connection]{display:flex;flex-wrap:wrap;gap:6px 12px;align-items:center;min-height:40px}.home-speakers .connection-state{background:transparent;padding:0;font-size:12px}.home-speakers .speaker-facts{display:none}.home-speakers .delivery{margin:0;font-size:11px}.home-speakers .dot{width:6px;height:6px}.home-speakers .volume-control{margin-top:18px}.volume-label{font-size:12px;font-weight:500;margin-bottom:10px}.volume-label output{font-size:14px;font-weight:600;color:var(--accent)}
      .volume-control input[type=range]{appearance:none;-webkit-appearance:none;padding:0;height:40px;min-height:40px;border:0;border-radius:12px;background:linear-gradient(to right,color-mix(in srgb,var(--accent) 65%,var(--surface)) var(--volume),color-mix(in srgb,var(--primary-text-color) 8%,var(--surface)) var(--volume));cursor:pointer}.volume-control input[type=range]::-webkit-slider-thumb{-webkit-appearance:none;width:5px;height:22px;border-radius:4px;background:var(--primary-text-color);box-shadow:0 0 0 4px color-mix(in srgb,var(--surface) 20%,transparent)}.volume-control input[type=range]::-moz-range-thumb{width:5px;height:22px;border:0;border-radius:4px;background:var(--primary-text-color)}.volume-control input[type=range]:disabled{opacity:.4;cursor:default}.home-speakers .volume-control .actions{margin-top:12px;gap:6px;flex-wrap:wrap}.home-speakers .volume-control .actions button{min-height:38px;font-size:12px;padding:7px 12px}.home-speakers .media-note{font-size:11px;line-height:1.4;min-height:16px;margin:6px 0 0}.home-speakers .quick-actions{display:flex;gap:6px;margin-top:auto;padding-top:12px}.home-speakers .quick-actions button{flex:1;min-height:38px;padding:7px 8px;font-size:11px;background:color-mix(in srgb,var(--accent) 8%,var(--surface));white-space:nowrap}.home-speakers .quick-actions button ha-icon{display:none}
      .home-speakers .quick-actions button,.home-speakers .volume-label output,.home-speakers .volume-control .actions button:not(.secondary){color:color-mix(in srgb,var(--accent) 65%,var(--primary-text-color))}
      .home-commands{padding-top:24px;border-top:1px solid var(--line)}.home-commands .list{grid-template-columns:repeat(auto-fill,minmax(min(100%,360px),1fr));gap:16px}.home-commands .command{padding:20px;border-radius:18px;min-width:0}.home-commands .command-top{display:block}.home-commands .command-top>.actions{margin-top:14px;gap:6px}.home-commands .command-top>.actions button{min-height:36px;padding:6px 11px;font-size:12px}.home-commands .phrase{font-size:18px;line-height:1.4;letter-spacing:-.2px;margin-bottom:7px}.home-commands .target{font-size:12px}.home-commands .chips{margin:12px 0 0}.home-commands .chip{border:0;background:color-mix(in srgb,var(--accent) 9%,var(--surface));font-size:11px}.home-commands .meta{font-size:11px;gap:12px;margin-top:14px;padding-top:12px}.home-commands details{font-size:11px}.home-commands .help.section{font-size:12px;margin-top:14px}.home-commands .empty{grid-column:1/-1;padding:28px}.home-journal{margin-top:26px;border:1px solid var(--line);border-radius:16px;background:var(--surface);padding:0 20px}.home-journal>summary{padding:18px 0;font-size:13px;font-weight:500;color:var(--primary-text-color)}.home-journal>.box{border:0;padding:0 0 20px}
      .dialog{border-radius:22px;box-shadow:0 24px 100px #0006}.utility-grid button{min-height:62px;border-radius:13px;background:color-mix(in srgb,var(--accent) 5%,var(--surface));border-color:var(--line)}.overlay{backdrop-filter:blur(5px)}.notice{border-color:color-mix(in srgb,var(--accent) 24%,var(--surface))}.empty{background:color-mix(in srgb,var(--accent) 3%,var(--surface))}
      @media(min-width:1200px){.dashboard-layout .speaker-grid:has(>.box:nth-child(2):last-child){grid-template-columns:repeat(2,minmax(0,1fr))}}
      @media(max-width:760px){main{padding:20px 16px 32px}.home-heading{align-items:flex-start;flex-direction:column;gap:18px;margin-bottom:26px;display:flex}.home-heading>.actions{justify-content:flex-start}.home-heading h1{font-size:26px}.home-heading .eyebrow{font-size:9px}.home-heading .intro{font-size:12px}.home-mark{width:44px;height:44px;border-radius:14px}.home-title{gap:12px}.section-title{flex-wrap:wrap}.section-title>div{flex-basis:50%}.section-title h2{font-size:18px}.home-commands .section-title>div:first-child{flex-basis:100%}.section-actions{margin-top:4px}.section-title>button{font-size:12px;padding:8px 10px}.home-speakers .box{padding:20px}.home-commands{padding-top:20px}.dashboard-layout{gap:24px}.dialog{max-height:94dvh;border-radius:22px 22px 0 0}.overlay{align-items:flex-end}.utility-grid{gap:8px}.utility-grid button{font-size:13px}.home-journal{padding:0 16px}.speaker-form{grid-template-columns:1fr}.span{grid-column:auto}}
      @media(prefers-reduced-motion:reduce){button{transition:none}.overlay{backdrop-filter:none}}
      </style><main>
      ${this._tab==='home'?this._home(rows,disabled):`      <header class="heading"><div><div class="eyebrow">Алиса × Home Assistant</div><h1>${this._tab==='speakers'?'Колонки Алисы':this._tab==='diagnostics'?'Диагностика':'Голосовые команды'}</h1><p class="intro">${this._tab==='speakers'?'Звук, управление и состояние ваших станций.':this._tab==='diagnostics'?'Проверка команд и последние события.':'Ваша фраза — нужное действие в доме.'}</p></div>${this._tab==='commands'?`<button class="primary" data-action="new">＋ Добавить</button>`:this._tab==='diagnostics'?`<button class="primary" data-action="preview">Проверить фразу</button>`:`<button class="primary" data-action="add-speaker" ${this._managerError?'disabled':''}>＋ Добавить колонку</button>`}</header>
      ${!this._config.view?`<nav class="tabs" role="tablist" aria-label="Алиса"><button role="tab" aria-selected="${this._tab==='commands'}" data-action="commands">Команды</button><button role="tab" aria-selected="${this._tab==='speakers'}" data-action="speakers">Колонки</button><button role="tab" aria-selected="${this._tab==='diagnostics'}" data-action="diagnostics">Диагностика</button></nav>`:''}
      ${this._message&&!this._dialog&&!this._utility?`<div class="notice" role="status">${escape(this._message)} ${this._undo?'<button data-action="undo">Отменить</button>':''}</div>`:''}
      ${this._tab==='commands'?`<div class="toolbar"><span class="count">Команд: ${rows.length} · Вариантов фраз: ${rows.reduce((sum,item)=>sum+new Set(aliases(item).map(normalize).filter(Boolean)).size,0)}</span><div class="actions"><button data-action="preview">Проверить фразу</button><button data-action="refresh" ${disabled}>Обновить</button></div></div><div class="list">${rows.length?rows.map(item=>this._command(item,disabled)).join(''):`<div class="empty"><h2>${this._loaded?'Первая команда — ваша':'Загрузка команд…'}</h2><p>Например: «Я ухожу» → выключить свет и запустить сценарий ухода.</p>${this._loaded?'<button class="primary" data-action="new">Добавить команду</button>':''}</div>`}</div><p class="help section">Одна команда может иметь несколько вариантов фразы. Произносите «Алиса» перед любым из них.</p>`:this._tab==='diagnostics'?this._diagnostics():this._speakerPanel(disabled)}
`}
      ${this._dialog?this._editor(disabled):''}${this._speakerDialog?this._speakerEditor(disabled):''}${this._utility?this._utilityDialog():''}</main>`;
      this._bindNativePicker();this._bindUtilityPickers();
      if(this._dialog)queueMicrotask(()=>this.shadowRoot.querySelector('#phrases')?.focus());
    }
    _home(rows,disabled){return `<header class="heading home-heading"><div class="home-title"><span class="home-mark"><ha-icon icon="mdi:waveform"></ha-icon></span><div><div class="eyebrow">Алиса × Home Assistant</div><h1>${this._speakers.some(s=>this._needsSetup(s))?'Подключим ваши колонки':'Ваш дом на связи'}</h1><p class="intro">Колонки, голосовые команды и всё, что им нужно.</p></div></div><div class="actions"><button class="secondary" data-action="tts"><ha-icon icon="mdi:bullhorn-outline"></ha-icon>Объявление</button><button class="primary" data-action="new">＋ Команда</button></div></header>${this._message?`<div class="notice" role="status">${escape(this._message)} ${this._undo?'<button data-action="undo">Отменить</button>':''}</div>`:''}<div id="setup-notice-region">${this._setupNotice()}</div><div class="dashboard-layout"><section class="home-speakers" aria-label="Колонки"><div class="section-title"><div><h2>Колонки</h2><p id="speaker-summary">${this._speakerSummary()}</p></div><button data-action="add-speaker" ${this._managerError?'disabled':''}>＋ Подключить</button><button data-action="refresh" aria-label="Обновить колонки" ${disabled}><ha-icon icon="mdi:refresh"></ha-icon></button></div>${this._speakerPanel(disabled)}</section><section class="home-commands" aria-label="Голосовые команды"><div class="section-title"><div><h2>Голосовые команды</h2><p>Команд: ${rows.length} · Вариантов фраз: ${rows.reduce((n,item)=>n+new Set(aliases(item).map(normalize).filter(Boolean)).size,0)}</p></div><div class="actions section-actions"><button class="secondary" data-action="new">＋ Добавить</button><button data-action="preview" title="Проверить фразу без выполнения"><ha-icon icon="mdi:text-search"></ha-icon>Проверить</button></div></div><div class="list">${rows.length?rows.map(item=>this._command(item,disabled)).join(''):`<div class="empty"><h2>${this._loaded?'Начните со своей фразы':'Загрузка команд…'}</h2><p>«Я ухожу» или «Спокойной ночи» — выберите действие для вашего дома.</p><button class="primary" data-action="new">Добавить команду</button></div>`}</div><p class="help section">Скажите «Алиса» и любую из ваших фраз.<br>Одна команда — несколько привычных вариантов.</p></section></div><details id="home-journal" class="home-journal" ${this._journalOpen?'open':''}><summary>Журнал и диагностика</summary>${this._diagnostics()}</details>`;}
    _command(item,disabled){
      const phrases=[...new Map(aliases(item).map(p=>[normalize(p),p])).values()],state=this._hass?.states[item.entity_id]?.state;
      const ready=!!state&&state!=='unavailable'&&state!=='unknown'&&(!item.entity_id.startsWith('automation.')||state==='on');
      const conflict=phrases.some(p=>this._registry.some(other=>other.entity_id!==item.entity_id&&this._selected(other)&&aliases(other).some(q=>normalize(q)===normalize(p))));
      return `<article class="command" aria-label="${escape(this._name(item))}"><div class="command-top"><div><h2 class="phrase">${escape(phrases[0]??'Добавьте фразу')}</h2>${normalize(this._name(item))!==normalize(phrases[0]??'')?`<div class="target">Действие: ${escape(this._name(item))}</div>`:''}${phrases.length>1?`<div class="chips">${phrases.slice(1).map(p=>`<span class="chip">${escape(p)}</span>`).join('')}</div>`:''}</div><div class="actions"><button class="secondary" data-action="edit" data-id="${escape(item.entity_id)}" ${disabled}>Изменить</button><button data-action="disable" data-id="${escape(item.entity_id)}" ${disabled}>Отключить</button></div></div><div class="meta"><span class="status ${ready?'ready':''}"><span class="dot"></span>${ready?'Готова к запуску':'Действие недоступно'}</span><span>◉ ${escape(this._audience(item.entity_id))}</span></div>${conflict?'<p class="error help">Фраза совпадает с другой командой. Измените её для однозначного выбора.</p>':''}<details><summary>Детали действия</summary><p>${escape(this._name(item))}</p><code>${escape(item.entity_id)}</code></details></article>`;
    }
    _editor(disabled){return `<div class="overlay"><section class="dialog" role="dialog" aria-modal="true" aria-labelledby="editor-title"><div class="dialog-header"><h2 id="editor-title">${this._editing?'Изменить команду':'Новая команда'}</h2><button data-action="close" aria-label="Закрыть" ${disabled}>✕</button></div><form id="command-form">${this._message?`<div class="notice" role="status">${escape(this._message)}</div>`:''}<div id="editor-service-status"></div><div class="fields"><label>Что сказать Алисе<textarea id="phrases" required maxlength="12000" placeholder="Не включай свет на кухне" ${disabled}>${escape(this._draftPhrases)}</textarea><span class="help">Несколько вариантов? Каждый с новой строки.</span></label><div><ha-entity-picker id="target"></ha-entity-picker><p class="help">Сохранение команды не запускает действие.</p></div><div id="audience">${this._audienceFields()}</div></div><div class="actions"><button type="button" data-action="close" ${disabled}>Отмена</button><button id="save-command" class="primary" type="submit" disabled>${this._busy?'Сохранение…':'Сохранить команду'}</button></div></form></section></div>`;}
    _needsSetup(s){return !this._managerError&&(['awaiting_reboot','awaiting_acceptance'].includes(s.install?.state)||(s.connection?.status!=='online'&&!s.connection?.last_seen&&s.install?.state!=='ready'));}
    _setupNotice(){const pending=this._speakers.filter(s=>this._needsSetup(s));return pending.length?`<aside class="setup-notice" role="alert"><ha-icon icon="mdi:connection"></ha-icon><div><strong>Осталось настроить мост</strong><p>${this._installationReady===false?'Сначала укажите локальный адрес в настройках приложения. Затем мастер поможет подключить колонки.':'Колонки добавлены. Завершите подключение, чтобы использовать голосовые команды.'}</p></div><button class="primary" data-action="setup" data-id="${escape(pending[0].id)}">Продолжить настройку</button></aside>`:'';}
    _speakerSummary(){const online=this._speakers.filter(s=>!this._managerError&&s.connection?.status==='online').length;return this._speakers.length+' '+(this._speakers.length===1?'колонка':this._speakers.length<5?'колонки':'колонок')+' · '+online+' на связи'+(this._speakers.some(s=>this._needsSetup(s))?' · есть незавершённые подключения':'');}
    _connectionDetails(s){
      const c=s.connection??{};const status=this._managerError?'unknown':c.status??'unknown';
      if(this._needsSetup(s))return `<div class="connection-state setup"><span class="dot"></span>${s.install?.state==='awaiting_acceptance'?'Проверьте голос':s.install?.state==='awaiting_reboot'?'Нужна перезагрузка':'Мост не настроен'}</div><button class="setup-link" data-action="setup" data-id="${escape(s.id)}">Настроить мост →</button>`;
      const labels={online:'На связи',offline:'Нет связи',unknown:'Статус уточняется'};
      const assigned=this._registry.filter(item=>this._selected(item)&&aliases(item).length&&(!Object.hasOwn(this._assignments,item.entity_id)||this._assignments[item.entity_id].includes(s.id))).length;
      return `<div class="connection-state ${status}"><span class="dot"></span>${labels[status]??labels.unknown}</div><div class="speaker-facts"><div><span>Мост</span><strong>${status==='online'?'Работает':status==='offline'?'Недоступен':'Ожидание проверки'}</strong></div><div><span>Назначено команд</span><strong>${this._managerError?'—':assigned}</strong></div><div><span>Загружено вариантов фраз</span><strong>${status==='online'?escape(c.voice_phrase_count??'—'):'—'}</strong></div><div><span>Последняя связь</span><strong>${c.last_seen?escape(new Date(c.last_seen*1000).toLocaleTimeString('ru-RU')):'Ещё не было'}</strong></div></div>${c.error&&status==='offline'?`<p class="help">${escape(c.error)}</p>`:''}`;
    }
    _delivery(s){if(this._needsSetup(s))return '';const delivery=s.connection?.delivery??{},status=this._managerError?'unknown':delivery.status??'unknown';const labels={synced:'Команды синхронизированы',pending:'Команды обновляются',error:'Ошибка синхронизации',unknown:'Синхронизация не проверена'};return `<div class="delivery ${escape(status)}" title="${escape(delivery.message??'')}"><span class="dot"></span>${labels[status]??labels.unknown}</div>`;}
    _media(s){const state=this._hass?.states[s.media_entity];const available=!!state&&!['unavailable','unknown','off'].includes(state.state);const features=Number(state?.attributes.supported_features??0);return {state,available,features,can:bit=>available&&(features&bit)===bit,yandex:this._mediaPlatforms[s.media_entity]==='yandex_station'};}
    _mediaControls(s){const media=this._media(s),busy=this._stationBusy.has(s.id);const volume=media.state?.attributes.volume_level;const known=typeof volume==='number'&&Number.isFinite(volume);const value=known?Math.round(volume*100):0;const action=media.state?.state==='playing'&&media.can(1)?'media_pause':media.state?.state==='paused'&&media.can(16384)?'media_play':media.can(4096)?'media_stop':media.can(1)?'media_pause':media.can(16384)?'media_play':null;const labels={media_stop:'Стоп',media_pause:'Пауза',media_play:'Воспроизвести'};
      return `<div class="volume-control"><p class="media-source">Звук через Home Assistant</p><div class="volume-label"><span>Громкость</span><output data-volume-output="${escape(s.id)}">${known?value+' %':'—'}</output></div><input type="range" aria-label="Громкость ${escape(s.name)}" min="0" max="100" step="1" value="${value}" data-volume="${escape(s.id)}" style="--volume:${value}%" ${!media.can(4)||busy||!known?'disabled':''}><div class="actions"><button class="secondary" data-action="media" data-service="${action??''}" data-id="${escape(s.id)}" ${!action||busy?'disabled':''}>${action?labels[action]:'Медиа недоступно'}</button>${action!=='media_stop'&&media.can(4096)?`<button class="secondary" data-action="media" data-service="media_stop" data-id="${escape(s.id)}" ${busy?'disabled':''}>Стоп</button>`:''}<button data-action="control" data-id="${escape(s.id)}"><ha-icon icon="mdi:dots-horizontal"></ha-icon>Управление</button></div><p class="media-note" role="status" data-media-message="${escape(s.id)}">${escape(this._stationMessages[s.id]??(!s.media_entity?'Выберите медиаплеер в настройках.':!media.available?'Медиаплеер недоступен.':''))}</p></div>`;
    }
    _refreshMedia(){if(!this.shadowRoot)return;for(const speaker of this._speakers??[]){const region=this.shadowRoot.querySelector('[data-media="'+speaker.id+'"]');if(region&&this._volumeDragging!==speaker.id&&!this._stationBusy.has(speaker.id))region.innerHTML=this._mediaControls(speaker);}}
    async _mediaAction(id,service,data={}){const speaker=this._speakers.find(s=>s.id===id);if(!speaker||this._stationBusy.has(id))return;const media=this._media(speaker);const bits={volume_set:4,media_stop:4096,media_pause:1,media_play:16384,play_media:512};if(!bits[service]||!media.can(bits[service])||(service==='play_media'&&!media.yandex)){this._stationMessages[id]='Это действие недоступно для выбранного медиаплеера.';this._refreshMedia();return false;}
      this._stationBusy.add(id);const utility=this._utility;const relatedButtons=[...this.shadowRoot.querySelectorAll('.utility-dialog button[data-id="'+id+'"]')].filter(el=>['tts','test-sound','media'].includes(el.dataset.action)).map(el=>({el,disabled:el.disabled}));for(const item of relatedButtons)item.el.disabled=true;if(utility?.type==='control'&&utility.speakerId===id){utility.message='Отправка запроса…';this._utilityMessage();}for(const control of this.shadowRoot.querySelectorAll('[data-media="'+id+'"] input,[data-media="'+id+'"] button[data-service]'))control.disabled=true;try{await this._hass.callService('media_player',service,data,{entity_id:speaker.media_entity});this._stationMessages[id]=service==='volume_set'?'Запрос громкости отправлен. Показано последнее состояние HA.':'Запрос отправлен в Home Assistant.';return true;}catch(error){this._stationMessages[id]='Не удалось отправить запрос. Повторите ещё раз.';return false;}finally{this._stationBusy.delete(id);for(const item of relatedButtons)if(item.el.isConnected)item.el.disabled=item.disabled;if(this._utility===utility&&utility?.type==='control'&&utility.speakerId===id){utility.message=this._stationMessages[id];this._utilityMessage();}this._refreshMedia();}}
    _speakerPanel(disabled){return `${this._managerError?'<div class="notice" role="status">Нет связи с сервисом колонок. Последние данные могут быть неактуальны.<button data-action="refresh">Повторить подключение</button></div>':''}${this._tab==='home'?'':`<div class="toolbar"><span id="speaker-summary" class="count">${this._speakerSummary()}</span><button data-action="refresh" ${disabled}>Обновить</button></div>`}<div class="speaker-grid">${this._speakers.map(s=>`<article class="box"><div class="speaker-heading"><ha-icon icon="mdi:speaker-wireless"></ha-icon><div><h2>${escape(s.name)}</h2><p>${escape(s.area||'Комната не указана')}</p></div></div><div data-connection="${escape(s.id)}">${this._connectionDetails(s)}${this._delivery(s)}</div><div data-media="${escape(s.id)}">${this._mediaControls(s)}</div>${this._tab==='home'?`<div class="quick-actions"><button data-action="tts" data-id="${escape(s.id)}" ${this._media(s).can(512)&&this._media(s).yandex?'':'disabled'}><ha-icon icon="mdi:message-text-outline"></ha-icon>Сказать</button><button data-action="test-sound" data-id="${escape(s.id)}" ${this._media(s).can(512)&&this._media(s).yandex?'':'disabled'}><ha-icon icon="mdi:volume-high"></ha-icon>Проверить звук</button><button data-action="settings" data-id="${escape(s.id)}" ${this._managerError||this._stationBusy.has(s.id)?'disabled':''}><ha-icon icon="mdi:cog-outline"></ha-icon>Настройки</button></div>`:''}</article>`).join('')}</div>${!this._speakers.length&&!this._managerError?'<div class="empty"><h2>Подключите первую колонку</h2><p>Добавьте станцию, чтобы видеть её состояние и назначать голосовые команды.</p></div>':''}`;}
    _diagnostics(){return `<section class="box"><div class="toolbar"><div><h2>Последние команды</h2><p class="help">События обработки; проверка фразы не запускает действия.</p></div><button data-action="activity-refresh">Обновить журнал</button></div><div id="activity-region">${this._activityMarkup()}</div></section>`;}
    _statusLabel(status){return ({handled:'Обработано',forwarded:'Передано Алисе',answered:'Ответ дан',accepted:'Запрос принят',confirmed:'Состояние подтверждено',already:'Уже в нужном состоянии',clarification:'Нужно уточнение',unavailable:'Недоступно',error:'Ошибка',cancelled:'Отменено',success:'Выполнено',completed:'Выполнено',failed:'Ошибка',pending:'В обработке'})[status]??'Статус не указан';}
    _routeLabel(route){return ({home:'Управление домом',yandex:'Штатная Алиса',llm:'Нейросеть',playback:'Воспроизведение'})[route]??'Маршрут не определён';}
    _sourceLabel(source){return ({voice_phrase:'Ваша голосовая команда',rule:'Правило обработки',classifier:'Выбор маршрута',classifier_error:'Ошибка выбора маршрута',default:'Маршрут по умолчанию',dialogue:'Продолжение диалога',dialogue_retry:'Повтор диалога',dialogue_reset:'Новый диалог',home_clarification:'Уточнение команды',playback:'Воспроизведение',none:'Совпадений нет'})[source]??'Способ обработки не указан';}
    _activityMarkup(speakerId){const all=(this._events??[]).filter(e=>!speakerId||e.speaker_id===speakerId).slice(0,100);this._activityPage=Math.max(0,Math.min(this._activityPage,Math.max(0,Math.ceil(all.length/20)-1)));const start=this._activityPage*20,events=all.slice(start,start+20);return `${this._activityError?'<p class="help error" role="status">Не удалось загрузить журнал. Попробуйте обновить.</p>':''}${events.length?`<div class="activity-list">${events.map(e=>`<article class="activity-item"><strong>${escape(e.text||'Команда без текста')}</strong><p>${escape(this._speakers.find(s=>s.id===e.speaker_id)?.name??e.speaker_id??'Колонка не указана')} · ${escape(this._eventTime(e.at))}</p><p>${escape(this._statusLabel(e.status))} · ${escape(this._routeLabel(e.route))}${e.source?' · '+escape(this._sourceLabel(e.source)):''}${Number.isFinite(e.duration_ms)?' · '+e.duration_ms+' мс':''}</p>${e.error?`<p class="error">${escape(e.error)}</p>`:''}${e.calls?.length?`<p>${e.calls.map(c=>escape(c.label??c.target??c.name??'Действие')).join(' · ')}</p>`:''}</article>`).join('')}</div><div class="toolbar section"><span class="help">${start+1}–${start+events.length} из ${all.length}</span><div class="actions"><button type="button" data-action="activity-prev" ${start===0?'disabled':''}>Новее</button><button type="button" data-action="activity-next" ${start+20>=all.length?'disabled':''}>Раньше</button></div></div>`:'<p class="help section">Событий пока нет.</p>'}`;}
    _eventTime(at){if(!at)return 'Время не указано';const date=new Date(typeof at==='number'&&at<1e12?at*1000:at);return Number.isNaN(date.getTime())?'Время не указано':date.toLocaleString('ru-RU');}
    async _loadActivity(){try{const data=await this._api('/api/activity');this._events=(data.events??[]).slice(0,100);this._activityError='';}catch(error){this._activityError='Не удалось загрузить журнал';}const region=this.shadowRoot.getElementById('activity-region');if(region)region.innerHTML=this._activityMarkup(this._utility?.speakerId);}
    _openUtility(type,id){const section=type==='setup'?'bridge':'general';if(type==='setup')type='settings';const s=this._speakers.find(item=>item.id===id);const area=Object.values(this._hass.areas??{}).find(item=>item.name===s?.area);this._utility={type,section,speakerId:id,speakers:id?[id]:this._speakers.map(item=>item.id),text:'',name:s?.name??'',area_id:area?.area_id??'',media_entity:s?.media_entity??'',message:'',results:[]};this._render();if(type==='activity')this._loadActivity();}
    async _copyPublicKey(){let copied=false;try{if(navigator.clipboard?.writeText){await navigator.clipboard.writeText(this._key);copied=true;}}catch{}if(!copied){const field=document.createElement('textarea'),focus=this.shadowRoot.activeElement;field.value=this._key??'';field.readOnly=true;field.style.cssText='position:fixed;left:-10000px;top:0';document.body.append(field);field.select();try{copied=!!document.execCommand('copy');}catch{}finally{field.remove();focus?.focus?.();}}if(this._utility){this._utility.message=copied?'Ключ скопирован. Добавьте его на колонку по инструкции.':'Не удалось скопировать автоматически. Выделите и скопируйте ключ ниже.';this._utilityMessage();}}
    _suggestedAppUrl(){let host=location.hostname;const internal=this._hass?.config?.internal_url;if(internal){try{host=new URL(internal).hostname;}catch{}}return /^(10\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.)/.test(host)&&this._appPort?'http://'+host+':'+this._appPort:'';}
    _validatedAppUrl(value){
      const match=/^http:\/\/((?:\d{1,3}\.){3}\d{1,3}):(\d{1,5})\/?$/.exec(String(value??'').trim());
      if(!match)throw Error('Введите полный адрес: http://локальный_IP_HA:порт. Например, http://192.168.10.10:'+(this._appPort??18080)+'.');
      const parts=match[1].split('.'),ip=parts.map(Number),port=Number(match[2]);
      if(parts.some(p=>! /^(0|[1-9]\d{0,2})$/.test(p))||ip.some(n=>n>255)||!(ip[0]===10||ip[0]===192&&ip[1]===168||ip[0]===172&&ip[1]>=16&&ip[1]<=31)||port<1||port>65535)throw Error('Нужен локальный IPv4-адрес HA и порт от 1 до 65535.');
      return 'http://'+match[1]+':'+port;
    }
    async _waitAppSetup(value){for(let n=0;n<40;n++){try{const data=await this._api('/api/setup');if(data.installation_ready&&data.advertised_url===value)return data;}catch{}await new Promise(resolve=>setTimeout(resolve,1000));}throw Error('Адрес записан, но запуск приложения ещё не подтверждён. Проверьте журнал приложения и обновите страницу.');}
    async _saveAppUrl(){
      const u=this._utility;if(!u||u.busy)return;
      if(!this._config.manager_app||!this._hass.user?.is_admin){u.message='Сохранить адрес может администратор приложения Home Assistant.';this._utilityMessage();return;}
      let value;try{value=this._validatedAppUrl(u.advertised_url??this.shadowRoot.getElementById('settings-url')?.value??this._advertisedUrl??this._suggestedAppUrl());}catch(error){u.message=error.message;u.urlError=true;this._utilityMessage();this.shadowRoot.getElementById('settings-url')?.focus();return;}
      u.advertised_url=value;u.urlError=false;u.busy=true;u.message='Сохраняем адрес в настройках приложения…';this._utilityMessage();
      try{
        const endpoint='/addons/'+this._config.manager_app;
        const info=await this._hass.callWS({type:'supervisor/api',endpoint:endpoint+'/info',method:'GET'});
        await this._hass.callWS({type:'supervisor/api',endpoint:endpoint+'/options',method:'POST',data:{options:{...info.options,advertised_url:value}}});
        const verified=await this._hass.callWS({type:'supervisor/api',endpoint:endpoint+'/info',method:'GET'});
        if(verified.options?.advertised_url!==value)throw Error('Home Assistant не подтвердил сохранение адреса.');
        try{sessionStorage.setItem(this._addressResumeKey(),JSON.stringify({speakerId:u.speakerId,url:value,at:Date.now()}));}catch{}
        u.message='Адрес сохранён. Перезапускаем приложение…';this._utilityMessage();
        try{await this._hass.callWS({type:'supervisor/api',endpoint:endpoint+'/restart',method:'POST',data:{}});}catch{} // Verify actual readiness before reporting success; never replay restart.
        const ready=await this._waitAppSetup(value);this._installationReady=true;this._advertisedUrl=ready.advertised_url;
        await this._loadManager();u.setupStep=undefined;u.message='Адрес сохранён. Продолжим подключение колонки.';
      }catch(error){u.message=error.message??'Не удалось сохранить адрес. Попробуйте ещё раз.';}
      finally{u.busy=false;if(this._utility===u){this._render();}}
    }
    _bridgeStage(s){if(['awaiting_reboot','awaiting_acceptance'].includes(s.install?.state))return 4;if(s.connection?.status==='online'||s.install?.state==='ready')return 5;if(this._installationReady===false)return 0;if(!s.trusted_fingerprint||!s.last_check?.root)return 1;if(s.connection?.status==='online'||s.install?.state==='ready')return 5;return 3;}
    _bridgeSettings(s,u){
      const locked=!!this._stationBusy.has(s.id)||!!this._managerError,stage=this._bridgeStage(s),step=u.setupStep??stage;
      const btn=(action,label,blocked=false)=>`<button type="button" class="secondary" data-action="${action}" data-id="${escape(s.id)}" ${locked||blocked?'disabled':''}>${label}</button>`;
      const wiki=this._ingress?.url?`<a class="bridge-guide" href="${escape(this._ingress.url+'/wiki/Bridge')}" target="_parent">Подробная инструкция ↗</a>`:'';
      const key=`<div class="key"><code>${escape(this._key??'Ключ загружается…')}</code></div>${btn('copy-key','Скопировать ключ')}`;
      const phase=[['Укажите адрес приложения','Один раз для всех колонок.'],['Разрешите доступ к колонке','Новый экземпляр приложения использует свой ключ.'],['Проверьте подключение','Проверим, что приложение может войти на колонку.'],['Подключите мост','Свяжем голосовые команды с этим приложением.'],['Проверьте голос','Последний шаг выполняется на слух.']][Math.min(step,4)];
      let body='';
      if(step===0){const value=u.advertised_url??(this._advertisedUrl||this._suggestedAppUrl());body=`<p>Введите адрес, по которому колонка будет обращаться к Home Assistant в домашней сети.</p><label class="app-url-label">Адрес приложения<input id="settings-url" data-utility-field="advertised_url" type="url" inputmode="url" autocomplete="url" spellcheck="false" placeholder="http://192.168.10.10:${escape(this._appPort??18080)}" value="${escape(value)}" aria-invalid="${!!u.urlError}" ${u.busy?'disabled':''}></label><p class="help">Формат: <code>http://IP_ВАШЕГО_HA:ПОРТ</code>.${this._appPort?' Внешний порт этого приложения: '+escape(this._appPort)+'.':''} Используйте локальный IP, а не внешний домен.</p><button type="button" class="primary" data-action="save-app-url" ${u.busy||!this._config.manager_app?'disabled':''}>${u.busy?'Сохраняем и перезапускаем…':'Сохранить и продолжить'}</button><p class="help">Адрес сохранится в настройках приложения. Оно перезапустится автоматически, колонки и ключи останутся на месте.</p>`;}
      if(step===1)body=`<p>У колонки уже должен быть настроен доступ по SSH с правами root. Добавьте публичный ключ приложения в список разрешённых ключей на колонке. Прежние ключи оставьте.</p>${key}<details><summary>Как добавить ключ?</summary><p>Откройте доверенное SSH-подключение к колонке. Добавьте ключ отдельной строкой в постоянный файл authorized_keys вашей прошивки. Путь зависит от модели; пошаговая инструкция доступна ниже.</p>${wiki}</details><h3 class="bridge-subtitle">Убедитесь, что это ваша колонка</h3><p>Нажмите кнопку и сравните отпечаток с доверенным SSH-подключением. Подтверждение отпечатка не добавляет ключ автоматически.</p>${s.trusted_fingerprint?'<p class="help">Отпечаток уже подтверждён. Если публичный ключ добавлен, переходите к проверке.</p>'+btn('key-added','Ключ добавлен — проверить доступ'):btn('fingerprint','Показать SSH-отпечаток')}${this._fingerprintData?.id===s.id?`<div class="key"><code>${escape(this._fingerprintData.fingerprint)}</code></div>${btn('trust','Отпечаток совпадает — доверять')}`:''}`;
      if(step===2)body=`<p>После добавления ключа нажмите «Проверить подключение». Проверка ничего не устанавливает и не перезагружает колонку.</p>${btn('check','Проверить подключение',!s.trusted_fingerprint)}<details><summary>Проверка не проходит?</summary><p>Проверьте IP и SSH-порт: <code>${escape(s.host)}:${escape(s.port??2222)}</code>. Убедитесь, что на колонке добавлен ключ именно этого приложения.</p>${key}${wiki}</details>`;
      if(step===3)body=`<p>Теперь можно установить мост. Если он уже установлен, приложение обновит его и подключит к себе. Перед изменениями сохраняется резервная копия.</p>${btn('install','Установить / обновить мост',this._installationReady===false||!s.trusted_fingerprint||!s.last_check?.root)}<p class="help">Дождитесь завершения операции. Перезагрузка потребуется только тогда, когда приложение предложит её явно.</p>`;
      if(step===4)body=s.install?.state==='awaiting_reboot'?`<p>Мост установлен. Перезагрузите колонку, затем вернитесь сюда для голосовой проверки.</p>${btn('reboot','Перезагрузить колонку')}`:`<p>Скажите Алисе одну из настроенных команд. Проверьте ответ, микрофон и обычную работу Алисы. Только после проверки на слух подтвердите результат.</p>${btn('confirm','Голосовая проверка пройдена',s.install?.state!=='awaiting_acceptance')}${s.install?.state==='awaiting_acceptance'?btn('rollback','Откатить установку'):''}`;
      const maintenance=`<details class="bridge-maintenance"><summary>Обслуживание моста</summary><p class="help">Для диагностики уже подключённой колонки.</p><div class="utility-grid">${btn('check','Проверить подключение',!s.trusted_fingerprint)}${btn('restart','Перезапустить мост',!s.trusted_fingerprint)}${btn('install','Обновить мост',this._installationReady===false||!s.trusted_fingerprint||['awaiting_reboot','awaiting_acceptance'].includes(s.install?.state))}${btn('activity','Последние команды')}</div><p class="help">Перезапуск моста не перезагружает колонку.</p><details><summary>SSH и подключение</summary><p><code>${escape(s.host)}:${escape(s.port??2222)}</code></p>${btn('fingerprint','Показать SSH-отпечаток')}<p class="help">Публичный ключ приложения:</p>${key}${this._fingerprintData?.id===s.id?`<div class="key"><code>${escape(this._fingerprintData.fingerprint)}</code></div>${btn('trust','Отпечаток совпадает — доверять')}`:''}</details></details>`;
      if(step===5)return `<div class="bridge-ready"><ha-icon icon="mdi:check-circle-outline"></ha-icon><div><h3>${s.connection?.status==='online'?'Мост подключён':'Мост настроен, проверяем связь'}</h3><p>${s.connection?.status==='online'?'Колонка готова к голосовым командам.':'Если колонка не отвечает, откройте обслуживание ниже.'}</p></div></div>${maintenance}`;
      const titles=['Адрес','Доступ','Проверка','Мост','Голос'];
      return `<div class="bridge-intro"><strong>Подключение: ${escape(s.name)}</strong><p>Звук через HA и голосовые команды настраиваются отдельно. Здесь подключается обработчик голосовых команд — мост.</p></div><ol class="bridge-progress" aria-label="Этапы подключения">${titles.map((t,i)=>`<li class="${i===step?'active':i<stage?'complete':''}" ${i===step?'aria-current="step"':''}><span>${i<stage?'✓':i+1}</span>${t}</li>`).join('')}</ol><section class="bridge-current"><p class="bridge-kicker">ШАГ ${step+1} ИЗ 5</p><h3>${phase[0]}</h3><p class="help">${phase[1]}</p>${body}</section><div class="bridge-navigation">${step>0?btn('setup-back','← Назад'):''}${step>0&&step<4?btn('setup-next','Дальше →',step>=stage):''}</div>${wiki}`;
    }
    _utilityDialog(){const u=this._utility,s=this._speakers.find(item=>item.id===u.speakerId),blocked=u.busy?'disabled':'';const titles={control:s?.name??'Управление колонкой',tts:'Произнести текст',settings:'Настройки колонки',preview:'Проверить фразу',activity:'Последние команды',setup:'Подключение моста'};let content='';
      if(u.type==='control'){const media=s?this._media(s):null;content=`<p>${escape(s?.area??'')}</p><div class="utility-grid"><button type="button" data-action="tts" data-id="${escape(s?.id)}" ${media?.can(512)&&media.yandex?'':'disabled'}>Произнести текст</button><button type="button" data-action="test-sound" data-id="${escape(s?.id)}" ${media?.can(512)&&media.yandex?'':'disabled'}>Проверить звук</button><button type="button" data-action="settings" data-id="${escape(s?.id)}">Настройки</button><button type="button" data-action="activity" data-id="${escape(s?.id)}">Последние команды</button></div>`;}
      if(u.type==='tts'||u.type==='preview'){content=`<label>${u.type==='tts'?'Текст для произнесения':'Что вы скажете Алисе'}<textarea data-utility-field="text" required maxlength="4000" placeholder="${u.type==='tts'?'Например: ужин готов':'Не включай свет на кухне'}">${escape(u.text)}</textarea></label><fieldset style="border:0;padding:0;margin:20px 0"><legend style="font-size:14px;margin-bottom:12px">${u.type==='tts'?'Где произнести':'Для каких колонок проверить'}</legend>${this._speakers.map(item=>{const media=this._media(item);const unavailable=u.type==='tts'&&(!media.can(512)||!media.yandex);return `<label class="check" style="margin-top:10px"><input type="checkbox" name="tts-speaker" value="${escape(item.id)}" ${u.speakers.includes(item.id)&&!unavailable?'checked':''} ${unavailable?'disabled':''}>${escape(item.name)} · ${escape(item.area)}${unavailable?' — озвучивание недоступно':''}</label>`;}).join('')}</fieldset><p class="help">${u.type==='tts'?'Запрос отправляется через Home Assistant. Фактическое звучание подтвердите на слух.':'Показывает маршрут и действие без запуска автоматизаций.'}</p>${u.results?.length?u.results.map(r=>`<section class="result"><h3>${escape(this._speakers.find(item=>item.id===r.speaker_id)?.name??r.speaker_id)}</h3><p>${r.matched?(r.allowed===false?'Команда найдена, но не назначена этой колонке':'Команда найдена'):'Совпадение не найдено'}</p>${r.calls?.length?`<p>${r.calls.map(c=>escape(c.label??c.target??c.name)).join(' · ')}</p>`:''}<p>${escape(r.answer??r.reason??'')}</p><p class="help">Маршрут: ${escape(this._routeLabel(r.route))}${r.source?' · '+escape(this._sourceLabel(r.source)):''}</p></section>`).join(''):''}`;}
      if(u.type==='settings'&&u.section!=='bridge')content=`<div class="fields"><label>Название<input data-utility-field="name" required maxlength="80" value="${escape(u.name)}"></label><ha-form id="settings-area"></ha-form><ha-entity-picker id="settings-media"></ha-entity-picker><p class="help">Название и медиаплеер сохраняются без SSH. Смена комнаты применяется к мосту и требует проверенного SSH-подключения.</p></div>`;
      if(u.type==='activity')content=`<button type="button" data-action="activity-refresh">Обновить журнал</button><div id="activity-region">${this._activityMarkup(u.speakerId)}</div>`;
      if(u.type==='settings'&&s){if(u.section==='bridge')content=this._bridgeSettings(s,u);content=`<p class="settings-speaker">${escape(s.name)}</p><div class="settings-tabs" role="tablist" aria-label="Разделы настроек"><button type="button" role="tab" aria-selected="${u.section!=='bridge'}" data-action="settings-general">Колонка и звук</button><button type="button" role="tab" aria-selected="${u.section==='bridge'}" data-action="settings-bridge">Мост ${this._needsSetup(s)?'<span class="setup-tab-dot" aria-label="Требует настройки">●</span>':''}</button></div>${content}`;}
      return `<div class="overlay"><section class="dialog utility-dialog" role="dialog" aria-modal="true" aria-labelledby="utility-title"><div class="dialog-header"><h2 id="utility-title">${escape(titles[u.type])}</h2><button type="button" data-action="close-utility" aria-label="Закрыть" ${blocked}>✕</button></div><form id="utility-form"><div id="utility-message" role="status" class="media-note">${escape(u.message)}</div><div id="utility-content">${content}</div>${(['tts','preview'].includes(u.type)||u.type==='settings'&&u.section!=='bridge')?`<div class="actions"><button type="button" data-action="close-utility" ${blocked}>Отмена</button><button id="utility-submit" class="primary" type="submit" ${blocked||this._managerError&&u.type!=='tts'?'disabled':''}>${u.busy?'Выполнение…':({tts:'Произнести',settings:'Сохранить настройки',preview:'Проверить без выполнения'})[u.type]}</button></div>`:''}</form></section></div>`;
    }
    async _bindUtilityPickers(){if(this._utility?.type!=='settings'||this._utility.section==='bridge')return;try{await this._ensureNativePicker();if(!customElements.get('ha-form')){const helpers=await window.loadCardHelpers();await helpers.createCardElement({type:'entities',entities:[]}).constructor.getConfigElement();}const media=this.shadowRoot.getElementById('settings-media'),area=this.shadowRoot.getElementById('settings-area');if(media){media.hass=this._hass;media.label='Медиаплеер колонки';media.includeDomains=['media_player'];media.allowCustomEntity=false;media.value=this._utility.media_entity;}if(area){area.hass=this._hass;area.schema=[{name:'area',required:true,selector:{area:{}}}];area.data={area:this._utility.area_id};area.computeLabel=()=> 'Комната';}}catch(error){this._utility.message='Не удалось загрузить поля Home Assistant. Закройте настройки и попробуйте снова.';this._utilityMessage();}}
    _utilityMessage(){const u=this._utility;if(!u)return;const region=this.shadowRoot.getElementById('utility-message');if(region)region.textContent=u.message??'';for(const close of this.shadowRoot.querySelectorAll('[data-action=close-utility]'))close.disabled=!!u.busy;if(u.type==='settings')for(const field of this.shadowRoot.querySelectorAll('.utility-dialog [data-utility-field=name],#settings-area,#settings-media,#settings-url'))field.disabled=!!u.busy;const address=this.shadowRoot.getElementById('settings-url');if(address)address.setAttribute('aria-invalid',String(!!u.urlError));const addressSave=this.shadowRoot.querySelector('[data-action=save-app-url]');if(addressSave){addressSave.disabled=!!u.busy||!this._config.manager_app||!this._hass.user?.is_admin;addressSave.textContent=u.busy?'Сохраняем и перезапускаем…':'Сохранить и продолжить';}const button=this.shadowRoot.getElementById('utility-submit');if(button){button.disabled=!!u.busy||!!this._managerError&&u.type!=='tts';button.textContent=u.busy?'Выполнение…':({tts:'Произнести',settings:'Сохранить настройки',preview:'Проверить без выполнения'})[u.type]??'Выполнить';}}
    async _submitUtility(){const u=this._utility;if(!u||u.busy)return;if(u.type==='settings'&&u.section==='bridge'){const speaker=this._speakers.find(s=>s.id===u.speakerId);if(speaker&&(u.setupStep??this._bridgeStage(speaker))===0)return this._saveAppUrl();return;}u.message='';if(['tts','preview'].includes(u.type)&&(!u.text.trim()||!u.speakers.length)){u.message='Введите текст и выберите хотя бы одну колонку.';this._utilityMessage();return;}if(u.type==='settings'&&(!u.name.trim()||!u.area_id)){u.message='Укажите название и комнату.';this._utilityMessage();return;}u.busy=true;this._utilityMessage();try{
        if(u.type==='tts'){const outcomes=await Promise.all(u.speakers.map(id=>this._mediaAction(id,'play_media',{media_content_id:u.text.trim(),media_content_type:'text'})));u.message=outcomes.every(Boolean)?'Запрос озвучивания отправлен. Проверьте звук на колонках.':'Часть запросов не отправлена. Проверьте доступность колонок.';}
        if(u.type==='preview'){const data=await this._api('/api/phrase-check','POST',{text:u.text,speakers:u.speakers});u.results=data.results??[];u.message='Проверено. Действия не выполнялись.';this._render();}
        if(u.type==='settings'){
          const area=this._hass.areas?.[u.area_id]?.name,speaker=this._speakers.find(item=>item.id===u.speakerId);
          if(!area)throw Error('Комната не найдена в Home Assistant');
          if(!speaker)throw Error('Колонка не найдена. Обновите список.');
          const settings={name:u.name.trim(),media_entity:u.media_entity||null};
          if(area===speaker.area){
            await this._api('/api/speakers/'+encodeURIComponent(u.speakerId),'PATCH',settings);
            await this._loadManager();this._refreshMedia();
            u.message='Название и медиаплеер сохранены.';
          }else{
            await this._stationJob(u.speakerId,'configure',{area,...settings});u.message=this._stationMessages[u.speakerId];
          }
        }
      }catch(error){u.message=error.message??'Не удалось выполнить запрос. Повторите ещё раз.';}finally{u.busy=false;this._utilityMessage();}}
    async _stationJob(id,action,body={}){if(this._stationBusy.has(id)||this._managerError)return false;this._stationBusy.add(id);const u=this._utility;try{const response=await this._api('/api/speakers/'+encodeURIComponent(id)+'/'+action,'POST',body);if(!response.job_id)throw Error('Сервис не вернул операцию');for(let n=0;n<60;n++){const job=await this._api('/api/jobs/'+encodeURIComponent(response.job_id));this._stationMessages[id]=job.error??job.stage??'Выполнение…';if(u&&this._utility===u){u.message=this._stationMessages[id];this._utilityMessage();}if(job.status==='failed')throw Error(job.error??'Операция не выполнена. Предыдущие настройки сохранены.');if(job.status==='succeeded'){this._stationMessages[id]=({configure:'Настройки применены и проверены.',restart:'Мост перезапущен. Проверка выполнена.'})[action]??'Операция завершена.';await this._loadManager();return true;}await new Promise(resolve=>setTimeout(resolve,2000));}throw Error('Операция продолжается. Обновите состояние позже.');}catch(error){this._stationMessages[id]=error.message;return false;}finally{this._stationBusy.delete(id);if(u&&this._utility===u){u.message=this._stationMessages[id];this._utilityMessage();if(u.type==='control'||u.type==='settings'&&u.section==='bridge'){u.setupStep=undefined;const old=this.shadowRoot.querySelector('.utility-dialog')?.parentElement;if(old){const template=document.createElement('template');template.innerHTML=this._utilityDialog();old.replaceWith(template.content);}}}this._refreshMedia();}}
    _speakerEditor(disabled){return `<div class="overlay"><section class="dialog" role="dialog" aria-modal="true" aria-labelledby="speaker-title"><div class="dialog-header"><h2 id="speaker-title">Добавить колонку</h2><button data-action="close-speaker" aria-label="Закрыть" ${disabled}>✕</button></div><p class="help">Добавим колонку в список. Затем мастер поможет настроить доступ и подключить мост.</p><form id="speaker-form" class="speaker-form"><label>Название<input name="name" required placeholder="Алиса на кухне"></label><label>Комната<input name="area" required placeholder="Кухня"></label><label>IP-адрес колонки<input name="host" required placeholder="192.168.10.20"></label><label>SSH-порт<input name="port" required type="number" min="1" max="65535" value="2222"></label><label class="span">Модель колонки<select name="profile"><option value="default">Станция Lite 2</option><option value="midi">Станция MIDI</option></select></label><div class="span actions"><button type="button" data-action="close-speaker" ${disabled}>Отмена</button><button class="primary" type="submit" ${disabled||this._managerError?'disabled':''}>Добавить колонку</button></div></form></section></div>`;}
    async _fingerprint(id){this._busy=true;try{const data=await this._api('/api/speakers/'+encodeURIComponent(id)+'/fingerprint','POST',{});this._fingerprintData={...data,id};this._message='Сравните отпечаток SSH-ключа перед подтверждением доверия.';if(this._utility?.section==='bridge')this._utility.message=this._message;}catch(error){this._message=error.message;if(this._utility?.section==='bridge')this._utility.message=this._message;}finally{this._busy=false;this._render();}}
    async _trust(id){if(this._fingerprintData?.id!==id)return;this._busy=true;try{await this._api('/api/speakers/'+encodeURIComponent(id)+'/trust','POST',{fingerprint:this._fingerprintData.fingerprint,nonce:this._fingerprintData.nonce});this._fingerprintData=undefined;this._message='Отпечаток подтверждён. Теперь проверим вход с ключом приложения.';if(this._utility?.section==='bridge'){this._utility.message=this._message;this._utility.setupStep=2;}await this._loadManager();}catch(error){this._message=error.message;if(this._utility?.section==='bridge')this._utility.message=error.message;}finally{this._busy=false;this._render();}}
    async _register(form){if(this._busy||this._managerError)return;const values=Object.fromEntries(new FormData(form));values.port=Number(values.port);this._busy=true;try{const created=await this._api('/api/speakers','POST',values);await this._loadManager();this._speakerDialog=false;this._message='Колонка добавлена. Осталось настроить мост.';const added=this._speakers.find(s=>s.id===created.id)??this._speakers.find(s=>s.host===values.host);if(added)this._utility={type:'settings',section:'bridge',speakerId:added.id,message:''};}catch(error){this._message=error.message;}finally{this._busy=false;this._render();}}
    async _job(id,action){return this._stationJob(id,action,action==='confirm'?{voice_tested:true}:{});}
    async _save() {
      if (this._busy) return;
      if(this._managerError||!this._config.manager_url||this._pickerError||!customElements.get('ha-entity-picker')){this._renderEditorStatus();return;}
      const target = this._draftTarget;
      const normalized = new Map();
      for (const phrase of this._draftPhrases.split('\n').map(s => s.trim()).filter(Boolean)) {
        const key = normalize(phrase);
        if (!key || key.length > 1000) {this._message='Фраза пустая или слишком длинная.';this._render();return;}
        normalized.set(key, phrase);
      }
      if (!target || !normalized.size) {this._message='Выберите действие и добавьте хотя бы одну фразу.';this._render();return;}
      this._busy=true;this._message='';this._render();
      try {
        const registry = await this._records();
        const item = registry.find(r => r.entity_id===target && supported(r));
        if (!item) throw Error('Действие больше не существует. Обновите список.');
        if (this._editing && JSON.stringify(aliases(item)) !== this._baseline) throw Error('Фразы изменены в другом окне. Обновите список перед сохранением.');
        for (const other of registry.filter(r => r.entity_id!==target && supported(r) && this._selected(r))) {
          for (const phrase of aliases(other)) if (normalized.has(normalize(phrase))) throw Error('Эта фраза уже связана с «'+this._name(other)+'»: '+phrase);
        }
        let previousAssignment;
        let assignmentWritten=false;
        if(this._config.manager_url) {
          if(this._managerError)throw Error('Сервис колонок недоступен. Команда не изменена.');
          const current=await this._api('/api/assignments');
          previousAssignment=current.assignments?.[target]??null;
          await this._api('/api/assignments/'+encodeURIComponent(target),'PUT',this._all?{speakers:null}:{speakers:this._chosen});
          assignmentWritten=true;
        }
        try {
          await this._hass.callWS({type:'config/entity_registry/update', entity_id:target,
            aliases:[...(item.aliases ?? []).filter(v=>v===null),...normalized.values()], labels:[...new Set([...(item.labels ?? []),this._config.label])]});
        } catch(error) {
          if(assignmentWritten) {
            try {await this._api('/api/assignments/'+encodeURIComponent(target),'PUT',{speakers:previousAssignment});}
            catch {throw Error('Фразы не сохранены. Назначение колонок изменилось; проверьте его перед повтором.');}
          }
          throw error;
        }
        this._message='Сохранено. Колонки подхватят команду автоматически.';
        this._dialog=false;
        this._editing=undefined;this._draftTarget='';this._draftPhrases='';this._undo=undefined;
        await this._load();
      } catch(error) {this._message=error.message ?? String(error);}
      finally {this._busy=false;this._render();}
    }
    async _click(event) {
      const button = event.target.closest('button[data-action]');
      if (!button || button.disabled || this._busy) return;
      const {action,id}=button.dataset;
      if(['control','tts','settings','activity','preview','setup'].includes(action)){this._openUtility(action,id);return;}
      if(action==='save-app-url')return this._saveAppUrl();
      if(action==='key-added'){if(this._utility&&!this._stationBusy.has(this._utility.speakerId)){this._utility.setupStep=2;this._utility.message='';this._render();}return;}
      if(action==='settings-general'||action==='settings-bridge'){if(this._utility&&!this._utility.busy&&!this._stationBusy.has(this._utility.speakerId)){this._utility.section=action==='settings-bridge'?'bridge':'general';this._utility.setupStep=undefined;this._utility.message='';this._render();}return;}
      if(action==='setup-back'||action==='setup-next'){const u=this._utility,s=this._speakers.find(s=>s.id===u?.speakerId);if(u&&s&&!this._stationBusy.has(s.id)){u.setupStep=Math.max(0,Math.min(4,(u.setupStep??this._bridgeStage(s))+(action==='setup-next'?1:-1)));this._render();}return;}
      if(action==='copy-key')return this._copyPublicKey();
      if(action==='close-utility'){if(!this._utility?.busy){this._utility=null;this._render();}return;}
      if(action==='media')return this._mediaAction(id,button.dataset.service);
      if(action==='test-sound'){const s=this._speakers.find(item=>item.id===id);const ok=await this._mediaAction(id,'play_media',{media_content_id:'Проверка звука. Колонка '+s.name+' готова к работе.',media_content_type:'text'});if(this._utility){this._utility.message=ok?'Тестовая фраза отправлена. Подтвердите звучание на слух.':'Не удалось отправить тестовую фразу.';this._utilityMessage();}return;}
      if(action==='restart')return this._stationJob(id,'restart');
      if(action==='activity-refresh'){this._activityPage=0;return this._loadActivity();}
      if(action==='activity-next'||action==='activity-prev'){this._activityPage+=action==='activity-next'?1:-1;const region=this.shadowRoot.getElementById('activity-region');if(region)region.innerHTML=this._activityMarkup(this._utility?.speakerId);return;}
      if(action==='add-speaker'){this._speakerDialog=true;this._message='';this._render();return;}
      if(action==='close-speaker'){this._speakerDialog=false;this._render();return;}
      if(['commands','speakers','diagnostics'].includes(action)){this._tab=action;this._render();if(action==='diagnostics')this._loadActivity();return;}
      if(action==='retry-editor'){button.disabled=true;await this._loadManager();await this._bindNativePicker();return;}
      if(action==='close'){this._dialog=false;this._render();return;}
      if(['check','install','reboot','confirm','rollback'].includes(action))return this._job(id,action);
      if(action==='fingerprint')return this._fingerprint(id);
      if(action==='trust')return this._trust(id);
      if (action==='refresh') {this._message='';return this._load();}
      if (action==='new') {this._editing=undefined;this._draftTarget='';this._draftPhrases='';this._all=true;this._chosen=[];this._dialog=true;return this._render();}
      if (action==='edit') {
        const item=this._registry.find(r=>r.entity_id===id);
        this._editing=id;this._draftTarget=id;this._draftPhrases=aliases(item).join('\n');
        this._chosen=this._assignments[id]??[];this._all=this._assignments[id]===undefined;this._dialog=true;
        this._baseline=JSON.stringify(aliases(item));this._message='';this._render();
        this.shadowRoot.getElementById('phrases').focus();return;
      }
      if (action!=='disable' && action!=='undo') return;
      this._busy=true;
      try {
        const target=action==='undo' ? this._undo : id;
        const records=await this._hass.callWS({type:'config/entity_registry/list'});
        const item=records.find(r=>r.entity_id===target);
        if (!item) throw Error('Действие не найдено.');
        const labels=action==='undo' ? [...new Set([...(item.labels ?? []),this._config.label])] : (item.labels ?? []).filter(l=>l!==this._config.label);
        await this._hass.callWS({type:'config/entity_registry/update',entity_id:target,labels});
        this._undo=action==='disable' ? target : undefined;
        this._message=action==='disable' ? 'Голосовое соответствие отключено. Автоматизация сохранена.' : 'Голосовое соответствие восстановлено.';
        if (this._editing===target) {this._editing=undefined;this._draftTarget='';this._draftPhrases='';}
        await this._load();
      } catch(error) {this._message=error.message ?? String(error);}
      finally {this._busy=false;this._render();}
    }
  }
  if (!customElements.get('alice-voice-card')) customElements.define('alice-voice-card',AliceVoiceCard);
  window.customCards=window.customCards ?? [];
  window.customCards.push({type:'alice-voice-card',name:'Алиса — голосовые команды',description:'Связать точную фразу с автоматизацией или скриптом HA'});
})();
