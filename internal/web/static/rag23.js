(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const labels = {baseline:"Базовый RAG",filter:"Реранкинг + фильтр",rewrite:"Query rewrite",full:"Rewrite + фильтр"};
  const n = (tag,text,cls) => { const e=document.createElement(tag); if(text!==undefined)e.textContent=text; if(cls)e.className=cls; return e; };
  let report, loadedSettings=false;
  function options(prefix) { return {mode:$(prefix+"-mode").value,topKBefore:Number($(prefix+"-before").value),topKAfter:Number($(prefix+"-after").value),relevanceThreshold:Number($(prefix+"-threshold").value)}; }
  function result(v) {
    const card=window.CodexRAG.answerCard(labels[v.mode]||v.mode,v.answer);
    const r=v.answer.retrieval;
    card.append(n("p",`${r.candidates?.length||0} кандидатов → ${r.sources?.length||0} источников · порог ${r.options?.relevanceThreshold??"—"}/3`,"rag-metrics"));
    return card;
  }
  function renderQuestion() {
    if(!report?.comparisons.length)return;
    const c=report.comparisons[Number($("rag23-question").value)||0];
    const out=$("rag23-pair");out.replaceChildren(n("h2",c.question.query));
    const expected=n("details");expected.append(n("summary",`Ожидания · ${c.question.answerable?"ответ есть в корпусе":"нет запрошенных данных"}`));
    for(const text of c.question.expectations)expected.append(n("p",text));
    for(const e of c.question.relevant||[])expected.append(n("p",`${e.source}: «${e.quote}»`));
    out.append(expected);
    const cols=n("div",undefined,"rag-columns");
    for(const mode of [$("rag23-left").value,$("rag23-right").value]) {
      const v=c.results.find(v=>v.mode===mode);
      if(v){const card=result(v);if(c.question.answerable)card.append(n("p",`Эталонный фрагмент: до ${v.evidenceBefore?"✓":"—"} · после ${v.evidenceAfter?"✓":"—"}`,"rag-metrics"));cols.append(card);} else cols.append(n("p","Этот режим ещё не выполнен."));
    }
    out.append(cols);
  }
  function renderSummary() {
    const out=$("rag23-summary");out.replaceChildren();
    out.append(n("p",`${report.comparisons.length} вопросов · ${report.model} · K: ${report.settings.topKBefore} → ${report.settings.topKAfter} · порог ${report.settings.relevanceThreshold}/3 · ${report.complete?"завершено":"неполный прогон"}`));
    const wrap=n("div",undefined,"monitor-table-wrap"),table=n("table");
    const head=n("tr");for(const h of ["Режим","Корректность / полнота","Источников, среднее","LLM токены","Время, среднее"])head.append(n("th",h));table.append(head);
    for(const mode of Object.keys(labels)) {
      const items=report.comparisons.flatMap(c=>c.results.filter(v=>v.mode===mode)),rated=items.filter(v=>v.answer.review);
      const avg=fn=>items.length?(items.reduce((sum,v)=>sum+fn(v),0)/items.length):0;
      const grade=key=>rated.length?(rated.reduce((s,v)=>s+v.answer.review[key],0)/rated.length).toFixed(2):"—";
      const tr=n("tr");for(const text of [labels[mode],`${grade("correctness")} / ${grade("completeness")}`,avg(v=>v.answer.retrieval.sources?.length||0).toFixed(1),items.reduce((s,v)=>s+v.answer.usage.TotalTokens,0).toLocaleString("ru"),`${(avg(v=>v.answer.durationMs)/1000).toFixed(1)} с`])tr.append(n("td",text));table.append(tr);
    }
    wrap.append(table);out.append(wrap,n("p",report.evaluation,"rag-method"));
    if(report.calibration){const c=report.calibration,details=n("details");details.append(n("summary",`Калибровка порога на ${c.questions.length} отдельных вопросах`),n("p",c.rationale));for(const v of c.scores)details.append(n("p",`Порог ${v.threshold}: попаданий ${v.answerableHits}, правильных пустых выборок ${v.unanswerableEmpty}, выбрано чанков ${v.selected}`));out.append(details);}
    $("rag23-question").replaceChildren(...report.comparisons.map((c,i)=>{const o=n("option",`${c.question.id} · ${c.question.query}`);o.value=String(i);return o;}));
    if(!loadedSettings){for(const prefix of ["rag-chat","rag23-live"]){$(prefix+"-before").value=report.settings.topKBefore;$(prefix+"-after").value=report.settings.topKAfter;$(prefix+"-threshold").value=report.settings.relevanceThreshold;}loadedSettings=true;}
    renderQuestion();
  }
  async function load(){
    $("rag23-status").textContent="Загрузка сравнения четырёх режимов…";
    try{const response=await fetch("/api/rag/experiment",{headers:{"X-Codex-Chat":"1"}});const value=await response.json();if(!response.ok)throw new Error(value.error);report=value;renderSummary();$("rag23-status").textContent="Сохранённый эксперимент · реальные запросы OpenAI · ожидания скрыты от всех этапов RAG.";}
    catch(e){$("rag23-status").textContent=e.message;}
  }
  for(const id of ["rag23-question","rag23-left","rag23-right"])$(id).addEventListener("change",renderQuestion);
  $("rag23-refresh").addEventListener("click",load);
  $("rag23-live-form").addEventListener("submit",async e=>{
    e.preventDefault();const button=$("rag23-run"),settings=options("rag23-live");button.disabled=true;
    $("rag23-live-status").textContent=`Выполняется ${labels[settings.mode]}: поиск, обработка и ответ…`;$("rag23-live-output").replaceChildren();
    try{const response=await fetch("/api/rag/run",{method:"POST",headers:{"Content-Type":"application/json","X-Codex-Chat":"1"},body:JSON.stringify({query:$("rag23-query").value,model:$("model-select").value,options:settings})});const value=await response.json();if(!response.ok)throw new Error(value.error);$("rag23-live-output").append(result(value));$("rag23-live-status").textContent="Готово. Новый запрос выполнен в независимой сессии; сохранённый эксперимент не изменён.";}
    catch(e){$("rag23-live-status").textContent=e.message;}finally{button.disabled=false;}
  });
  window.CodexRAG23={load,labels,chatOptions:()=>options("rag-chat"),setChatBusy:busy=>{for(const id of ["mode","before","after","threshold"])$("rag-chat-"+id).disabled=busy;}};
})();
