(() => {
  "use strict";
  const form = document.querySelector("#mcp-form");
  const controls = document.querySelector("#mcp-controls");
  const status = document.querySelector("#mcp-status");
  const result = document.querySelector("#mcp-result");
  const search = document.querySelector("#mcp-search");
  const field = name => form.elements.namedItem(name);
  let connections = [];
  let active = "new";
  const drafts = new Map();
  const checks = new Map();
  const messages = new Map();
  const tabs = document.querySelector("#mcp-server-tabs");
  const panel = document.querySelector("#mcp-server-panel");
  const current = () => connections.find(c => c.id === active);
  let discovery = null;
  let busy = false;
  let loaded = false;
  const element = (tag, text, className) => {
    const node = document.createElement(tag);
    if (text !== undefined) node.textContent = text;
    if (className) node.className = className;
    return node;
  };
  const button = (text, action) => {
    const node = element("button", text, "memory-button");
    node.type = "button"; node.addEventListener("click", action); return node;
  };
  function values() {
    return { name: field("name").value, url: field("url").value,
      token: field("token").value, clearToken: field("clearToken").checked };
  }
  function remember() {
    drafts.set(active, values());
    const check = checks.get(active);
    if (check) check.query = search.value;
  }
  function syncActions() {
    const c = current();
    const draft = values();
    const dirty = c && (draft.name.trim() !== c.name || draft.url.trim() !== c.url || draft.token.trim() || draft.clearToken);
    document.querySelector("#mcp-check").disabled = busy || !c || !!dirty;
    document.querySelector("#mcp-save-hint").hidden = !dirty;
  }
  function showActive() {
    const c = current();
    const draft = drafts.get(active) || { name: c?.name || "", url: c?.url || "", token: "", clearToken: false };
    form.reset();
    for (const key of ["name", "url", "token"]) field(key).value = draft[key];
    field("clearToken").checked = draft.clearToken;
    document.querySelector("#mcp-form-title").textContent = c ? `Настройки: ${c.name}` : "Новое подключение";
    field("token").placeholder = c?.hasToken ? "Ключ сохранён. Оставьте пустым, чтобы сохранить его" : "Для серверов без авторизации оставьте пустым";
    document.querySelector("#mcp-presets").hidden = !!c;
    document.querySelector("#mcp-server-actions").hidden = !c;
    const check = checks.get(active);
    discovery = check?.discovery || null;
    result.hidden = !discovery;
    search.value = check?.query || "";
    if (discovery) {
      document.querySelector("#mcp-result-title").textContent = `Инструменты: ${c.name}`;
      document.querySelector("#mcp-server-info").textContent = `${discovery.serverName} ${discovery.serverVersion} · MCP ${discovery.protocolVersion} · ${discovery.tools.length} инструментов · проверено ${check.time}`;
    }
    renderTools();
    status.textContent = messages.get(active) || (c ? "Настройте сервер или проверьте подключение." : "Добавьте Neurly или укажите адрес другого MCP-сервера.");
    renderTabs(); syncActions();
  }
  function select(id, focus = false) {
    if (busy) return;
    remember(); active = id; showActive();
    if (focus) document.getElementById(`mcp-server-tab-${active}`).focus();
  }
  function renderTabs() {
    tabs.replaceChildren();
    const entries = [...connections, { id: "new", name: "+ Добавить сервер" }];
    entries.forEach((c, index) => {
      const tab = button(c.name, () => select(c.id, true));
      tab.id = `mcp-server-tab-${c.id}`;
      tab.setAttribute("role", "tab");
      tab.setAttribute("aria-controls", "mcp-server-panel");
      tab.setAttribute("aria-selected", String(c.id === active));
      tab.tabIndex = c.id === active ? 0 : -1;
      tab.addEventListener("keydown", event => {
        if (!["ArrowLeft", "ArrowRight", "Home", "End"].includes(event.key)) return;
        event.preventDefault();
        const next = event.key === "Home" ? 0 : event.key === "End" ? entries.length - 1 : (index + (event.key === "ArrowRight" ? 1 : -1) + entries.length) % entries.length;
        select(entries[next].id, true);
      });
      tabs.append(tab);
    });
    panel.setAttribute("aria-labelledby", `mcp-server-tab-${active}`);
  }
  function applyConnections(next) {
    for (const c of connections) {
      if (!next.some(item => item.id === c.id && item.version === c.version)) {
        drafts.delete(c.id); checks.delete(c.id); messages.delete(c.id);
      }
    }
    connections = next;
  }
  function renderTools() {
    const list = document.querySelector("#mcp-tools"); list.replaceChildren();
    if (!discovery) return;
    const query = search.value.trim().toLowerCase();
    const tools = discovery.tools.filter(tool => `${tool.name} ${tool.description || ""}`.toLowerCase().includes(query));
    if (!tools.length) list.append(element("p", discovery.tools.length ? "По запросу ничего не найдено." : "Сервер доступен, но список инструментов пуст. Проверьте включённые интеграции в кабинете сервиса."));
    for (const tool of tools) {
      const card = element("article", undefined, "mcp-tool");
      card.append(element("h3", tool.name), element("p", tool.description || "Описание не предоставлено"));
      const schema = element("details");
      schema.append(element("summary", "Параметры инструмента"), element("pre", JSON.stringify(tool.inputSchema, null, 2)));
      card.append(schema); list.append(card);
    }
  }
  async function request(path, body) {
    const response = await fetch(path, {
      method: body ? "POST" : "GET",
      headers: { "Content-Type": "application/json", "X-Codex-Chat": "1" },
      ...(body ? { body: JSON.stringify(body) } : {})
    });
    const payload = await response.json();
    if (!response.ok) throw new Error(payload.error || "MCP недоступен");
    return payload.mcp;
  }
  async function load(force = false) {
    if (busy || (loaded && !force)) return;
    if (loaded) remember();
    busy = true; controls.disabled = true; status.textContent = "Загружаем подключения…";
    try {
      const data = await request("/api/mcp");
      applyConnections(data.connections);
      if (!loaded || (active !== "new" && !current())) active = connections[0]?.id || "new";
      loaded = true; showActive();
    } catch (error) { status.textContent = error.message; }
    finally { busy = false; controls.disabled = false; syncActions(); }
  }
  async function mutate(action, body) {
    if (busy) return;
    remember();
    const id = active;
    const connection = current();
    const previousIDs = new Set(connections.map(c => c.id));
    busy = true; controls.disabled = true;
    if (action === "check") { checks.delete(id); discovery = null; result.hidden = true; }
    status.textContent = action === "check" ? `Подключаемся к ${connection.name} и получаем инструменты…` : "Сохраняем…";
    try {
      const data = await request(`/api/mcp/${action}`, body);
      applyConnections(data.connections); loaded = true;
      if (action === "check") {
        checks.set(id, { discovery: data.discovery, time: new Date().toLocaleTimeString(), query: "" });
        messages.set(id, `Соединение установлено. Получено инструментов: ${data.discovery.tools.length}.`);
      } else if (action === "save") {
        drafts.delete(id); checks.delete(id);
        if (id === "new") active = connections.find(c => !previousIDs.has(c.id))?.id || "new";
        messages.set(active, "Подключение сохранено. Можно проверить соединение и получить инструменты.");
      } else {
        drafts.delete(id); checks.delete(id); messages.delete(id);
        active = connections[0]?.id || "new";
      }
      showActive();
      if (action === "check") result.scrollIntoView({ block: "start", behavior: "smooth" });
    } catch (error) {
      messages.set(id, error.message); status.textContent = error.message;
    } finally {
      field("token").value = "";
      const draft = drafts.get(active); if (draft) draft.token = "";
      busy = false; controls.disabled = false; syncActions();
    }
  }
  form.addEventListener("input", syncActions);
  form.addEventListener("submit", event => {
    event.preventDefault();
    const c = current();
    mutate("save", { id: c?.id || "", version: c?.version || 0,
      name: field("name").value.trim(), url: field("url").value.trim(),
      token: field("token").value.trim(), clearToken: field("clearToken").checked });
  });
  document.querySelector("#mcp-neurly").addEventListener("click", () => {
    drafts.set("new", { name: "Neurly", url: "https://neurly.ru/v1/mcp", token: "", clearToken: false });
    showActive(); field("token").focus();
  });
  document.querySelector("#mcp-custom").addEventListener("click", () => { drafts.delete("new"); showActive(); field("name").focus(); });
  document.querySelector("#mcp-cancel").addEventListener("click", () => { drafts.delete(active); showActive(); });
  document.querySelector("#mcp-check").addEventListener("click", () => {
    const c = current(); if (c) mutate("check", { id: c.id, version: c.version });
  });
  document.querySelector("#mcp-delete").addEventListener("click", () => {
    const c = current();
    if (c && window.confirm(`Удалить подключение «${c.name}» и его сохранённый ключ?`)) mutate("delete", { id: c.id, version: c.version });
  });
  document.querySelector("#mcp-refresh").addEventListener("click", () => load(true));
  search.addEventListener("input", renderTools);
  window.CodexMCP = Object.freeze({ load });
})();
