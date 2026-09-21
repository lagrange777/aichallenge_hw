(() => {
  const categories = { architecture: "Архитектура", decision: "Техническое решение", stack: "Стек", business: "Бизнес-правило", other: "Другое" };
  const statuses = { active: "Активен", disabled: "Отключён", proposed: "На подтверждении", rejected: "Отклонён" };
  function node(tag, text, cls) { const el = document.createElement(tag); if (text !== undefined) el.textContent = text; if (cls) el.className = cls; return el; }
  function button(text, action) { const el = node("button", text, "memory-button"); el.type = "button"; el.addEventListener("click", action); return el; }
  function editor(rule, save) {
    const form = node("form", undefined, "invariant-form");
    const field = (name, label, type, value, options) => {
      const wrap = node("label", label); const input = node(type); input.name = name;
      if (options) for (const [key, text] of Object.entries(options)) { const opt = node("option", text); opt.value = key; input.append(opt); }
      input.value = value || ""; wrap.append(input); form.append(wrap); return input;
    };
    field("category", "Категория", "select", rule.category || "stack", categories);
    const title = field("title", "Название", "input", rule.title); title.required = true; title.maxLength = 100;
    const text = field("rule", "Правило", "textarea", rule.rule); text.required = true; text.maxLength = 2000; text.rows = 3;
    const reason = field("reason", "Обоснование", "textarea", rule.reason); reason.maxLength = 1000; reason.rows = 2;
    const submit = node("button", rule.id ? "Сохранить изменения" : "Добавить активный инвариант", "memory-button memory-primary"); submit.type = "submit"; form.append(submit);
    form.addEventListener("submit", event => { event.preventDefault(); save({ ...rule, ...Object.fromEntries(new FormData(form)) }); });
    return form;
  }
  function render(state, mutate) {
    const set = state.invariants;
    const command = (action, rule) => mutate("/api/tasks/invariants", { taskId: state.task.id, profileId: window.CodexProfiles.activeID(), version: set.version, action, rule });
    const list = document.querySelector("#invariants-list"); list.replaceChildren();
    for (const rule of set.items || []) {
      const card = node("article", undefined, "memory-entry invariant-card");
      card.append(node("span", `${statuses[rule.status]} · ${categories[rule.category]} · v${rule.version}`, "task-badge"), node("h3", rule.title), node("p", rule.rule));
      if (rule.reason) card.append(node("p", rule.reason, "memory-reason"));
      const actions = node("div", undefined, "memory-actions");
      if (rule.status === "active") actions.append(button("Отключить", () => command("disable", rule)));
      if (rule.status === "disabled" || rule.status === "proposed") actions.append(button(rule.status === "proposed" ? "Подтвердить инвариант" : "Активировать", () => command("activate", rule)));
      if (rule.status === "proposed") actions.append(button("Отклонить", () => command("reject", rule)));
      card.append(actions);
      if (rule.status !== "rejected") { const details = node("details", undefined, "memory-edit"); details.append(node("summary", "Редактировать"), editor(rule, updated => command("edit", updated))); card.append(details); }
      list.append(card);
    }
    if (!set.items?.length) list.append(node("p", "Правил пока нет. Добавьте ограничения или подтвердите предложение агента. Контроль текущего этапа задачи работает независимо от этого списка."));
    const log = node("details", undefined, "memory-edit"); log.append(node("summary", `История изменений (${set.events?.length || 0})`));
    const names = { create: "Добавлен", edit: "Изменён", activate: "Активирован", disable: "Отключён", reject: "Отклонён" };
    for (const event of [...(set.events || [])].reverse()) log.append(node("p", `${new Date(event.at).toLocaleString("ru-RU")} · ${names[event.action]} · ${event.rule.title} v${event.rule.version}: ${event.rule.rule}`));
    list.append(log);
    document.querySelector("#invariant-create-form").replaceChildren(editor({}, rule => command("create", rule)));
  }
  function snapshot(check) {
    const root = node("details", undefined, "profile-snapshot invariant-snapshot");
    const labels = { allow: "проверено", partial: "частичный конфликт", conflict: "конфликт запроса", clarify: "нужно уточнение", rules_conflict: "противоречие правил", unavailable: "проверка недоступна", blocked: "решение заблокировано" };
    root.append(node("summary", `Учтённые ограничения · ${labels[check.status] || check.status}`), node("p", check.explanation));
    for (const rule of check.rules || []) root.append(node("p", `${(check.conflictingIds || []).includes(rule.id) ? "Конфликт · " : ""}${rule.title} v${rule.version}: ${rule.rule}`));
    return root;
  }
  window.CodexInvariants = Object.freeze({ render, snapshot });
})();
