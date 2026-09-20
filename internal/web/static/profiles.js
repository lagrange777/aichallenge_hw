(() => {
  "use strict";
  const form = document.querySelector("#profile-form");
  const controls = document.querySelector("#profiles-controls");
  const status = document.querySelector("#profiles-status");
  const indicator = document.querySelector("#active-profile");
  const fields = ["name", "description", "language", "level", "style", "detail", "format", "constraints"];
  const labels = {
    language: { auto: "По запросу и памяти", ru: "Русский", en: "Английский" },
    level: { auto: "По контексту", beginner: "Новичок", intermediate: "Уверенный", expert: "Эксперт" },
    style: { neutral: "Нейтральный", friendly: "Дружелюбный", business: "Деловой" },
    detail: { brief: "Кратко", normal: "Обычно", detailed: "Подробно" },
    format: { auto: "По запросу", list: "Список", steps: "Пошагово" }
  };
  let state = null;
  let editing = null;
  let busy = false;
  let chatBusy = false;
  let memoryBusy = false;
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
  function syncBusy() {
    controls.disabled = !state || busy || chatBusy || memoryBusy;
    document.querySelector("#new-profile").disabled = controls.disabled;
    indicator.disabled = !state;
  }
  for (const type of ["chat", "memory"]) window.addEventListener(`codex:${type}-busy`, event => {
    if (type === "chat") chatBusy = event.detail; else memoryBusy = event.detail;
    syncBusy();
  });
  function edit(p) {
    editing = { ...p };
    for (const field of fields) form.elements.namedItem(field).value = p[field] || "";
    document.querySelector("#profile-form-title").textContent = p.id ? `Профиль «${p.name}»` : "Новый профиль";
    document.querySelector("#save-profile").textContent = p.id ? "Сохранить изменения" : "Создать профиль";
  }
  function fresh() {
    edit({ name: "", description: "", language: "auto", level: "auto", style: "neutral", detail: "normal", format: "auto", constraints: "" });
    form.elements.namedItem("name").focus();
  }
  function render() {
    const active = state.profiles.find(p => p.id === state.activeId);
    indicator.textContent = `Профиль: ${active.name}`;
    indicator.title = `Активный профиль: ${active.name}`;
    const list = document.querySelector("#profiles-list"); list.replaceChildren();
    for (const p of state.profiles) {
      const card = element("article", undefined, "task-list-item profile-card");
      const info = element("div");
      info.append(element("strong", p.name), element("p", [labels.language[p.language], labels.detail[p.detail], labels.style[p.style]].join(" · ")));
      const actions = element("div", undefined, "memory-actions");
      if (p.id === state.activeId) { card.classList.add("is-current"); actions.append(element("span", "Активный", "task-badge")); }
      else {
        const use = button("Использовать", () => mutate("switch", { id: p.id }));
        use.setAttribute("aria-label", `Использовать профиль «${p.name}»`); actions.append(use);
      }
      const change = button("Настроить", () => { edit(p); form.scrollIntoView({ block: "start", behavior: "smooth" }); });
      change.setAttribute("aria-label", `Настроить профиль «${p.name}»`);
      actions.append(change); card.append(info, actions); list.append(card);
    }
    const presets = document.querySelector("#profile-presets"); presets.replaceChildren();
    for (const p of state.presets) presets.append(button(p.name, () => { edit({ ...p, id: "" }); form.scrollIntoView({ block: "start", behavior: "smooth" }); }));
    syncBusy();
  }
  async function load() {
    try {
      const response = await fetch("/api/profiles", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Профили недоступны");
      state = payload.profiles; render(); edit(state.profiles.find(p => p.id === state.activeId));
      window.CodexMemory.apply(state);
      status.textContent = "Настройки профиля действуют во всех его задачах, включая первый ответ нового чата.";
    } catch (error) { status.textContent = error.message; }
    syncBusy();
  }
  async function mutate(action, body) {
    if (!state || busy || chatBusy || memoryBusy) return;
    busy = true; syncBusy();
    window.dispatchEvent(new CustomEvent("codex:profile-busy", { detail: true }));
    status.textContent = "Сохраняем…";
    try {
      const response = await fetch(`/api/profiles/${action}`, {
        method: "POST", headers: { "Content-Type": "application/json", "X-Codex-Chat": "1" },
        body: JSON.stringify({ activeId: state.activeId, ...body })
      });
      const payload = await response.json();
      if (!response.ok) throw new Error(payload.error || "Не удалось сохранить профиль");
      state = payload.profiles; render();
      const selected = action === "new" ? state.profiles[state.profiles.length - 1] : state.profiles.find(p => p.id === (action === "edit" ? body.profile.id : state.activeId));
      edit(selected);
      if (action === "switch") {
        window.CodexMemory.apply(state);
        window.dispatchEvent(new CustomEvent("codex:profile-changed", { detail: state }));
      }
      status.textContent = action === "new" ? "Профиль создан. Нажмите «Использовать», чтобы начать работу с ним." : action === "switch" ? `Активен профиль «${selected.name}». Его задачи и память восстановлены.` : "Профиль сохранён. Новые настройки будут учтены со следующего ответа.";
    } catch (error) { status.textContent = error.message; }
    finally { busy = false; syncBusy(); window.dispatchEvent(new CustomEvent("codex:profile-busy", { detail: false })); }
  }
  form.addEventListener("submit", event => {
    event.preventDefault();
    const p = { ...editing };
    for (const field of fields) p[field] = form.elements.namedItem(field).value.trim();
    mutate(p.id ? "edit" : "new", { profile: p });
  });
  document.querySelector("#new-profile").addEventListener("click", fresh);
  document.querySelector("#cancel-profile").addEventListener("click", () => {
    const saved = state.profiles.find(p => p.id === editing.id);
    if (saved) edit(saved); else fresh();
  });
  function snapshot(p) {
    const details = element("details", undefined, "profile-snapshot");
    details.append(element("summary", `Профиль: ${p.name} · ${labels.language[p.language]} · ${labels.detail[p.detail]} · ${labels.format[p.format]}`));
    details.append(element("p", "Настройки, переданные модели для этого ответа. Явная просьба и параметры текущего сообщения имеют приоритет."));
    const list = element("dl");
    const names = { description: "О пользователе", language: "Язык", level: "Уровень", style: "Стиль", detail: "Подробность", format: "Формат", constraints: "Ограничения" };
    for (const [key, name] of Object.entries(names)) if (p[key]) list.append(element("dt", name), element("dd", labels[key] ? labels[key][p[key]] : p[key]));
    details.append(list); return details;
  }
  window.CodexProfiles = Object.freeze({ load, activeID: () => state ? state.activeId : "", snapshot });
})();
