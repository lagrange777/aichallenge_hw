(() => {
  "use strict";
  const dialog = document.querySelector("#memory-dialog");
  const open = document.querySelector("#open-memory");
  const controls = document.querySelector("#memory-controls");
  const status = document.querySelector("#memory-status");
  let state = null;
  let busy = false;
  let chatBusy = false;
  let loadVersion = 0;
  const labels = { working: "Рабочая", long_term: "Долговременная" };

  function node(tag, text, className) {
    const element = document.createElement(tag);
    if (text !== undefined) element.textContent = text;
    if (className) element.className = className;
    return element;
  }
  function field(label, element) {
    const wrapper = node("label", label);
    wrapper.append(element);
    return wrapper;
  }
  function button(text, callback) {
    const element = node("button", text, "memory-button");
    element.type = "button";
    element.addEventListener("click", callback);
    return element;
  }
  function syncBusy() {
    controls.disabled = !state || busy || chatBusy;
    if (chatBusy) status.textContent = "Агент отвечает. Изменение памяти будет доступно после ответа.";
  }
  async function load() {
    const version = ++loadVersion;
    open.disabled = false;
    try {
      const response = await fetch("/api/memory", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Память недоступна");
      if (version !== loadVersion || busy) return;
      state = payload.memory;
      render();
      if (!chatBusy) status.textContent = "Подтверждённые записи используются со следующего сообщения. Удаление записи не удаляет её упоминания из переписки.";
    } catch (error) {
      if (version === loadVersion) status.textContent = error.message;
    }
  }
  async function mutate(path, body) {
    if (!state || busy || chatBusy) return;
    busy = true;
    loadVersion++;
    syncBusy();
    window.dispatchEvent(new CustomEvent("codex:memory-busy", { detail: true }));
    status.textContent = "Сохраняем…";
    try {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Codex-Chat": "1" },
        body: JSON.stringify({ taskId: state.task.id, ...body })
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Не удалось сохранить память");
      state = payload.memory;
      render();
      status.textContent = "Сохранено. Изменения будут учтены в следующем ответе.";
      if (path === "/api/tasks/new") {
        document.querySelector("#new-task-name").value = "";
        window.dispatchEvent(new CustomEvent("codex:new-task", { detail: payload }));
      }
    } catch (error) {
      status.textContent = error.message;
    } finally {
      busy = false;
      syncBusy();
      window.dispatchEvent(new CustomEvent("codex:memory-busy", { detail: false }));
    }
  }
  function editor(item, layer, proposal) {
    const card = node("article", undefined, "memory-entry");
    const key = node("input");
    key.value = item.key;
    key.maxLength = 100;
    const value = node("textarea");
    value.value = item.value;
    value.rows = 3;
    value.maxLength = 2000;
    const destination = node("select");
    for (const [id, label] of Object.entries(labels)) {
      const option = node("option", label);
      option.value = id;
      destination.append(option);
    }
    destination.value = layer;
    if (proposal) {
      card.append(field("Сохранить в слой", destination), node("p", item.reason, "memory-reason"));
    }
    card.append(field("Ключ", key), field("Значение", value));
    const source = node("details");
    source.append(node("summary", "Источник и время"), node("p", item.source));
    source.append(node("small", new Date(item.createdAt || item.updatedAt).toLocaleString("ru-RU")));
    card.append(source);
    const actions = node("div", undefined, "memory-actions");
    if (proposal) {
      actions.append(
        button("Подтвердить", () => mutate("/api/memory/review", { id: item.id, action: "accept", layer: destination.value, key: key.value, value: value.value })),
        button("Отклонить", () => mutate("/api/memory/review", { id: item.id, action: "reject" }))
      );
    } else {
      actions.append(
        button("Сохранить правку", () => mutate("/api/memory/edit", { id: item.id, layer, key: key.value, value: value.value })),
        button("Удалить", () => mutate("/api/memory/delete", { id: item.id, layer }))
      );
    }
    card.append(actions);
    return card;
  }
  function render() {
    if (!state) return;
    document.querySelector("#short-memory-count").textContent = `${state.shortTermMessages} сообщений в диалоге · ${state.contextMessages} в активной истории`;
    document.querySelector("#memory-task-name").textContent = state.task.name;
    for (const [selector, entries, layer] of [["#working-memory-list", state.working, "working"], ["#long-memory-list", state.longTerm, "long_term"]]) {
      const list = document.querySelector(selector);
      list.replaceChildren();
      for (const entry of entries || []) list.append(editor(entry, layer, false));
      if (!list.childElementCount) list.append(node("p", "Нет подтверждённых записей", "memory-empty"));
    }
    const proposals = state.proposals || [];
    const pending = proposals.filter(item => item.status === "pending");
    document.querySelector("#memory-pending-count").textContent = pending.length ? `(${pending.length})` : "";
    const list = document.querySelector("#memory-proposals-list");
    list.replaceChildren();
    pending.forEach(item => list.append(editor(item, item.layer, true)));
    if (!pending.length) list.append(node("p", "После ответа агент предложит важные сведения для сохранения. Новых предложений пока нет.", "memory-empty"));
    const journal = document.querySelector("#memory-journal-list");
    journal.replaceChildren();
    proposals.filter(item => item.status !== "pending").reverse().forEach(item => {
      journal.append(node("p", `${item.status === "accepted" ? "Подтверждено" : "Отклонено"} · ${labels[item.layer]} · ${item.key}: ${item.value}`));
    });
    if (!journal.childElementCount) journal.append(node("p", "Решений пока нет"));
    syncBusy();
  }
  open.addEventListener("click", () => { dialog.showModal(); load(); });
  document.querySelector("#close-memory").addEventListener("click", () => dialog.close());
  document.querySelector("#new-task-form").addEventListener("submit", event => {
    event.preventDefault();
    mutate("/api/tasks/new", { name: document.querySelector("#new-task-name").value });
  });
  window.CodexMemory = Object.freeze({
    load,
    setChatBusy(value) { chatBusy = value; syncBusy(); }
  });
})();
