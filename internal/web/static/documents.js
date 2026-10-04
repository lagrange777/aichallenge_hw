(() => {
  "use strict";
  const $ = id => document.getElementById(id);
  const el = (tag, text) => { const n = document.createElement(tag); if (text !== undefined) n.textContent = text; return n; };
  const labels = { fixed: "Фиксированный размер", structured: "По структуре" };
  let info = null, offset = 0, loading = false, searching = false, pageGeneration = 0;
  async function request(path, body) {
    const response = await fetch(path, { method: body === undefined ? "GET" : "POST", headers: { "X-Codex-Chat": "1", ...(body === undefined ? {} : { "Content-Type": "application/json" }) }, ...(body === undefined ? {} : { body: JSON.stringify(body) }) });
    const data = await response.json(); if (!response.ok) throw new Error(data.error || "Не удалось загрузить документы"); return data;
  }
  function table(headers, rows) {
    const t = el("table"), head = el("thead"), hr = el("tr"), body = el("tbody"); t.className = "monitor-table";
    for (const title of headers) { const th = el("th", title); th.scope = "col"; hr.append(th); } head.append(hr);
    for (const values of rows) { const tr = el("tr"); for (const value of values) tr.append(el("td", String(value))); body.append(tr); } t.append(head, body); return t;
  }
  function chunkCard(c, score, rank) {
    const d = el("details"), title = el("summary", `${rank ? rank + ". " : ""}${c.source} · ${c.section}`); d.className = "document-chunk";
    d.append(title); if (score !== undefined) d.append(el("p", `Косинусное сходство: ${score.toFixed(4)} (не вероятность правильного ответа)`));
    d.append(el("p", `${c.title} · символы ${c.start}–${c.end} · ${c.end - c.start} символов`), el("p", `chunk_id: ${c.chunk_id}`), el("pre", c.text)); return d;
  }
  async function chunks() {
    const generation = ++pageGeneration, strategy = $("documents-strategy").value;
    $("documents-prev").disabled = true; $("documents-next").disabled = true; $("documents-chunks-status").textContent = "Загрузка чанков…";
    try {
      const data = await request(`/api/documents/chunks?strategy=${strategy}&offset=${offset}`); if (generation !== pageGeneration) return;
      $("documents-chunks").replaceChildren(...data.chunks.map(c => chunkCard(c)));
      const count = info.strategies.find(s => s.strategy === strategy).chunks;
      $("documents-chunks-status").textContent = `${offset + (data.chunks.length ? 1 : 0)}–${offset + data.chunks.length} из ${count}`;
      $("documents-prev").disabled = offset === 0; $("documents-next").disabled = offset + data.chunks.length >= count;
    } catch (e) { if (generation === pageGeneration) $("documents-chunks-status").textContent = e.message; }
  }
  async function load() {
    if (loading) return; loading = true; $("documents-refresh").disabled = true;
    try {
      const data = await request("/api/documents"); info = data.index;
      $("documents-content").hidden = false; $("documents-status").textContent = `Построен ${new Date(info.created_at).toLocaleString()}`;
      $("documents-summary").replaceChildren(el("p", `${info.documents.length} документов · ${info.words.toLocaleString()} слов · ≈ ${info.estimated_pages_at_400_words.toFixed(1)} страниц по 400 слов`), el("p", `${info.embedding_model} · ${info.embedding_dimensions} координат · до ${info.chunking.size_characters} Unicode-символов в чанке · перекрытие ${info.chunking.overlap_characters}`));
      $("documents-stats").replaceChildren(table(["Стратегия", "Чанки", "Средний размер, симв.", "Пересечения разделов", "Векторы, КиБ"], info.strategies.map(s => [labels[s.strategy], s.chunks, s.mean_characters.toFixed(0), s.cross_section_chunks, (s.vector_bytes / 1024).toFixed(0)])));
      $("documents-sources").replaceChildren(...info.documents.map(d => el("p", `${d.source} · ${d.words} слов · ${d.sections.length} разделов`)));
      const evaluation = $("documents-evaluation"); evaluation.replaceChildren();
      if (data.report) {
        const report = data.report; evaluation.append(el("p", `${report.evaluations[0].queries.length} одинаковых вопросов. Попадание — чанк содержит заранее выбранную цитату из исходного документа.`));
        const wrap = el("div"); wrap.className = "monitor-table-wrap"; wrap.append(table(["Стратегия", `Hit@${report.k}`, `Recall@${report.k}`, `MRR@${report.k}`], report.evaluations.map(e => [labels[e.strategy], (e.hit_rate_at_k * 100).toFixed(1) + "%", (e.recall_at_k * 100).toFixed(1) + "%", e.mrr_at_k.toFixed(3)]))); evaluation.append(wrap);
        const details = el("details"); details.append(el("summary", "Вопросы и результаты"));
        for (let i = 0; i < report.evaluations[0].queries.length; i++) {
          const row = el("details"); row.append(el("summary", report.evaluations[0].queries[i].query));
          for (const e of report.evaluations) { const q = e.queries[i]; row.append(el("h3", `${labels[e.strategy]}: ${q.hit ? "найдено" : "цитата не найдена"}`)); q.matches.forEach((m, j) => row.append(chunkCard(m.chunk, m.score, j + 1))); } details.append(row);
        } evaluation.append(details);
      } else evaluation.append(el("p", "Сравнение ещё не выполнено. Запустите doc-index compare."));
      offset = 0; await chunks();
    } catch (e) { $("documents-status").textContent = e.message; $("documents-content").hidden = true; }
    finally { loading = false; $("documents-refresh").disabled = false; }
  }
  $("documents-search").addEventListener("submit", async event => {
    event.preventDefault(); if (searching) return; searching = true; $("documents-search-button").disabled = true;
    $("documents-search-status").textContent = "Получение эмбеддинга и поиск…"; $("documents-results").replaceChildren();
    try {
      const data = await request("/api/documents/search", { query: event.currentTarget.elements.query.value });
      for (const strategy of ["fixed", "structured"]) { const column = el("section"); column.append(el("h3", labels[strategy])); data.results[strategy].forEach((m, i) => column.append(chunkCard(m.chunk, m.score, i + 1))); $("documents-results").append(column); }
      $("documents-search-status").textContent = `Результаты для «${data.query}». Разверните фрагмент, чтобы прочитать текст.`;
    } catch (e) { $("documents-search-status").textContent = e.message; }
    finally { searching = false; $("documents-search-button").disabled = false; }
  });
  $("documents-refresh").addEventListener("click", load);
  $("documents-strategy").addEventListener("change", () => { offset = 0; chunks(); });
  $("documents-prev").addEventListener("click", () => { offset = Math.max(0, offset - 20); chunks(); });
  $("documents-next").addEventListener("click", () => { offset += 20; chunks(); });
  window.CodexDocuments = Object.freeze({ load });
})();
