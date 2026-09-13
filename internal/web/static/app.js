(() => {
  "use strict";

  const workspace = document.querySelector(".workspace");
  const chat = document.querySelector("#chat");
  const messagesNode = document.querySelector("#messages");
  const emptyState = document.querySelector("#empty-state");
  const form = document.querySelector("#chat-form");
  const input = document.querySelector("#message-input");
  const responseOptionsDetails = document.querySelector("#response-options");
  const responseFormatInput = document.querySelector("#response-format");
  const lengthLimitInput = document.querySelector("#length-limit");
  const completionConditionInput = document.querySelector("#completion-condition");
  const temperatureInput = document.querySelector("#temperature");
  const contextStrategyInput = document.querySelector("#context-strategy");
  const contextKeepLastInput = document.querySelector("#context-keep-last");
  const strategyDescription = document.querySelector("#strategy-description");
  const optionInputs = [responseFormatInput, lengthLimitInput, completionConditionInput, temperatureInput];
  const sendButton = document.querySelector("#send-button");
  const newChatButton = document.querySelector("#new-chat");
  const modelSelect = document.querySelector("#model-select");
  const connection = document.querySelector(".connection");
  const connectionLabel = document.querySelector("#connection-label");
  const conversationStats = document.querySelector("#conversation-stats");
  const conversationContextTrack = document.querySelector("#conversation-context-track");
  const conversationContextFill = document.querySelector("#conversation-context-fill");
  const conversationContextPercent = document.querySelector("#conversation-context-percent");
  const conversationContextValue = document.querySelector("#conversation-context-value");
  const conversationHistoryTokens = document.querySelector("#conversation-history-tokens");
  const conversationTotalTokens = document.querySelector("#conversation-total-tokens");
  const conversationTotalCost = document.querySelector("#conversation-total-cost");
  const conversationStrategy = document.querySelector("#conversation-strategy");
  const conversationWindowMessages = document.querySelector("#conversation-window-messages");
  const conversationFactsCount = document.querySelector("#conversation-facts-count");
  const conversationActiveBranch = document.querySelector("#conversation-active-branch");
  const conversationContextWarning = document.querySelector("#conversation-context-warning");
  const factsPanel = document.querySelector("#facts-panel");
  const factsList = document.querySelector("#facts-list");
  const branchPanel = document.querySelector("#branch-panel");
  const createBranchesButton = document.querySelector("#create-branches");
  const branchSelectLabel = document.querySelector(".branch-select");
  const branchSelect = document.querySelector("#branch-select");
  const checkpointLabel = document.querySelector("#checkpoint-label");
  const toast = document.querySelector("#toast");

  let transcript = [];
  let sending = false;
  let historyReady = false;
  let toastTimer = 0;
  let contextState = {
    strategy: { type: "sliding_window", keepLast: 10 },
    facts: {},
    branches: [],
    activeBranchId: "",
    checkpoint: null
  };

  renderTranscript();
  updateStrategySettings();
  resizeComposer();
  updateSendButton();
  loadStatus();
  loadHistory();

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    sendMessage(input.value);
  });

  input.addEventListener("input", () => {
    resizeComposer();
    updateSendButton();
  });

  input.addEventListener("keydown", (event) => {
    if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
      event.preventDefault();
      if (!sendButton.disabled) {
        form.requestSubmit();
      }
    }
  });

  newChatButton.addEventListener("click", resetChat);
  contextStrategyInput.addEventListener("change", updateStrategySettings);
  createBranchesButton.addEventListener("click", createBranches);
  branchSelect.addEventListener("change", () => switchBranch(branchSelect.value));

  async function loadStatus() {
    try {
      const response = await fetch("/api/status", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) {
        throw new Error(payload.error || "Сервер недоступен");
      }
      const availableModels = Array.isArray(payload.models) ? payload.models : [];
      modelSelect.replaceChildren();
      availableModels.forEach((model) => {
        if (!model || typeof model.id !== "string") {
          return;
        }
        const option = document.createElement("option");
        option.value = model.id;
        option.textContent = typeof model.label === "string" ? model.label : model.id;
        option.title = model.id;
        modelSelect.append(option);
      });
      if (payload.model && Array.from(modelSelect.options).some((option) => option.value === payload.model)) {
        modelSelect.value = payload.model;
      }
      modelSelect.disabled = modelSelect.value === "";
      connection.classList.add("online");
      connection.classList.remove("offline");
      connectionLabel.textContent = "готов к работе";
    } catch (error) {
      modelSelect.replaceChildren(new Option("нет соединения", ""));
      modelSelect.disabled = true;
      connection.classList.add("offline");
      connection.classList.remove("online");
      connectionLabel.textContent = "соединение потеряно";
    }
  }

  async function loadHistory() {
    try {
      const response = await fetch("/api/history", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) {
        throw new Error(payload.error || "История недоступна");
      }
      const messages = Array.isArray(payload.messages) ? payload.messages : [];
      if (payload.context && typeof payload.context === "object") {
        applyContextState(payload.context);
        const keepLast = Number(payload.context.strategy && payload.context.strategy.keepLast);
        if (Number.isInteger(keepLast) && keepLast >= 1 && keepLast <= 1000) {
          contextKeepLastInput.value = String(keepLast);
        }
        const strategy = payload.context.strategy && payload.context.strategy.type;
        if (Array.from(contextStrategyInput.options).some((option) => option.value === strategy)) {
          contextStrategyInput.value = strategy;
        }
      }
      transcript = messages.filter(isHistoryMessage);
      setSessionSettingsLocked(transcript.length > 0);
      updateStrategySettings();
      renderTranscript();
    } catch (error) {
      showToast("Не удалось восстановить историю диалога");
    } finally {
      historyReady = true;
      updateSendButton();
    }
  }

  async function sendMessage(rawMessage) {
    const message = rawMessage.trim();
    if (!message || sending) {
      return;
    }

    const responseOptions = {
      responseFormat: responseFormatInput.value.trim(),
      lengthLimit: lengthLimitInput.value.trim(),
      completionCondition: completionConditionInput.value.trim()
    };
    const contextKeepLast = Number(contextKeepLastInput.value);
    if (!Number.isInteger(contextKeepLast) || contextKeepLast < 1 || contextKeepLast > 1000) {
      showToast("Укажите от 1 до 1000 последних сообщений");
      contextKeepLastInput.focus();
      return;
    }
    responseOptions.contextStrategy = contextStrategyInput.value;
    responseOptions.contextKeepLast = contextKeepLast;
    const temperature = temperatureInput.value.trim();
    if (temperature !== "") {
      const parsedTemperature = Number(temperature);
      if (!Number.isFinite(parsedTemperature) || parsedTemperature < 0 || parsedTemperature > 2) {
        showToast("Температура должна быть от 0 до 2");
        temperatureInput.focus();
        return;
      }
      responseOptions.temperature = parsedTemperature;
    }

    sending = true;
    input.value = "";
    optionInputs.forEach((field) => { field.disabled = true; });
    modelSelect.disabled = true;
    resizeComposer();
    updateSendButton();
    newChatButton.disabled = true;
    setSessionSettingsLocked(true);
    sendButton.classList.add("sending");

    addMessage({ role: "user", text: message, time: Date.now() });
    const pending = addPendingMessage();
    let requestWarning = "";
    let requestMetrics = null;
    scrollToLatest();

    try {
      const response = await fetch("/api/chat", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "X-Codex-Chat": "1"
        },
        body: JSON.stringify({ message, model: modelSelect.value, ...responseOptions })
      });
      const payload = await response.json().catch(() => ({}));
      requestWarning = typeof payload.warning === "string" ? payload.warning : "";
      requestMetrics = payload.metrics && typeof payload.metrics === "object" ? payload.metrics : null;
      pending.remove();
      if (!response.ok) {
        throw new Error(payload.error || `Ошибка сервера (${response.status})`);
      }
      if (payload.context) {
        applyContextState(payload.context);
      }
      addMessage({
        role: "assistant",
        text: payload.answer,
        time: Date.now(),
        model: payload.model || modelSelect.value,
        metrics: payload.metrics
      });
    } catch (error) {
      pending.remove();
      const messageText = error instanceof Error ? error.message : "Не удалось получить ответ";
      if (requestMetrics || requestWarning) {
        updateConversationMetrics(requestMetrics, requestWarning);
      }
      addErrorMessage(messageText);
      showToast("Запрос не выполнен. Проверьте сервер и API-ключ.");
    } finally {
      sending = false;
      optionInputs.forEach((field) => { field.disabled = false; });
      modelSelect.disabled = modelSelect.value === "";
      newChatButton.disabled = false;
      sendButton.classList.remove("sending");
      updateSendButton();
      input.focus();
      scrollToLatest();
    }
  }

  async function resetChat() {
    if (sending) {
      return;
    }
    newChatButton.disabled = true;
    try {
      const response = await fetch("/api/reset", {
        method: "POST",
        headers: { "X-Codex-Chat": "1" }
      });
      if (!response.ok) {
        throw new Error("reset failed");
      }
      transcript = [];
      renderTranscript();
      input.value = "";
      optionInputs.forEach((field) => { field.value = ""; });
      responseOptionsDetails.open = false;
      contextState = {
        strategy: { type: contextStrategyInput.value, keepLast: Number(contextKeepLastInput.value) || 10 },
        facts: {}, branches: [], activeBranchId: "", checkpoint: null
      };
      renderStrategyPanels();
      setSessionSettingsLocked(false);
      resizeComposer();
      updateSendButton();
      input.focus();
      showToast("Новый диалог начат");
    } catch (error) {
      showToast("Не удалось начать новый диалог");
    } finally {
      newChatButton.disabled = false;
    }
  }

  function addMessage(message) {
    transcript.push(message);
    workspace.classList.add("has-messages");
    chat.classList.add("has-messages");
    messagesNode.append(createMessage(message));
    if (message.role === "assistant" && message.metrics) {
      updateConversationMetrics(message.metrics);
    }
  }

  function renderTranscript() {
    messagesNode.replaceChildren();
    workspace.classList.toggle("has-messages", transcript.length > 0);
    chat.classList.toggle("has-messages", transcript.length > 0);
    emptyState.hidden = transcript.length > 0;
    transcript.forEach((message) => messagesNode.append(createMessage(message)));
    const latestAssistant = [...transcript].reverse().find((message) => message.role === "assistant" && message.metrics);
    updateConversationMetrics(latestAssistant ? latestAssistant.metrics : null);
    renderStrategyPanels();
    requestAnimationFrame(scrollToLatest);
  }

  function createMessage(message) {
    const article = document.createElement("article");
    article.className = `message ${message.role}`;

    const avatar = document.createElement("div");
    avatar.className = "message-avatar";
    avatar.setAttribute("aria-hidden", "true");
    avatar.textContent = message.role === "assistant" ? ">_" : "Вы";

    const main = document.createElement("div");
    main.className = "message-main";

    const meta = document.createElement("div");
    meta.className = "message-meta";
    const author = document.createElement("strong");
    author.textContent = message.role === "assistant" ? "Codex" : "Вы";
    const time = document.createElement("time");
    time.dateTime = new Date(message.time || Date.now()).toISOString();
    time.textContent = formatTime(message.time);
    meta.append(author, time);

    const content = document.createElement("div");
    content.className = "message-content";
    if (message.role === "assistant") {
      window.CodexMarkdown.render(content, message.text || "", showToast);
    } else {
      content.textContent = message.text || "";
    }

    main.append(meta, content);
    if (message.role === "assistant" && message.metrics) {
      main.append(createResponseMetrics(message.model, message.metrics));
    }
    article.append(avatar, main);
    return article;
  }

  function createResponseMetrics(model, metrics) {
    const node = document.createElement("div");
    node.className = "response-metrics";
    node.setAttribute("aria-label", "Метрики ответа");

    if (model) {
      node.append(metricItem("модель", model));
    }
    if (metrics.contextStrategy) {
      node.append(metricItem("контекст", strategyLabel(metrics.contextStrategy)));
    }
    node.append(metricItem("время", formatDuration(metrics.durationMs)));

    if (metrics.tokenCountAvailable) {
      node.append(metricItem("запрос", `${formatNumber(metrics.currentRequestTokens)} ток.`));
    }

    const totalTokens = Number(metrics.totalTokens) || 0;
    const tokenTitle = [
      `Вход: ${formatNumber(metrics.inputTokens)} токенов`,
      `Выход: ${formatNumber(metrics.outputTokens)} токенов`
    ];
    if (Number(metrics.cachedInputTokens) > 0) {
      tokenTitle.push(`Из кэша: ${formatNumber(metrics.cachedInputTokens)}`);
    }
    if (Number(metrics.cacheWriteTokens) > 0) {
      tokenTitle.push(`Записано в кэш: ${formatNumber(metrics.cacheWriteTokens)}`);
    }
    if (Number(metrics.reasoningTokens) > 0) {
      tokenTitle.push(`Рассуждение: ${formatNumber(metrics.reasoningTokens)}`);
    }
    node.append(metricItem("ответ", `${formatNumber(metrics.outputTokens)} ток.`));
    node.append(metricItem("ход", `${formatNumber(totalTokens)} ток.`, tokenTitle.join(" · ")));

    if (Number(metrics.memoryUpdates) > 0 && metrics.contextStrategy === "sticky_facts") {
      node.append(metricItem("facts", `${formatNumber(metrics.factsCount)} · ${formatNumber(metrics.memoryTotalTokens)} ток.`));
    }

    const cost = Number(metrics.costUsd);
    const costText = metrics.costUsd === null || metrics.costUsd === undefined || !Number.isFinite(cost)
      ? "неизвестно"
      : formatCost(cost);
    node.append(metricItem("стоимость", costText, "Ориентировочная стоимость токенов по публичным тарифам OpenAI; дополнительные сборы инструментов не учитываются."));
    return node;
  }

  function updateConversationMetrics(metrics, warning = "") {
    if (!metrics && !warning) {
      conversationStats.hidden = true;
      conversationContextWarning.hidden = true;
      conversationContextWarning.textContent = "";
      return;
    }

    metrics = normalizeConversationMetrics(metrics);
    conversationStats.hidden = false;
    const tokenCountAvailable = Boolean(metrics && metrics.tokenCountAvailable);
    const maxInputTokens = tokenCountAvailable ? Math.max(0, Number(metrics.maxInputTokens) || 0) : 0;
    const projectedInputTokens = tokenCountAvailable ? Math.max(0, Number(metrics.projectedInputTokens) || 0) : 0;
    const percent = maxInputTokens > 0 ? Math.max(0, Number(metrics.contextUsagePercent) || 0) : 0;
    const boundedPercent = Math.min(100, percent);

    conversationContextPercent.textContent = maxInputTokens > 0
      ? `${percent.toLocaleString("ru-RU", { maximumFractionDigits: 1 })}%`
      : "—";
    conversationContextValue.textContent = maxInputTokens > 0
      ? `${formatNumber(projectedInputTokens)} / ${formatNumber(maxInputTokens)} ток.`
      : "данные недоступны";
    conversationContextFill.style.width = `${boundedPercent}%`;
    conversationContextTrack.setAttribute("aria-valuenow", String(Math.round(boundedPercent)));
    conversationContextTrack.classList.toggle("overflow", percent > 100);

    conversationHistoryTokens.textContent = tokenCountAvailable
      ? `${formatNumber(metrics.historyTokens)} ток.`
      : "неизвестно";
    conversationTotalTokens.textContent = metrics
      ? `${formatNumber(metrics.cumulativeTotalTokens)} ток.`
      : "неизвестно";

    const cumulativeCost = Number(metrics && metrics.cumulativeCostUsd);
    conversationTotalCost.textContent = metrics && metrics.cumulativeCostUsd !== null && metrics.cumulativeCostUsd !== undefined && Number.isFinite(cumulativeCost)
      ? formatCost(cumulativeCost)
      : "неизвестно";

    const strategy = (metrics && metrics.contextStrategy) || (contextState.strategy && contextState.strategy.type) || "sliding_window";
    conversationStrategy.textContent = strategyLabel(strategy);
    conversationWindowMessages.textContent = metrics ? formatNumber(metrics.windowMessages) : "0";
    conversationFactsCount.textContent = metrics ? formatNumber(metrics.factsCount) : "0";
    const activeBranch = Array.isArray(contextState.branches)
      ? contextState.branches.find((branch) => branch.id === contextState.activeBranchId)
      : null;
    conversationActiveBranch.textContent = activeBranch ? activeBranch.name : "—";

    const contextWarning = warning || (metrics && typeof metrics.contextWarning === "string" ? metrics.contextWarning : "");
    conversationContextWarning.textContent = contextWarning;
    conversationContextWarning.hidden = !contextWarning;
  }

  function normalizeConversationMetrics(metrics) {
    if (!metrics || typeof metrics !== "object") {
      return metrics;
    }

    const normalized = { ...metrics };
    let transcriptTotalTokens = 0;
    let transcriptCost = 0;
    let transcriptCostKnown = true;
    let assistantMessages = 0;
    let committedMessages = 0;

    transcript.forEach((message, index) => {
      if (message.role !== "assistant" || !message.metrics) {
        return;
      }
      assistantMessages += 1;
      committedMessages = index + 1;
      const turnTotal = Math.max(
        0,
        Number(message.metrics.totalTokens)
          || (Number(message.metrics.inputTokens) || 0) + (Number(message.metrics.outputTokens) || 0)
      );
      transcriptTotalTokens += turnTotal;

      const turnCost = Number(message.metrics.costUsd);
      if (message.metrics.costUsd === null || message.metrics.costUsd === undefined || !Number.isFinite(turnCost)) {
        transcriptCostKnown = false;
      } else {
        transcriptCost += turnCost;
      }
    });

    if (Math.max(0, Number(normalized.cumulativeTotalTokens) || 0) === 0 && transcriptTotalTokens > 0) {
      normalized.cumulativeTotalTokens = transcriptTotalTokens;
    }
    const cumulativeCost = Number(normalized.cumulativeCostUsd);
    if ((normalized.cumulativeCostUsd === null || normalized.cumulativeCostUsd === undefined || !Number.isFinite(cumulativeCost))
        && transcriptCostKnown && assistantMessages > 0) {
      normalized.cumulativeCostUsd = transcriptCost;
    }

    normalized.retainedMessages = committedMessages;
    return normalized;
  }

  function metricItem(label, value, title = "") {
    const item = document.createElement("span");
    item.className = "response-metric";
    if (title) {
      item.title = title;
    }
    const name = document.createElement("span");
    name.className = "response-metric-label";
    name.textContent = `${label}:`;
    const content = document.createElement("strong");
    content.textContent = value;
    item.append(name, content);
    return item;
  }

  function formatDuration(value) {
    const milliseconds = Math.max(0, Number(value) || 0);
    if (milliseconds < 1000) {
      return `${formatNumber(milliseconds)} мс`;
    }
    const seconds = milliseconds / 1000;
    return `${seconds.toLocaleString("ru-RU", { minimumFractionDigits: 1, maximumFractionDigits: 2 })} с`;
  }

  function formatNumber(value) {
    return new Intl.NumberFormat("ru-RU").format(Math.max(0, Number(value) || 0));
  }

  function formatCost(value) {
    if (value > 0 && value < 0.000001) {
      return "< $0.000001";
    }
    const digits = value >= 0.01 ? 4 : 6;
    return `≈ $${value.toFixed(digits)}`;
  }

  function addPendingMessage() {
    chat.classList.add("has-messages");
    const article = document.createElement("article");
    article.className = "message assistant pending";
    article.innerHTML = '<div class="message-avatar" aria-hidden="true">&gt;_</div><div class="message-main"><div class="message-meta"><strong>Codex</strong><time>думает</time></div><div class="thinking" aria-label="Готовится ответ"><i></i><i></i><i></i></div></div>';
    messagesNode.append(article);
    return article;
  }

  function addErrorMessage(text) {
    const article = document.createElement("article");
    article.className = "message error";
    const avatar = document.createElement("div");
    avatar.className = "message-avatar";
    avatar.textContent = "!";
    const main = document.createElement("div");
    main.className = "message-main";
    const meta = document.createElement("div");
    meta.className = "message-meta";
    const title = document.createElement("strong");
    title.textContent = "Запрос не выполнен";
    meta.append(title);
    const content = document.createElement("div");
    content.className = "message-content";
    content.textContent = text;
    main.append(meta, content);
    article.append(avatar, main);
    messagesNode.append(article);
  }

  function resizeComposer() {
    input.rows = 1;
    const lineHeight = Number.parseFloat(window.getComputedStyle(input).lineHeight) || 22;
    input.rows = Math.min(8, Math.max(1, Math.ceil(input.scrollHeight / lineHeight)));
  }

  function updateSendButton() {
    sendButton.disabled = sending || !historyReady || !input.value.trim();
  }

  function setSessionSettingsLocked(locked) {
    contextStrategyInput.disabled = locked;
    contextKeepLastInput.disabled = locked || contextStrategyInput.value === "branching";
  }

  function updateStrategySettings() {
    const descriptions = {
      sliding_window: "Последние N сообщений, остальное отбрасывается",
      sticky_facts: "Facts key-value + последние N сообщений",
      branching: "Две независимые ветки из одного checkpoint"
    };
    strategyDescription.textContent = descriptions[contextStrategyInput.value] || descriptions.sliding_window;
    contextKeepLastInput.disabled = contextStrategyInput.disabled || contextStrategyInput.value === "branching";
    if (transcript.length === 0) {
      contextState.strategy = {
        type: contextStrategyInput.value,
        keepLast: Number(contextKeepLastInput.value) || 10
      };
      renderStrategyPanels();
    }
  }

  function strategyLabel(strategy) {
    return {
      sliding_window: "Sliding Window",
      sticky_facts: "Sticky Facts",
      branching: "Branching"
    }[strategy] || strategy;
  }

  function applyContextState(next) {
    if (!next || typeof next !== "object") {
      return;
    }
    contextState = {
      strategy: next.strategy && typeof next.strategy === "object"
        ? next.strategy
        : { type: "sliding_window", keepLast: 10 },
      facts: next.facts && typeof next.facts === "object" ? next.facts : {},
      branches: Array.isArray(next.branches) ? next.branches : [],
      activeBranchId: typeof next.activeBranchId === "string" ? next.activeBranchId : "",
      checkpoint: next.checkpoint && typeof next.checkpoint === "object" ? next.checkpoint : null
    };
    renderStrategyPanels();
  }

  function renderStrategyPanels() {
    const strategy = contextState.strategy && contextState.strategy.type;
    factsPanel.hidden = strategy !== "sticky_facts" || transcript.length === 0;
    factsList.replaceChildren();
    Object.entries(contextState.facts || {}).sort(([first], [second]) => first.localeCompare(second, "ru")).forEach(([key, value]) => {
      const row = document.createElement("div");
      const name = document.createElement("dt");
      const content = document.createElement("dd");
      name.textContent = key;
      content.textContent = value;
      row.append(name, content);
      factsList.append(row);
    });
    if (!factsPanel.hidden && factsList.childElementCount === 0) {
      const empty = document.createElement("div");
      empty.className = "strategy-empty";
      empty.textContent = "Важные факты пока не выделены";
      factsList.append(empty);
    }

    branchPanel.hidden = strategy !== "branching" || transcript.length === 0;
    const branches = Array.isArray(contextState.branches) ? contextState.branches : [];
    createBranchesButton.hidden = branches.length > 0;
    branchSelectLabel.hidden = branches.length === 0;
    branchSelect.replaceChildren();
    branches.forEach((branch) => {
      const option = document.createElement("option");
      option.value = branch.id;
      option.textContent = `${branch.name} · ${formatNumber(branch.messageCount)} сообщ.`;
      branchSelect.append(option);
    });
    if (branches.some((branch) => branch.id === contextState.activeBranchId)) {
      branchSelect.value = contextState.activeBranchId;
    }
    checkpointLabel.textContent = contextState.checkpoint
      ? `Checkpoint: ${formatNumber(contextState.checkpoint.messageCount)} сообщений`
      : "Сначала соберите общий контекст, затем создайте две ветки";
  }

  async function createBranches() {
    if (sending) {
      return;
    }
    createBranchesButton.disabled = true;
    try {
      const response = await fetch("/api/branches/create", {
        method: "POST",
        headers: { "X-Codex-Chat": "1" }
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error(payload.error || "Не удалось создать ветки");
      }
      transcript = Array.isArray(payload.messages) ? payload.messages.filter(isHistoryMessage) : transcript;
      applyContextState(payload.context);
      renderTranscript();
      showToast("Checkpoint сохранён, созданы две ветки");
    } catch (error) {
      showToast(error instanceof Error ? error.message : "Не удалось создать ветки");
    } finally {
      createBranchesButton.disabled = false;
    }
  }

  async function switchBranch(branchId) {
    if (sending || !branchId || branchId === contextState.activeBranchId) {
      return;
    }
    branchSelect.disabled = true;
    try {
      const response = await fetch("/api/branches/switch", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
          "X-Codex-Chat": "1"
        },
        body: JSON.stringify({ branchId })
      });
      const payload = await response.json().catch(() => ({}));
      if (!response.ok) {
        throw new Error(payload.error || "Не удалось переключить ветку");
      }
      transcript = Array.isArray(payload.messages) ? payload.messages.filter(isHistoryMessage) : [];
      applyContextState(payload.context);
      renderTranscript();
      showToast("Ветка переключена");
    } catch (error) {
      renderStrategyPanels();
      showToast(error instanceof Error ? error.message : "Не удалось переключить ветку");
    } finally {
      branchSelect.disabled = false;
    }
  }

  function scrollToLatest() {
    chat.scrollTop = chat.scrollHeight;
  }

  function formatTime(timestamp) {
    const date = new Date(timestamp || Date.now());
    return new Intl.DateTimeFormat("ru", { hour: "2-digit", minute: "2-digit" }).format(date);
  }

  function isHistoryMessage(message) {
    return message && (message.role === "user" || message.role === "assistant") && typeof message.text === "string";
  }

  function showToast(message) {
    window.clearTimeout(toastTimer);
    toast.textContent = message;
    toast.classList.add("show");
    toastTimer = window.setTimeout(() => toast.classList.remove("show"), 2400);
  }
})();
