(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const panel=$("monitors-panel"), form=$("monitor-form"), status=$("monitors-status"), formStatus=$("monitor-form-status"), grant=$("monitors-chat-grant");
  const field=name=>form.elements.namedItem(name);
  const date=value=>value && value<1e12 ? new Date(value*1000).toLocaleString() : "—";
  const el=(tag,text,cls)=>{const n=document.createElement(tag);if(text!==undefined)n.textContent=text;if(cls)n.className=cls;return n;};
  const button=(text,fn)=>{const b=el("button",text,"memory-button");b.type="button";b.addEventListener("click",()=>Promise.resolve().then(fn).catch(e=>status.textContent=e.message));return b;};
  let busy=false, grantDirty=false, selected="", editing=null, catalog=[], catalogSource="", catalogGeneration=0, argumentReaders=[];
  field("timezone").value=Intl.DateTimeFormat().resolvedOptions().timeZone || "Europe/Moscow";
  async function request(path,body){const response=await fetch(path,{method:body?"POST":"GET",headers:{"X-Codex-Chat":"1","Content-Type":"application/json"},...(body?{body:JSON.stringify(body)}:{})});const data=await response.json();if(!response.ok)throw new Error(data.error || "Планировщик недоступен");return data;}
  function scheduleFields(){for(const n of form.querySelectorAll("[data-schedule]")){n.hidden=n.dataset.schedule!==field("scheduleKind").value;for(const input of n.querySelectorAll("input")){input.disabled=n.hidden;input.required=!n.hidden;}}$("monitor-summary-period").hidden=field("summaryMode").value!=="period";field("summaryMinutes").disabled=field("summaryMode").value!=="period";$("monitor-summary-prompt").hidden=field("summaryMode").value==="none";}
  function renderArguments(values){
    argumentReaders=[];const area=$("monitor-arguments");area.replaceChildren();
    const tool=catalog.find(t=>t.name===field("toolName").value);$("monitor-tool-description").textContent=tool?.description || "Выберите доступный инструмент чтения.";$("monitor-schema").textContent=tool?JSON.stringify(tool.inputSchema,null,2):"";
    if(!tool)return;
    const schema=tool.inputSchema || {}, properties=schema.properties || {}, required=new Set(schema.required || []);
    // Use a full JSON editor for composed schemas, refs and free-form objects.
    const complex=schema.$ref || schema.allOf || schema.anyOf || schema.oneOf || !Object.keys(properties).length || Object.values(properties).some(p=>p.$ref || p.allOf || p.anyOf || p.oneOf || !["string","number","integer","boolean"].includes(p.type));
    if(complex){const label=el("label","Параметры (JSON-объект)"),input=el("textarea");input.rows=8;input.value=JSON.stringify(values || Object.fromEntries(Object.entries(properties).filter(([,p])=>p.default!==undefined).map(([k,p])=>[k,p.default])),null,2);label.append(input);area.append(label);argumentReaders.push(()=>{let v;try{v=JSON.parse(input.value);}catch{throw new Error("Проверьте синтаксис JSON параметров");}if(!v || Array.isArray(v) || typeof v!=="object")throw new Error("Параметры должны быть JSON-объектом");return v;});return;}
    for(const [key,p] of Object.entries(properties)){
      const label=el("label",`${p.title || key}${required.has(key)?" *":""}`);let input;
      const val=values && Object.hasOwn(values,key)?values[key]:p.default;
      if(p.enum || p.type==="boolean"){
        input=el("select");const blank=el("option","Не задано");blank.value="";input.append(blank);
        for(const v of p.enum || [true,false]){const option=el("option",String(v));option.value=JSON.stringify(v);input.append(option);}input.value=val!==undefined?JSON.stringify(val):"";
      }else{input=el("input");input.type=["number","integer"].includes(p.type)?"number":"text";if(input.type==="number"){input.step=p.type==="integer"?"1":"any";if(p.minimum!==undefined)input.min=p.minimum;if(p.maximum!==undefined)input.max=p.maximum;}if(p.minLength!==undefined)input.minLength=p.minLength;if(p.maxLength!==undefined)input.maxLength=p.maxLength;input.value=val??"";}
      input.required=required.has(key);label.append(input);if(p.description)label.append(el("small",p.description));area.append(label);
      argumentReaders.push(()=>{if(input.value===""){if(required.has(key))throw new Error(`Заполните параметр ${key}`);return {};}let value=input.value;if(input.tagName==="SELECT")value=JSON.parse(value);else if(input.type==="number")value=Number(value);return {[key]:value};});
    }
  }
  async function loadTools(preferred,values){
    const generation=++catalogGeneration,source=field("connectionId").value;
    catalog=[];catalogSource="";field("toolName").replaceChildren();renderArguments();
    if(!source){formStatus.textContent="Добавьте MCP-сервер в настройках проекта.";return;}
    formStatus.textContent="Загружаю инструменты…";
    try{const data=await request(`/api/monitors?connectionId=${encodeURIComponent(source)}`);if(generation!==catalogGeneration)return;catalog=data.tools;catalogSource=source;
      for(const t of catalog){const option=el("option",t.name);option.value=t.name;field("toolName").append(option);}if(catalog.some(t=>t.name===preferred))field("toolName").value=preferred;
      renderArguments(values);formStatus.textContent=catalog.length?`Инструментов чтения: ${catalog.length}`:"Сервер не предоставил инструментов с readOnlyHint.";
    }catch(e){if(generation===catalogGeneration)formStatus.textContent=e.message;}
  }
  function spec(){
    if(catalogSource!==field("connectionId").value || !field("toolName").value)throw new Error("Дождитесь загрузки и выберите инструмент");
    const kind=field("scheduleKind").value,schedule={kind};
    if(kind==="interval")schedule.intervalSeconds=Number(field("intervalMinutes").value)*60;
    if(kind==="once")schedule.at=Math.floor(new Date(field("at").value).getTime()/1000);
    if(kind==="daily"){schedule.time=field("dailyTime").value;schedule.timezone=field("timezone").value;}
    return {processor:field("template").value==="market"?"market":"",name:field("name").value,connectionId:field("connectionId").value,toolName:field("toolName").value,arguments:Object.assign({},...argumentReaders.map(read=>read())),schedule,summaryMode:field("summaryMode").value,summarySeconds:field("summaryMode").value==="period"?Number(field("summaryMinutes").value)*60:0,summaryPrompt:field("summaryPrompt").value};
  }
  function resetEditor(){editing=null;$("monitor-form-title").textContent="Новая задача";$("monitor-submit").textContent="Создать задачу";$("monitor-cancel").hidden=true;$("monitor-preview-result").hidden=true;}
  async function edit(job){
    editing=job;field("template").value=job.processor==="market" || !job.toolName?"market":"custom";field("name").value=job.name;field("connectionId").value=job.connectionId;
    const schedule=job.schedule?.kind?job.schedule:{kind:"interval",intervalSeconds:job.collectSeconds};
    field("scheduleKind").value=schedule.kind;field("intervalMinutes").value=(schedule.intervalSeconds || 1800)/60;field("dailyTime").value=schedule.time || "18:00";field("timezone").value=schedule.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone;
    if(schedule.at){const t=new Date(schedule.at*1000);t.setMinutes(t.getMinutes()-t.getTimezoneOffset());field("at").value=t.toISOString().slice(0,16);}
    field("summaryMode").value=job.summaryMode || "period";field("summaryMinutes").value=(job.summarySeconds || 3600)/60;field("summaryPrompt").value=job.summaryPrompt || "";
    $("monitor-form-title").textContent=`Изменить: ${job.name}`;$("monitor-submit").textContent="Сохранить изменения";$("monitor-cancel").hidden=false;scheduleFields();form.scrollIntoView({behavior:"smooth"});
    await loadTools(job.toolName || "moex_quotes",job.arguments || {tickers:job.tickers,board:"TQBR",engine:"stock",market:"shares"});
    if(!catalog.some(t=>t.name===(job.toolName || "moex_quotes")))formStatus.textContent="Прежний инструмент недоступен. Выберите замену и проверьте параметры.";
  }
  async function history(id){
    const data=await request(`/api/monitors?id=${encodeURIComponent(id)}`);selected=id;$("monitor-history").hidden=false;$("monitor-history-title").textContent=`Результаты: ${data.job.name}`;
    const summaries=$("monitor-summaries");summaries.replaceChildren();
    if(!data.summaries.length)summaries.append(el("p",data.job.summaryMode==="none"?"Сводки отключены. Ответы сохраняются в журнале.":"Сводок пока нет."));
    for(const summary of data.summaries){const article=el("article",undefined,"mcp-tool"),text=el("div",undefined,"message-content");window.CodexMarkdown.render(text,summary.text,message=>status.textContent=message);article.append(el("h3",`${date(summary.from)} — ${date(summary.to)}`),text);if(summary.warning)article.append(el("p",summary.warning,"monitor-warning"));if(summary.model)article.append(el("p",`${summary.model} · ${summary.tokens} токенов`));
      if(summary.stats?.length){const table=el("table",undefined,"monitor-table"),head=el("tr");for(const title of ["Тикер","Цена, ₽","Изменение*","Снимки","Время источника"])head.append(el("th",title));table.append(head);for(const stat of summary.stats){const row=el("tr");for(const value of [stat.ticker,stat.last.toFixed(2),`${stat.changePercent.toFixed(2)}%`,String(stat.count),new Date(stat.sourceTime).toLocaleString()])row.append(el("td",value));table.append(row);}const wrap=el("div",undefined,"monitor-table-wrap");wrap.append(table);article.append(wrap,el("p","* Между первым и последним наблюдениями периода, не изменение за торговый день."));}
      summaries.append(article);}
    const runs=$("monitor-runs"),openRuns=new Set([...runs.querySelectorAll("details[open]")].map(n=>n.dataset.run));runs.replaceChildren();if(!data.runs.length)runs.append(el("p","Запусков пока нет."));
    for(const run of data.runs){const article=el("article",undefined,"mcp-tool");article.append(el("p",`${date(run.at)} · ${run.error?"Ошибка: "+run.error:run.executed?"Вызов выполнен":"Наблюдений: "+run.samples}${run.summary?" · сводка сохранена":""}`));if(run.result){const details=el("details");details.dataset.run=String(run.at);details.open=openRuns.has(String(run.at));details.append(el("summary","Исходный ответ MCP"),el("pre",JSON.stringify(run.result,null,2),"monitor-json"));article.append(details);}runs.append(article);}
  }
  const scheduleText=j=>j.schedule?.kind==="once"?`Один раз: ${date(j.schedule.at)}`:j.schedule?.kind==="daily"?`Ежедневно в ${j.schedule.time} (${j.schedule.timezone})`:`Каждые ${(j.schedule?.intervalSeconds || j.collectSeconds)/60} мин`;
  async function load(quiet=false){
    if(busy)return;busy=true;
    try{const data=await request("/api/monitors");if(!grantDirty)grant.checked=data.allowChatChanges;
      const source=field("connectionId"),previous=source.value;source.replaceChildren();for(const c of data.sources){const option=el("option",c.name);option.value=c.id;source.append(option);}if(data.sources.some(c=>c.id===previous))source.value=previous;
      if(catalogSource!==source.value)await loadTools();
      const list=$("monitors-list"),openParams=new Set([...list.querySelectorAll("details[open]")].map(n=>n.dataset.job));list.replaceChildren();if(!data.jobs.length)list.append(el("p","Задач пока нет. Создайте первую в форме или через чат."));
      for(const job of data.jobs){const card=el("article",undefined,"task-current");card.append(el("h2",job.name),el("p",`${job.sourceName} · ${job.toolName || "moex_quotes (шаблон Мосбиржи)"} · ${job.completed?"Завершено":job.paused?"Пауза":job.running?"Выполняется":"Активно"}`),el("p",scheduleText(job)),el("p",`Последний запуск: ${date(job.lastRun)}`));if(!job.paused&&!job.completed)card.append(el("p",`Следующий запуск: ${date(job.nextCollect)}`));if(job.lastError)card.append(el("p",job.lastError,"monitor-warning"));
        const params=el("details");params.dataset.job=job.id;params.open=openParams.has(job.id);params.append(el("summary","Параметры задачи"),el("pre",JSON.stringify({arguments:job.arguments || {tickers:job.tickers},summaryMode:job.summaryMode || "market",summarySeconds:job.summarySeconds,summaryPrompt:job.summaryPrompt},null,2),"monitor-json"));card.append(params);
        const actions=el("div",undefined,"memory-actions");actions.append(button("Результаты и журнал",()=>history(job.id)),button("Изменить",()=>edit(job)));
        if(!job.completed)actions.append(button(job.paused?"Возобновить":"Пауза",()=>mutate("pause",{id:job.id,version:job.version,paused:!job.paused})));
        if(!job.paused&&!job.completed){const run=button("Запустить сейчас",()=>mutate("run",{id:job.id,version:job.version}));run.disabled=job.running;actions.append(run);}
        actions.append(button("Удалить",()=>{if(window.confirm(`Удалить «${job.name}» и всю историю?`))return mutate("delete",{id:job.id,version:job.version});}));card.append(actions);list.append(card);
      }
      if(selected && data.jobs.some(j=>j.id===selected))await history(selected);else {selected="";$("monitor-history").hidden=true;}
      if(!quiet)status.textContent=`Заданий: ${data.jobs.length}. Обновление каждые 10 секунд.`;
    }catch(e){status.textContent=e.message;}finally{busy=false;}
  }
  async function mutate(action,body){
    if(busy)return;busy=true;const buttons=panel.querySelectorAll("button");buttons.forEach(b=>b.disabled=true);
    try{const data=await request(`/api/monitors/${action}`,body);if(["create","update"].includes(action)){selected=data.id;resetEditor();formStatus.textContent="Задача сохранена.";}if(action==="settings")grantDirty=false;if(action==="delete"&&editing?.id===body.id)resetEditor();status.textContent=action==="run"?"Запуск поставлен в очередь.":"Сохранено.";}
    catch(e){status.textContent=e.message;formStatus.textContent=e.message;}finally{busy=false;buttons.forEach(b=>b.disabled=false);await load(true);}
  }
  form.addEventListener("submit",event=>{event.preventDefault();try{mutate(editing?"update":"create",{...spec(),...(editing?{id:editing.id,version:editing.version}:{})});}catch(e){formStatus.textContent=e.message;}});
  $("monitor-preview").addEventListener("click",async()=>{if(busy)return;busy=true;$("monitor-preview").disabled=true;formStatus.textContent="Выполняю пробный вызов…";try{const data=await request("/api/monitors/preview",spec());$("monitor-preview-result").textContent=JSON.stringify(data,null,2);$("monitor-preview-result").hidden=false;formStatus.textContent="Ответ получен. Пробный запуск не создаёт задачу.";}catch(e){formStatus.textContent=e.message;}finally{busy=false;$("monitor-preview").disabled=false;}});
  field("connectionId").addEventListener("change",()=>loadTools());field("toolName").addEventListener("change",()=>renderArguments());
  for(const name of ["scheduleKind","summaryMode"])field(name).addEventListener("change",scheduleFields);
  field("template").addEventListener("change",async()=>{if(field("template").value!=="market")return;field("name").value="Обзор рынка";field("scheduleKind").value="interval";field("intervalMinutes").value=30;field("summaryMode").value="period";field("summaryMinutes").value=60;field("summaryPrompt").value="Обобщи котировки по тикерам. Укажи время биржевых данных и статус торгов, отметь задержки и отсутствие новых данных. Не придумывай причины движения цен.";scheduleFields();await loadTools("moex_quotes",{tickers:["SBER","GAZP","LKOH"],board:"TQBR",engine:"stock",market:"shares"});if(!catalog.some(t=>t.name==="moex_quotes"))formStatus.textContent="Выберите сервер с инструментом moex_quotes, затем примените шаблон снова.";});
  $("monitor-cancel").addEventListener("click",resetEditor);grant.addEventListener("change",()=>grantDirty=true);$("monitors-save-grant").addEventListener("click",()=>mutate("settings",{enabled:grant.checked}));$("monitors-refresh").addEventListener("click",()=>load());
  scheduleFields();setInterval(()=>{if(!panel.hidden&&!document.hidden)load(true);},10000);window.CodexMonitors=Object.freeze({load});
})();
