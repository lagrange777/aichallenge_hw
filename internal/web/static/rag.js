(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const node = (tag, text, className) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; if (className) n.className = className; return n; };
  let report;
  function trace(r) {
    const box = node("details", undefined, "rag-trace");
    box.append(node("summary", r.mode === "rag" ? `С RAG · ${r.sources?.length || 0} источников · ${r.strategy} · ${r.durationMs ? "поиск " + r.durationMs + " мс" : "общая выборка"}` : "Без RAG · поиск по документам выключен"));
    if (r.mode !== "rag") return box;
    if(r.grounding){const g=r.grounding;box.append(node("p",`Проверка цитат: ${g.status} · утверждений ${g.claims.length} · цитат ${g.quotes.length} · попыток ${g.attempts}`));for(const c of g.checks)box.append(node("p",`${c.index+1}. ${c.verdict}: ${c.reason}`));}
    if(r.resolvedQuery) box.append(node("p",`Уточнённый вопрос: ${r.resolvedQuery}`),node("p",`Контекст задачи: версия ${r.dialogue?.version}, записей ${r.dialogue?.entries?.length || 0}`));
    if (r.options) box.append(node("p", `Режим: ${window.CodexRAG23?.labels[r.options.mode] || r.options.mode} · K ${r.options.topKBefore} → ${r.options.topKAfter} · порог ${r.options.relevanceThreshold}/3`));
    if (r.query) box.append(node("p", `Исходный запрос: ${r.originalQuery || r.query}`));
    if (r.rewrittenQuery) box.append(node("p", `Поисковый запрос: ${r.rewrittenQuery}`));
    if (r.warning) box.append(node("p", r.warning, "rag-warning"));
    if (r.empty) box.append(node("p", "Источники не отобраны. Ответ модели не генерировался."));
    for (const step of r.steps || []) box.append(node("p", `${step.name} · ${step.model} · ${step.durationMs} мс · ${step.usage.TotalTokens} LLM-токенов${step.attempts > 1 ? " · попыток: " + step.attempts : ""}${step.warning ? " · " + step.warning : ""}`));
    if (r.candidates?.length) {
      const candidates=node("details");candidates.append(node("summary", `Кандидаты до отбора: ${r.candidates.length} → ${r.sources?.length || 0}`));
      const decisions={selected:"В контексте",below_threshold:"Ниже порога",top_k:"За пределами top-K",context_budget:"Лимит контекста"};
      for (const c of r.candidates) {
        const item=node("details",undefined,"document-chunk");
        item.append(node("summary", `#${c.rank} · ${c.source} · ${c.evaluated ? c.relevance+"/3" : "без оценки"} · ${decisions[c.decision] || c.decision}`));
        item.append(node("p",`${c.section} · chunk_id: ${c.chunkId}`),node("p",`Ранг исходного запроса: ${c.originalRank || "—"}; rewrite: ${c.rewriteRank || "—"}. Cosine: ${c.score.toFixed(3)} (запрос первого появления). RRF: ${(c.fusionScore || 0).toFixed(4)}.`));
        if(c.reason)item.append(node("p",c.reason));if(c.evidence)item.append(node("blockquote",c.evidence));item.append(node("pre",c.text));candidates.append(item);
      }
      box.append(candidates);
    }
    box.append(node("p", `Эмбеддинг: ${r.embeddingModel} · ${r.embeddingTokens || 0} токенов. Ссылки: ${(r.citations || []).join(", ") || "нет"}. Наличие ссылки ещё не гарантирует, что она подтверждает утверждение.`));
    if (r.invalidCitations?.length) box.append(node("p", `Неизвестные ссылки: ${r.invalidCitations.join(", ")}`, "rag-warning"));
    for (const s of r.sources || []) {
      const card = node("details", undefined, "document-chunk");
      card.append(node("summary", `[${s.ref}] ${s.source} · ${s.section} · ${s.score.toFixed(3)}`));
      card.append(node("p", `${s.title} · chunk_id: ${s.chunkId}`), node("pre", s.text));
      box.append(card);
    }
    return box;
  }
  function answerCard(label, a) {
    const card = node("article", undefined, "rag-answer");
    card.append(node("h3", label));
    if (a.review) {
      const r = a.review;
      card.append(node("p", `Корректность ${r.correctness}/2 · полнота ${r.completeness}/2`, "rag-grade"));
      card.append(node("p", `${r.hallucination ? "Есть неподтверждённые утверждения" : "Галлюцинации не отмечены"}${r.abstention ? " · признан недостаток информации" : ""}${r.citationSupport === false ? " · ссылки не подтверждают все утверждения" : ""}`, "rag-review-label"));
    }
    const body = node("div", undefined, "message-content");
    window.CodexMarkdown.render(body, a.text, () => {});
    card.append(body, node("p", `${a.model} · ${(a.durationMs / 1000).toFixed(1)} с · токены ${a.usage.InputTokens} → ${a.usage.OutputTokens} · всего ${a.usage.TotalTokens}`, "rag-metrics"));
    if (a.retrieval) card.append(trace(a.retrieval));
    if (a.review) { const review = node("details"); review.append(node("summary", "Обоснование оценки"), node("p", a.review.rationale)); card.append(review); }
    return card;
  }
  function renderPair(pair, target) {
    target.replaceChildren();
    target.append(node("h2", pair.question.query));
    if (pair.question.expectations?.length) {
      const expected = node("details", undefined, "rag-expectations");
      expected.append(node("summary", "Ожидания и эталонные источники"));
      const list = node("ul"); pair.question.expectations.forEach(e => list.append(node("li", e))); expected.append(list);
      for (const e of pair.question.relevant) expected.append(node("p", `${e.source}: «${e.quote}»`));
      expected.append(node("p", `Эталонный фрагмент в top-5: ${pair.evidenceHit ? "да" : "нет"}. Ожидания не передавались отвечающей модели.`)); target.append(expected);
    }
    const columns = node("div", undefined, "rag-columns");
    columns.append(answerCard("Без RAG", pair.without), answerCard("С RAG", pair.with)); target.append(columns);
  }
  function renderReport() {
    const summary = $("rag-summary"); summary.replaceChildren();
    summary.append(node("p", `${report.pairs.length} из 10 вопросов · ${report.model} · top-${report.topK} · ${report.strategy} · ${report.complete ? "прогон завершён" : "неполный прогон"}`));
    const scores = report.pairs.filter(p => p.with.review && p.without.review);
    if (scores.length) {
      const means = mode => {
        const total = scores.reduce((a, p) => { const r = p[mode].review; return [a[0] + r.correctness, a[1] + r.completeness, a[2] + Number(r.hallucination)]; }, [0, 0, 0]);
        return `${(total[0]/scores.length).toFixed(1)}/2 · ${(total[1]/scores.length).toFixed(1)}/2 · галлюцинации ${total[2]}/${scores.length}`;
      };
      const stats = node("div", undefined, "rag-columns");
      for (const [label, mode] of [["Без RAG", "without"], ["С RAG", "with"]]) {
        const n = node("div", undefined, "rag-stat"); n.append(node("strong", label), node("p", means(mode))); stats.append(n);
      }
      summary.append(node("p", "Средняя корректность · полнота · ответы с галлюцинациями"), stats);
    }
    summary.append(node("p", report.evaluation, "rag-method"));
    $("rag-question-select").replaceChildren(...report.pairs.map((p, i) => {const o = node("option", `${p.question.id} · ${p.question.query}`); o.value = String(i); return o;}));
    if (report.pairs.length) renderPair(report.pairs[0], $("rag-saved-result"));
  }
  async function load() {
    $("rag-status").textContent = "Загрузка отчёта…";
    try {
      const response = await fetch("/api/rag/report", {headers:{"X-Codex-Chat":"1"}});
      const value = await response.json(); if (!response.ok) throw new Error(value.error || "Ошибка отчёта");
      report = value; renderReport(); $("rag-status").textContent = "Сохранённый прогон · реальные ответы OpenAI. Новое сравнение можно запустить ниже.";
    } catch (e) { $("rag-status").textContent = e.message; }
  }
  $("rag-question-select").addEventListener("change", e => {if (report) renderPair(report.pairs[Number(e.target.value)], $("rag-saved-result"));});
  $("rag-refresh").addEventListener("click", load);
  $("rag-live-form").addEventListener("submit", async e => {
    e.preventDefault(); const button = $("rag-live-submit"); button.disabled = true;
    $("rag-live-status").textContent = "Выполняются два запроса к модели и поиск по документам…";
    $("rag-live-result").replaceChildren();
    try {
      const response = await fetch("/api/rag/compare", {method:"POST",headers:{"Content-Type":"application/json","X-Codex-Chat":"1"},body:JSON.stringify({query:$("rag-live-query").value,model:$("model-select").value})});
      const result = await response.json(); if (!response.ok) throw new Error(result.error || "Ошибка сравнения");
      renderPair(result, $("rag-live-result")); $("rag-live-status").textContent = "Готово · два новых ответа в независимых сессиях. Этот запуск не меняет сохранённый отчёт; оценки не выставлялись.";
    } catch (err) { $("rag-live-status").textContent = err.message; } finally {button.disabled = false;}
  });
  window.CodexRAG = {load, trace, answerCard};
})();
