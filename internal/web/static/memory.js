(() => {
  "use strict";
  const dialog = document.querySelector("#memory-dialog");
  const open = document.querySelector("#open-memory");
  const controls = document.querySelector("#memory-controls");
  const status = document.querySelector("#memory-status");
  const tasksControls = document.querySelector("#tasks-controls");
  const tasksStatus = document.querySelector("#tasks-status");
  const saveDialog = document.querySelector("#save-message-dialog");
  const saveForm = document.querySelector("#save-message-form");
  const saveLayer = document.querySelector("#save-message-layer");
  const saveKey = document.querySelector("#save-message-key");
  const saveValue = document.querySelector("#save-message-value");
  const saveStatus = document.querySelector("#save-message-status");
  const memoryTabs = Array.from(document.querySelectorAll(".memory-tab"));
  const search = document.querySelector("#memory-search");
  let activeMemoryTab = "short";
  let messages = [];
  let selectedMessage = null;
  let selectedTask = "";
  let state = null;
  let busy = false;
  let chatBusy = false;
  let profileBusy = false;
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
  function selectMemoryTab(name, focus = false) {
    activeMemoryTab = name;
    for (const tab of memoryTabs) {
      const selected = tab.id === `memory-tab-${name}`;
      tab.setAttribute("aria-selected", String(selected));
      tab.tabIndex = selected ? 0 : -1;
      document.getElementById(tab.getAttribute("aria-controls")).hidden = !selected;
      if (selected && focus) { tab.focus(); tab.scrollIntoView({ block: "nearest", inline: "nearest" }); }
    }
    document.querySelector(".memory-body").scrollTop = 0;
    filterMemory();
  }
  function filterMemory() {
    const query = search.value.trim().toLocaleLowerCase("ru-RU");
    const panel = document.querySelector(`#memory-panel-${activeMemoryTab}`);
    const cards = Array.from(panel.querySelectorAll("[data-memory-search]"));
    for (const card of cards) card.hidden = !card.dataset.memorySearch.includes(query);
    document.querySelector("#memory-search-empty").hidden = !query || !cards.length || cards.some(card => !card.hidden);
  }
  memoryTabs.forEach((tab, index) => {
    tab.addEventListener("click", () => selectMemoryTab(tab.id.replace("memory-tab-", "")));
    tab.addEventListener("keydown", event => {
      if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      const next = event.key === "Home" ? 0 : event.key === "End" ? memoryTabs.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + memoryTabs.length) % memoryTabs.length;
      selectMemoryTab(memoryTabs[next].id.replace("memory-tab-", ""), true);
    });
  });
  search.addEventListener("input", filterMemory);
  function syncBusy() {
    window.CodexTaskState.setBusy(!state || busy || chatBusy || profileBusy);
    controls.disabled = !state || busy || chatBusy || profileBusy;
    tasksControls.disabled = !state || busy || chatBusy || profileBusy;
    saveForm.querySelectorAll("input, textarea, select, button").forEach(element => { element.disabled = busy || chatBusy || profileBusy; });
    document.querySelectorAll(".message-memory-action").forEach(element => {
      const entries = state ? (element.dataset.layer === "working" ? state.working : state.longTerm) : [];
      const saved = (entries || []).some(entry => entry.messageId === element.dataset.messageId);
      element.textContent = element.dataset.layer === "working"
        ? (saved ? "✓ В рабочей памяти" : "В рабочую память")
        : (saved ? "✓ В долговременной памяти" : "В долговременную память");
      element.classList.toggle("saved", saved);
      element.disabled = !state || busy || chatBusy || profileBusy || !element.dataset.messageId;
      element.title = !element.dataset.messageId ? "Доступно после успешного ответа" : saved ? "Открыть сохранённую запись" : "Выбрать, что запомнить из сообщения";
    });
    if (chatBusy) setStatus("Агент отвечает. Изменения будут доступны после ответа.");
  }
  function setStatus(text) {
    status.textContent = text;
    tasksStatus.textContent = text;
  }
  async function load() {
    const version = ++loadVersion;
    open.disabled = false;
    try {
      const response = await fetch("/api/memory", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Память недоступна");
      if (version !== loadVersion || busy || profileBusy) return;
      state = payload.memory;
      messages = payload.messages || [];
      render();
      if (!chatBusy) {
        status.textContent = "Изменения памяти учитываются со следующего ответа. Удаление записи не удаляет её упоминания из диалога.";
        tasksStatus.textContent = "";
      }
    } catch (error) {
      if (version === loadVersion) setStatus(error.message);
    }
  }
  async function mutate(path, body) {
    if (!state || busy || chatBusy || profileBusy) return;
    const previousTaskId = state.task.id;
    busy = true;
    loadVersion++;
    syncBusy();
    window.dispatchEvent(new CustomEvent("codex:memory-busy", { detail: true }));
    setStatus("Сохраняем…");
    if (path === "/api/tasks/state") window.CodexTaskState.setStatus("Сохраняем…");
    try {
      const response = await fetch(path, {
        method: "POST",
        headers: { "Content-Type": "application/json", "X-Codex-Chat": "1" },
        body: JSON.stringify({ taskId: state.task.id, ...body })
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Не удалось сохранить память");
      state = payload.memory;
      messages = payload.messages || [];
      render();
      if (path === "/api/tasks/state") window.CodexTaskState.setStatus(state.task.workflow.paused ? "Задача на паузе" : "Состояние задачи сохранено");
      setStatus("Сохранено. Изменения будут учтены в следующем ответе.");
      if (dialog.open && !saveDialog.open) document.querySelector(`#memory-tab-${activeMemoryTab}`).focus();
      if (path === "/api/tasks/new" || path === "/api/tasks/switch") {
        const created = path === "/api/tasks/new";
        tasksStatus.textContent = created ? "Новая задача создана. Предыдущая сохранена в списке." : `Задача «${state.task.name}» открыта. Перейдите в чат, чтобы продолжить.`;
        if (created) document.querySelector("#new-task-name").value = "";
        window.dispatchEvent(new CustomEvent("codex:task-changed", { detail: { ...payload, previousTaskId, created } }));
      }
      return payload;
    } catch (error) {
      setStatus(error.message);
      if (path === "/api/tasks/state") window.CodexTaskState.setStatus(error.message);
      if (saveDialog.open) saveStatus.textContent = error.message;
      return null;
    } finally {
      busy = false;
      syncBusy();
      window.dispatchEvent(new CustomEvent("codex:memory-busy", { detail: false }));
    }
  }
  function savedMessageEntry() {
    if (!selectedMessage || !state) return null;
    const entries = saveLayer.value === "working" ? state.working : state.longTerm;
    return (entries || []).find(entry => entry.messageId === selectedMessage.id);
  }
  function fillMessageForm() {
    const existing = savedMessageEntry();
    saveKey.value = existing ? existing.key : Array.from(selectedMessage.text.trim().split("\n")[0]).slice(0, 70).join("");
    saveValue.value = existing ? existing.value : selectedMessage.text;
    document.querySelector("#save-message-submit").textContent = existing ? "Сохранить изменения" : "Сохранить в память";
    document.querySelector("#save-message-title").textContent = existing ? "Запись из сообщения" : "Запомнить из сообщения";
    updateMessageLength();
  }
  function updateMessageLength() {
    const length = Array.from(saveValue.value).length;
    saveStatus.textContent = length > 2000 ? `${length} символов. Выберите важный фрагмент — до 2000 символов.` : `${length} / 2000 символов`;
  }
  function openFromMessage(message, layer) {
    if (!state || busy || chatBusy || profileBusy || !message.id) return;
    selectedMessage = message;
    selectedTask = state.task.id;
    saveLayer.value = layer;
    fillMessageForm();
    saveDialog.showModal();
    saveValue.focus();
  }
  function messageActions(message) {
    const actions = node("div", undefined, "message-memory-actions");
    actions.setAttribute("aria-label", "Сохранение сообщения в память");
    for (const layer of ["working", "long_term"]) {
      const action = button(layer === "working" ? "В рабочую память" : "В долговременную память", () => openFromMessage(message, layer));
      action.classList.add("message-memory-action");
      action.dataset.messageId = message.id || "";
      action.dataset.layer = layer;
      action.disabled = !state || busy || chatBusy || profileBusy || !message.id;
      actions.append(action);
    }
    return actions;
  }
  function sourceDetails(item) {
    const source = node("details", undefined, "memory-source");
    source.append(node("summary", "Источник записи"), node("p", item.source || "Источник не указан"));
    return source;
  }
  function record(item, layer, badge) {
    const card = node("article", undefined, "memory-entry memory-record");
    card.dataset.memorySearch = `${item.key} ${item.value} ${item.source || ""}`.toLocaleLowerCase("ru-RU");
    const meta = node("div", undefined, "memory-record-meta");
    meta.append(node("span", badge || labels[layer], `memory-label memory-label-${layer}`));
    const date = item.reviewedAt || item.updatedAt || item.createdAt;
    if (date) meta.append(node("time", new Date(date).toLocaleString("ru-RU")));
    card.append(meta, node("h4", item.key), node("p", item.value, "memory-record-value"));
    return card;
  }
  function editor(item, layer, proposal) {
    const card = record(item, layer, proposal ? `На подтверждении · ${labels[layer]}` : labels[layer]);
    if (proposal) card.append(node("p", item.reason, "memory-reason"));
    card.append(sourceDetails(item));
    const edit = node("details", undefined, "memory-edit");
    edit.append(node("summary", proposal ? "Изменить текст или слой" : "Редактировать запись"));
    const key = node("input");
    key.value = item.key;
    key.maxLength = 100;
    const value = node("textarea");
    value.value = item.value;
    value.rows = 4;
    value.maxLength = 2000;
    const destination = node("select");
    for (const [id, label] of Object.entries(labels)) {
      const option = node("option", label);
      option.value = id;
      destination.append(option);
    }
    destination.value = layer;
    if (proposal) edit.append(field("Сохранить в слой", destination));
    edit.append(field("Название", key), field("Что запомнить", value));
    if (!proposal) {
      const actions = node("div", undefined, "memory-actions");
      actions.append(
        button("Сохранить", () => mutate("/api/memory/edit", { id: item.id, layer, key: key.value, value: value.value })),
        button("Отмена", () => { key.value = item.key; value.value = item.value; edit.open = false; }),
        button("Удалить запись", () => mutate("/api/memory/delete", { id: item.id, layer }))
      );
      edit.append(actions);
    }
    card.append(edit);
    if (proposal) {
      const actions = node("div", undefined, "memory-actions");
      const accept = button("Подтвердить", () => mutate("/api/memory/review", { id: item.id, action: "accept", layer: destination.value, key: key.value, value: value.value }));
      accept.classList.add("memory-primary");
      actions.append(accept, button("Отклонить", () => mutate("/api/memory/review", { id: item.id, action: "reject" })));
      card.append(actions);
    }
    return card;
  }
  function render() {
    if (!state) return;
    window.CodexTaskState.render(state, mutate);
    window.CodexInvariants.render(state, mutate);
    const tasksList = document.querySelector("#tasks-list");
    tasksList.replaceChildren();
    for (const task of state.tasks || [state.task]) {
      const current = task.id === state.task.id;
      const item = node("div", undefined, "task-list-item");
      item.append(node("span", task.name, "task-list-name"));
      item.append(node("span", `${window.CodexTaskState.stages[task.workflow.stage]}${task.workflow.paused ? " · Пауза" : ""}`, "task-badge"));
      if (current) {
        item.classList.add("is-current");
        item.append(node("span", "Текущая", "task-badge"));
      } else {
        const action = button("Открыть", () => mutate("/api/tasks/switch", { id: task.id }));
        action.setAttribute("aria-label", `Открыть задачу «${task.name}»`);
        item.append(action);
      }
      tasksList.append(item);
    }
    document.querySelector("#short-memory-count").textContent = `Сообщений в диалоге: ${state.shortTermMessages} · В активной истории: ${state.contextMessages}`;
    document.querySelector("#memory-scope").textContent = `Текущая задача: ${state.task.name}`;
    document.querySelector("#memory-scope").title = state.task.name;
    document.querySelector("#tasks-current-name").textContent = state.task.name;
    document.querySelector("#tasks-summary").textContent = `Записей в рабочей памяти: ${(state.working || []).length} · Сообщений в текущем чате: ${state.shortTermMessages}`;
    for (const [selector, entries, layer] of [["#working-memory-list", state.working, "working"], ["#memory-working-list", state.working, "working"], ["#long-memory-list", state.longTerm, "long_term"]]) {
      const list = document.querySelector(selector);
      list.replaceChildren();
      for (const entry of entries || []) list.append(editor(entry, layer, false));
      if (!list.childElementCount) list.append(node("p", "Пока нет записей. Сохраните важный фрагмент из сообщения или подтвердите предложение агента.", "memory-empty"));
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
    const reviewed = proposals.filter(item => item.status !== "pending").reverse();
    reviewed.forEach(item => {
      const card = record(item, item.layer, `${item.status === "accepted" ? "Подтверждено" : "Отклонено"} · ${labels[item.layer]}`);
      card.classList.add(`memory-decision-${item.status}`);
      card.append(sourceDetails(item));
      journal.append(card);
    });
    if (!journal.childElementCount) journal.append(node("p", "История пока пуста. Здесь появятся ваши решения по предложениям агента.", "memory-empty"));
    const shortList = document.querySelector("#short-memory-list");
    shortList.replaceChildren();
    for (const message of messages) {
      const card = node("article", undefined, "memory-entry memory-record memory-message");
      card.dataset.memorySearch = message.text.toLocaleLowerCase("ru-RU");
      const meta = node("div", undefined, "memory-record-meta");
      meta.append(node("strong", message.role === "user" ? "Вы" : "Агент"), node("time", new Date(message.time).toLocaleString("ru-RU")));
      card.append(meta, node("p", message.text, "memory-record-value"));
      shortList.append(card);
    }
    if (!messages.length) shortList.append(node("p", "Диалог пока пуст. Отправьте сообщение агенту — оно появится здесь.", "memory-empty"));
    const counts = { short: state.shortTermMessages, working: (state.working || []).length, long: (state.longTerm || []).length, proposals: pending.length, journal: reviewed.length };
    for (const [name, count] of Object.entries(counts)) document.querySelector(`#memory-count-${name}`).textContent = count;
    filterMemory();
    syncBusy();
  }
  open.addEventListener("click", () => { dialog.showModal(); document.querySelector(`#memory-tab-${activeMemoryTab}`).focus(); load(); });
  document.querySelector("#close-memory").addEventListener("click", () => dialog.close());
  document.querySelector("#memory-open-tasks").addEventListener("click", () => {
    dialog.close();
    window.dispatchEvent(new CustomEvent("codex:open-tasks"));
  });
  document.querySelector("#new-task-form").addEventListener("submit", event => {
    event.preventDefault();
    mutate("/api/tasks/new", { name: document.querySelector("#new-task-name").value });
  });
  document.querySelector("#cancel-message-memory").addEventListener("click", () => saveDialog.close());
  saveDialog.addEventListener("cancel", event => { if (busy) event.preventDefault(); });
  saveLayer.addEventListener("change", () => {
    if (savedMessageEntry()) { fillMessageForm(); return; }
    document.querySelector("#save-message-submit").textContent = "Сохранить в память";
    document.querySelector("#save-message-title").textContent = "Запомнить из сообщения";
  });
  saveValue.addEventListener("input", updateMessageLength);
  saveForm.addEventListener("submit", async event => {
    event.preventDefault();
    if (!selectedMessage || busy || chatBusy || profileBusy) return;
    if (!saveKey.value.trim() || !saveValue.value.trim() || Array.from(saveValue.value.trim()).length > 2000) {
      saveStatus.textContent = "Укажите название и текст записи (до 2000 символов).";
      return;
    }
    const existing = savedMessageEntry();
    const payload = await mutate(existing ? "/api/memory/edit" : "/api/memory/from-message", {
      taskId: selectedTask, messageId: selectedMessage.id, id: existing ? existing.id : "",
      layer: saveLayer.value, key: saveKey.value, value: saveValue.value
    });
    if (payload) {
      saveDialog.close();
      window.dispatchEvent(new CustomEvent("codex:memory-saved", { detail: saveLayer.value }));
    }
  });
  window.addEventListener("codex:profile-busy", event => { profileBusy = event.detail; loadVersion++; syncBusy(); });
  window.CodexMemory = Object.freeze({
    apply(payload) { loadVersion++; state = payload.memory; messages = payload.messages || []; render(); },
    load,
    messageActions,
    setChatBusy(value) { chatBusy = value; syncBusy(); window.dispatchEvent(new CustomEvent("codex:chat-busy", { detail: value })); }
  });
})();
