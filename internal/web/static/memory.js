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
  let selectedMessage = null;
  let selectedTask = "";
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
    tasksControls.disabled = !state || busy || chatBusy;
    saveForm.querySelectorAll("input, textarea, select, button").forEach(element => { element.disabled = busy || chatBusy; });
    document.querySelectorAll(".message-memory-action").forEach(element => {
      const entries = state ? (element.dataset.layer === "working" ? state.working : state.longTerm) : [];
      const saved = (entries || []).some(entry => entry.messageId === element.dataset.messageId);
      element.textContent = element.dataset.layer === "working"
        ? (saved ? "✓ В рабочей памяти" : "В рабочую память")
        : (saved ? "✓ В долговременной памяти" : "В долговременную память");
      element.classList.toggle("saved", saved);
      element.disabled = !state || busy || chatBusy || !element.dataset.messageId;
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
      if (version !== loadVersion || busy) return;
      state = payload.memory;
      render();
      if (!chatBusy) {
        status.textContent = "Подтверждённые записи используются со следующего сообщения. Удаление записи не удаляет её упоминания из переписки.";
        tasksStatus.textContent = "";
      }
    } catch (error) {
      if (version === loadVersion) setStatus(error.message);
    }
  }
  async function mutate(path, body) {
    if (!state || busy || chatBusy) return;
    busy = true;
    loadVersion++;
    syncBusy();
    window.dispatchEvent(new CustomEvent("codex:memory-busy", { detail: true }));
    setStatus("Сохраняем…");
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
      setStatus("Сохранено. Изменения будут учтены в следующем ответе.");
      if (path === "/api/tasks/new") {
        tasksStatus.textContent = "Новая задача создана. Перейдите в чат, чтобы начать работу.";
        document.querySelector("#new-task-name").value = "";
        window.dispatchEvent(new CustomEvent("codex:new-task", { detail: payload }));
      }
      return payload;
    } catch (error) {
      setStatus(error.message);
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
    if (!state || busy || chatBusy || !message.id) return;
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
      action.disabled = !state || busy || chatBusy || !message.id;
      actions.append(action);
    }
    return actions;
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
    document.querySelector("#tasks-current-name").textContent = state.task.name;
    document.querySelector("#tasks-summary").textContent = `Записей в рабочей памяти: ${(state.working || []).length} · Сообщений в текущем чате: ${state.shortTermMessages}`;
    document.querySelector("#working-memory-summary").textContent = `Подтверждённых записей: ${(state.working || []).length}. Управление — на вкладке «Задачи».`;
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
    if (!selectedMessage || busy || chatBusy) return;
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
  window.CodexMemory = Object.freeze({
    load,
    messageActions,
    setChatBusy(value) { chatBusy = value; syncBusy(); }
  });
})();
