(() => {
  const stages = { planning: "Планирование", execution: "Выполнение", validation: "Проверка", done: "Завершено" };
  const fields = [
    ["goal", "Цель и критерии готовности", 2000],
    ["plan", "План выполнения", 6000],
    ["currentStep", "Текущий шаг", 500],
    ["expectedAction", "Ожидаемое действие", 2000],
    ["completed", "Уже выполнено и принято", 6000],
    ["result", "Промежуточный результат", 6000],
    ["openQuestions", "Открытые вопросы", 2000],
    ["validation", "Результат проверки", 4000]
  ];
  const transitions = { planning: ["execution"], execution: ["validation", "planning"], validation: ["execution", "done", "planning"], done: [] };
  let current = null;
  let change = null;
  let busy = true;
  let pendingTransition = null;
  const nextButton = document.querySelector("#composer-task-next");
  const pauseButton = document.querySelector("#composer-task-pause");
  const transitionDialog = document.querySelector("#task-transition-dialog");
  const transitionForm = document.querySelector("#task-transition-form");
  const actionNames = { approve_plan: "Утвердить план", submit_result: "На проверку", request_changes: "Вернуть на доработку", replan: "Вернуться к планированию", complete: "Завершить задачу" };
  function transitionAction(from, to) { return to === "planning" ? "replan" : from === "planning" ? "approve_plan" : to === "validation" ? "submit_result" : to === "done" ? "complete" : "request_changes"; }
  function blockedReason(w, target) {
    if (w.paused) return "Сначала снимите паузу.";
    if (target === "done" && (w.validationStatus !== "passed" || !w.resultVersion || w.validatedResultVersion !== w.resultVersion)) return "Сначала подтвердите успешную проверку текущего результата во вкладке «Задачи».";
    if (w.stage !== "planning" && target !== "planning" && (!w.planVersion || w.approvedPlanVersion !== w.planVersion)) return "Нет утверждённого плана. Вернитесь к планированию.";
    return "";
  }
  const forward = { planning: "execution", execution: "validation", validation: "done" };
  function nextStage(w) {
    const proposed = w.proposal?.progress.stage;
    return proposed && proposed !== w.stage && transitions[w.stage].includes(proposed) ? proposed : forward[w.stage];
  }
  function syncControls() {
    const w = current?.workflow;
    const next = w && nextStage(w);
    const reason = w && next ? blockedReason(w, next) : "Задача завершена";
    nextButton.textContent = next ? actionNames[transitionAction(w.stage, next)] : "Задача завершена";
    nextButton.title = reason || `${stages[w.stage]} → ${stages[next]}`;
    nextButton.disabled = busy || !w || !next || !!reason;
    pauseButton.textContent = w?.paused ? "Продолжить" : "Пауза";
    pauseButton.title = w?.paused ? "Снять паузу задачи" : "Приостановить задачу между ответами";
    pauseButton.disabled = busy || !w || w.stage === "done";
    transitionForm.querySelectorAll("input, textarea, select, button").forEach(el => { el.disabled = busy; });
  }
  pauseButton.addEventListener("click", () => {
    if (pauseButton.disabled) return;
    change("/api/tasks/state", { action: current.workflow.paused ? "resume" : "pause", version: current.workflow.version, profileId: window.CodexProfiles.activeID() });
  });
  nextButton.addEventListener("click", () => { if (!nextButton.disabled) openTransition(nextStage(current.workflow)); });
  function openTransition(target) {
    const w = current.workflow;
    if (busy || blockedReason(w, target)) return;
    const proposed = w.proposal?.progress.stage === target;
    const progress = { ...w, ...(proposed ? w.proposal.progress : {}), stage: target };
    if (w.stage !== "planning" && target !== "planning") { progress.plan = w.plan; progress.goal = w.goal; }
    if (w.stage === "validation") progress.result = w.result;
    // Completion confirms the already recorded verification, never a draft.
    if (target === "done") { progress.result = w.result; progress.validation = w.validation; }
    const action = transitionAction(w.stage, target);
    if (!proposed) {
      const defaults = {
        planning: ["Уточнить и утвердить план", "Составить план для повторного утверждения"],
        execution: ["Выполнить согласованный план", "Выполнить следующий шаг по сохранённой цели и плану"],
        validation: ["Проверить результат по критериям готовности", "Проверить сохранённый результат и сообщить о найденных несоответствиях"],
        done: ["Задача завершена", "Дальнейших действий не требуется"]
      };
      [progress.currentStep, progress.expectedAction] = defaults[target];
      progress.expectedActor = target === "done" ? "user" : "agent";
    }
    pendingTransition = { taskId: current.id, profileId: window.CodexProfiles.activeID(), version: w.version, target, action };
    document.querySelector("#task-transition-title").textContent = `${stages[w.stage]} → ${stages[target]}`;
    document.querySelector("#task-transition-reason").textContent = proposed ? w.proposal.reason : "Проверьте точку продолжения перед переходом. Сохранённые результаты будут переданы агенту.";
    document.querySelector("#task-transition-status").textContent = "";
    const fieldsRoot = document.querySelector("#task-transition-fields");
    fieldsRoot.replaceChildren();
    const actorLabel = node("label", "Кто должен действовать");
    const actor = node("select"); actor.name = "expectedActor";
    for (const [key, text] of [["agent", "Агент"], ["user", "Вы"]]) { const option = node("option", text); option.value = key; actor.append(option); }
    actor.value = progress.expectedActor; actorLabel.append(actor); fieldsRoot.append(actorLabel);
    for (const [key, label, max] of fields) {
      const wrapper = node("label", label);
      const input = node("textarea"); input.name = key; input.value = progress[key] || ""; input.rows = 2; input.maxLength = max;
      input.required = ["goal", "currentStep", "expectedAction"].includes(key) || key === "plan" && target === "execution" || key === "result" && ["validation", "done"].includes(target) || key === "validation" && target === "done";
      if (["plan", "goal"].includes(key) && w.stage !== "planning" && target !== "planning" || key === "result" && target === "done" || key === "validation" && target === "done") input.readOnly = true;
      if (key === "validation" && action === "request_changes") input.required = true;
      wrapper.append(input); fieldsRoot.append(wrapper);
    }
    document.querySelector("#task-transition-confirm").textContent = actionNames[action];
    syncControls(); transitionDialog.showModal();
  }
  transitionForm.addEventListener("submit", async event => {
    event.preventDefault();
    if (busy || !pendingTransition) return;
    const { target, action, ...identity } = pendingTransition;
    const progress = { ...Object.fromEntries(new FormData(transitionForm)), stage: target };
    const payload = await change("/api/tasks/state", { ...identity, action, progress });
    if (payload) transitionDialog.close();
  });
  document.querySelector("#task-transition-cancel").addEventListener("click", () => transitionDialog.close());
  transitionDialog.addEventListener("cancel", event => { if (busy) event.preventDefault(); });
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
    if (current?.id !== state.task.id) document.querySelector("#composer-task-status").textContent = "";
    current = state.task;
    change = mutate;
    const w = current.workflow;
    if (transitionDialog.open && pendingTransition && (pendingTransition.taskId !== current.id || pendingTransition.version !== w.version)) transitionDialog.close();
    syncControls();
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
    const validationNames = { pending: "ожидается", passed: "пройдена", failed: "не пройдена" };
    root.append(node("p", `План v${w.planVersion || 0}: ${w.planVersion && w.approvedPlanVersion === w.planVersion ? "утверждён" : "не утверждён"}. Результат v${w.resultVersion || 0}. Проверка: ${validationNames[w.validationStatus] || "ожидается"}${w.validatedResultVersion ? ` для результата v${w.validatedResultVersion}` : ""}.`, "task-state-hint"));
    if (w.validationBasis) root.append(node("p", w.validationBasis === "user_report" ? "Основание: проверка, о которой сообщил пользователь" : "Основание: анализ результата без запуска кода"));
    const actions = node("div", undefined, "memory-actions");
    if (w.stage !== "done") {
      actions.append(button(w.paused ? "Продолжить" : "Приостановить", () => command(w.paused ? "resume" : "pause"), w.paused));
      if (!w.paused) actions.append(button(w.expectedActor === "agent" ? "Выполнить текущий шаг" : "Ответить агенту", () => {
        window.dispatchEvent(new CustomEvent("codex:task-continue", { detail: { run: w.expectedActor === "agent" } }));
      }, true));
    }
    if (w.stage !== "done") {
      for (const target of transitions[w.stage]) {
        const reason = blockedReason(w, target);
        const control = button(actionNames[transitionAction(w.stage, target)], () => openTransition(target));
        control.disabled = !!reason; control.title = reason;
        actions.append(control);
        if (reason) root.append(node("p", reason, "task-state-hint"));
      }
    }
    root.append(actions, node("p", "Пауза доступна между ответами. «Продолжить» снимает паузу; запуск следующего шага — отдельной кнопкой. Новый чат сохраняет состояние задачи.", "task-state-hint"));
    if (w.proposal) {
      const card = node("section", undefined, "task-state-proposal");
      const p = w.proposal;
      card.append(node("h3", "Агент предлагает"), node("p", p.progress.stage === w.stage ? "Обновить точку продолжения" : `${stages[w.stage]} → ${stages[p.progress.stage]}`), node("p", p.reason), details(p.progress));
      const review = node("div", undefined, "memory-actions");
      const differentStage = p.progress.stage !== w.stage;
      const accept = button(differentStage ? actionNames[transitionAction(w.stage, p.progress.stage)] : "Подтвердить обновление", () => differentStage ? openTransition(p.progress.stage) : command("accept"), true);
      accept.disabled = w.paused || differentStage && !!blockedReason(w, p.progress.stage);
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
      const actor = node("select"); actor.name = "expectedActor";
      for (const [key, text] of [["agent", "Агент"], ["user", "Вы"]]) { const opt = node("option", text); opt.value = key; actor.append(opt); }
      actor.value = w.expectedActor; label("Кто должен действовать", actor);
      for (const [key, text, max] of fields) {
        const input = node("textarea"); input.name = key; input.value = w[key] || ""; input.maxLength = max; input.rows = key === "currentStep" ? 2 : 3;
        input.required = ["goal", "currentStep", "expectedAction"].includes(key);
        input.readOnly = ["plan", "goal"].includes(key) && w.stage !== "planning" || key === "result" && w.stage === "validation";
        label(text, input);
      }
      form.append(node("p", "Сохранение обновляет текущий шаг и не меняет этап. Для изменения утверждённого плана вернитесь к планированию. Агент не запускает код и тесты."));
      const save = node("button", "Сохранить точку продолжения", "memory-button memory-primary"); save.type = "submit";
      form.append(save);
      form.addEventListener("submit", event => { event.preventDefault(); command("save", { ...Object.fromEntries(new FormData(form)), stage: w.stage }); });
      edit.append(form); root.append(edit);
    }
    if (w.stage === "validation" && !w.paused) {
      const form = node("form", undefined, "task-state-editor");
      form.append(node("h3", "Подтверждение проверки результата"));
      const label = (text, input) => { const wrap = node("label", text); wrap.append(input); form.append(wrap); };
      const status = node("select");
      for (const [value, text] of [["", "Выберите итог"], ["passed", "Проверка пройдена"], ["failed", "Найдены замечания"]]) { const opt = node("option", text); opt.value = value; status.append(opt); }
      status.required = true; label("Итог", status);
      const basis = node("select");
      for (const [value, text] of [["", "Выберите основание"], ["user_report", "Я выполнил проверку и сообщаю результат"], ["conceptual_review", "Анализ результата без запуска кода"]]) { const opt = node("option", text); opt.value = value; basis.append(opt); }
      basis.required = true; label("Основание", basis);
      const evidence = node("textarea"); evidence.required = true; evidence.maxLength = 4000; evidence.value = w.validation || ""; label("Что проверено и с каким результатом", evidence);
      const save = node("button", `Подтвердить проверку результата v${w.resultVersion}`, "memory-button memory-primary"); save.type = "submit"; form.append(save);
      form.addEventListener("submit", event => {
        event.preventDefault();
        const progress = Object.fromEntries(["stage", "expectedActor", ...fields.map(f => f[0])].map(key => [key, w[key] || ""]));
        progress.validation = evidence.value;
        mutate("/api/tasks/state", { action: "record_validation", progress, validationStatus: status.value, validationBasis: basis.value, version: w.version, profileId: window.CodexProfiles.activeID() });
      });
      root.append(form);
    }
    if (w.lastTurn) {
      const turn = node("details", undefined, "task-state-editor");
      turn.append(node("summary", "Последний обмен · сохранён для продолжения"), node("p", "Это контекст разговора. Подтверждённое состояние меняется только по вашему решению."), node("h4", "Вы"), node("p", w.lastTurn.user), node("h4", "Агент"), node("p", w.lastTurn.assistant));
      root.append(turn);
    }
    const journal = node("details", undefined, "task-state-editor");
    journal.append(node("summary", `История изменений (${(w.events || []).length})`));
    const names = { ...actionNames, record_validation: "Проверка подтверждена", pause: "Пауза", resume: "Продолжение", save: "Сохранено вручную", accept: "Предложение принято", reject: "Предложение отклонено" };
    for (const event of [...(w.events || [])].reverse()) journal.append(node("p", `${new Date(event.at).toLocaleString("ru-RU")} · ${event.rejected ? "Отклонено: " : ""}${names[event.action] || event.action} · ${stages[event.from]}${event.from === event.to ? "" : ` → ${stages[event.to]}`} · ${event.step}${event.reason ? ` · ${event.reason}` : ""}`));
    if (!w.events?.length) journal.append(node("p", "Здесь появятся переходы, паузы и подтверждения."));
    root.append(journal);
    const banner = document.querySelector("#chat-task-state");
    banner.replaceChildren(node("span", `${current.name} · ${stages[w.stage]}${w.paused ? " · На паузе" : ""}${w.proposal ? " · Есть предложение" : ""}`), button("Состояние задачи", () => window.dispatchEvent(new CustomEvent("codex:open-tasks"))));
    banner.title = `${w.currentStep}. ${w.expectedActor === "agent" ? "Агент" : "Вы"}: ${w.expectedAction}`;
    window.dispatchEvent(new CustomEvent("codex:task-state", { detail: current }));
  }
  window.CodexTaskState = Object.freeze({ render, stages, current: () => current,
    setBusy(value) { busy = value; syncControls(); },
    setStatus(text) {
      document.querySelector("#composer-task-status").textContent = text;
      document.querySelector("#task-transition-status").textContent = text;
    }
  });
})();
