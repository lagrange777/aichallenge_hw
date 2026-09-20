(() => {
  const stages = { planning: "Планирование", execution: "Выполнение", validation: "Проверка", done: "Завершено" };
  const fields = [
    ["goal", "Цель и критерии готовности", 2000],
    ["currentStep", "Текущий шаг", 500],
    ["expectedAction", "Ожидаемое действие", 2000],
    ["completed", "Уже выполнено и принято", 6000],
    ["result", "Промежуточный результат", 6000],
    ["openQuestions", "Открытые вопросы", 2000],
    ["validation", "Результат проверки", 4000]
  ];
  const transitions = { planning: ["execution"], execution: ["validation"], validation: ["execution", "done"], done: [] };
  let current = null;
  function node(tag, text, className) {
    const el = document.createElement(tag);
    if (text !== undefined) el.textContent = text;
    if (className) el.className = className;
    return el;
  }
  function button(text, action, primary = false) {
    const el = node("button", text, `memory-button${primary ? " memory-primary" : ""}`);
    el.type = "button"; el.addEventListener("click", action); return el;
  }
  function details(progress) {
    const list = node("dl", undefined, "task-checkpoint");
    list.append(node("dt", "Кто действует"), node("dd", progress.expectedActor === "agent" ? "Агент" : "Вы"));
    for (const [key, label] of fields) {
      if (progress[key]) list.append(node("dt", label), node("dd", progress[key]));
    }
    return list;
  }
  function render(state, mutate) {
    current = state.task;
    const w = current.workflow;
    const root = document.querySelector("#task-workflow");
    root.replaceChildren();
    const command = (action, progress) => mutate("/api/tasks/state", {
      action, progress, version: w.version, profileId: window.CodexProfiles.activeID()
    });
    const stepper = node("ol", undefined, "task-stepper");
    stepper.setAttribute("aria-label", "Этап задачи");
    for (const [key, label] of Object.entries(stages)) {
      const item = node("li", label);
      if (key === w.stage) item.setAttribute("aria-current", "step");
      stepper.append(item);
    }
    root.append(stepper, node("p", w.paused ? "На паузе · точка продолжения сохранена" : w.stage === "done" ? "Задача завершена · для новой работы создайте другую задачу" : "Активна · следующий шаг выполняется по вашему запросу", "task-state-status"));
    root.append(details(w));
    const actions = node("div", undefined, "memory-actions");
    if (w.stage !== "done") {
      actions.append(button(w.paused ? "Продолжить" : "Приостановить", () => command(w.paused ? "resume" : "pause"), w.paused));
      if (!w.paused) actions.append(button(w.expectedActor === "agent" ? "Выполнить текущий шаг" : "Ответить агенту", () => {
        window.dispatchEvent(new CustomEvent("codex:task-continue", { detail: { run: w.expectedActor === "agent" } }));
      }, true));
    }
    root.append(actions, node("p", "Пауза доступна между ответами. «Продолжить» снимает паузу; запуск следующего шага — отдельной кнопкой. Новый чат сохраняет состояние задачи.", "task-state-hint"));
    if (w.proposal) {
      const card = node("section", undefined, "task-state-proposal");
      const p = w.proposal;
      card.append(node("h3", "Агент предлагает"), node("p", p.progress.stage === w.stage ? "Обновить точку продолжения" : `${stages[w.stage]} → ${stages[p.progress.stage]}`), node("p", p.reason), details(p.progress));
      const review = node("div", undefined, "memory-actions");
      const accept = button(p.progress.stage === "done" ? "Подтвердить завершение" : "Подтвердить обновление", () => command("accept"), true);
      accept.disabled = w.paused;
      review.append(accept, button("Отклонить", () => command("reject")));
      card.append(review);
      if (w.paused) card.append(node("p", "Снимите паузу, чтобы подтвердить обновление."));
      root.append(card);
    }
    if (w.stage !== "done" && !w.paused) {
      const edit = node("details", undefined, "task-state-editor");
      edit.append(node("summary", "Изменить точку продолжения вручную"));
      const form = node("form");
      const label = (text, input) => { const el = node("label", text); el.append(input); form.append(el); };
      const stage = node("select"); stage.name = "stage";
      for (const key of [w.stage, ...transitions[w.stage]]) { const opt = node("option", stages[key]); opt.value = key; stage.append(opt); }
      label("Этап (переход применяется при сохранении)", stage);
      const actor = node("select"); actor.name = "expectedActor";
      for (const [key, text] of [["agent", "Агент"], ["user", "Вы"]]) { const opt = node("option", text); opt.value = key; actor.append(opt); }
      actor.value = w.expectedActor; label("Кто должен действовать", actor);
      for (const [key, text, max] of fields) {
        const input = node("textarea"); input.name = key; input.value = w[key] || ""; input.maxLength = max; input.rows = key === "currentStep" ? 2 : 3;
        input.required = ["goal", "currentStep", "expectedAction"].includes(key);
        label(text, input);
      }
      form.append(node("p", "Для проверки нужен сохранённый результат. Для завершения также укажите, что проверено и с каким итогом. Агент в этом чате не запускает код и тесты."));
      const save = node("button", "Сохранить точку продолжения", "memory-button memory-primary"); save.type = "submit";
      stage.addEventListener("change", () => { save.textContent = stage.value === w.stage ? "Сохранить точку продолжения" : `Подтвердить переход: ${stages[stage.value]}`; });
      form.append(save);
      form.addEventListener("submit", event => { event.preventDefault(); command("save", Object.fromEntries(new FormData(form))); });
      edit.append(form); root.append(edit);
    }
    if (w.lastTurn) {
      const turn = node("details", undefined, "task-state-editor");
      turn.append(node("summary", "Последний обмен · сохранён для продолжения"), node("p", "Это контекст разговора. Подтверждённое состояние меняется только по вашему решению."), node("h4", "Вы"), node("p", w.lastTurn.user), node("h4", "Агент"), node("p", w.lastTurn.assistant));
      root.append(turn);
    }
    const journal = node("details", undefined, "task-state-editor");
    journal.append(node("summary", `История изменений (${(w.events || []).length})`));
    const names = { pause: "Пауза", resume: "Продолжение", save: "Сохранено вручную", accept: "Предложение принято", reject: "Предложение отклонено" };
    for (const event of [...(w.events || [])].reverse()) journal.append(node("p", `${new Date(event.at).toLocaleString("ru-RU")} · ${names[event.action]} · ${stages[event.from]}${event.from === event.to ? "" : ` → ${stages[event.to]}`} · ${event.step}`));
    if (!w.events?.length) journal.append(node("p", "Здесь появятся переходы, паузы и подтверждения."));
    root.append(journal);
    const banner = document.querySelector("#chat-task-state");
    banner.replaceChildren(node("span", `${current.name} · ${stages[w.stage]}${w.paused ? " · На паузе" : ""}${w.proposal ? " · Есть предложение" : ""}`), button("Состояние задачи", () => window.dispatchEvent(new CustomEvent("codex:open-tasks"))));
    banner.title = `${w.currentStep}. ${w.expectedActor === "agent" ? "Агент" : "Вы"}: ${w.expectedAction}`;
    window.dispatchEvent(new CustomEvent("codex:task-state", { detail: current }));
  }
  window.CodexTaskState = Object.freeze({ render, stages, current: () => current });
})();
