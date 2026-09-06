(() => {
  "use strict";

  const storageKey = "codex-chat-web-messages-v1";
  const chat = document.querySelector("#chat");
  const messagesNode = document.querySelector("#messages");
  const emptyState = document.querySelector("#empty-state");
  const form = document.querySelector("#chat-form");
  const input = document.querySelector("#message-input");
  const responseOptionsDetails = document.querySelector("#response-options");
  const responseFormatInput = document.querySelector("#response-format");
  const lengthLimitInput = document.querySelector("#length-limit");
  const completionConditionInput = document.querySelector("#completion-condition");
  const optionInputs = [responseFormatInput, lengthLimitInput, completionConditionInput];
  const sendButton = document.querySelector("#send-button");
  const newChatButton = document.querySelector("#new-chat");
  const modelName = document.querySelector("#model-name");
  const connection = document.querySelector(".connection");
  const connectionLabel = document.querySelector("#connection-label");
  const toast = document.querySelector("#toast");

  let transcript = loadTranscript();
  let sending = false;
  let toastTimer = 0;

  renderTranscript();
  resizeComposer();
  updateSendButton();
  loadStatus();

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

  document.querySelectorAll(".suggestion").forEach((button) => {
    button.addEventListener("click", () => {
      input.value = button.dataset.prompt || "";
      resizeComposer();
      updateSendButton();
      input.focus();
    });
  });

  async function loadStatus() {
    try {
      const response = await fetch("/api/status", { headers: { Accept: "application/json" } });
      const payload = await response.json();
      if (!response.ok) {
        throw new Error(payload.error || "Сервер недоступен");
      }
      modelName.textContent = payload.model || "OpenAI";
      connection.classList.add("online");
      connection.classList.remove("offline");
      connectionLabel.textContent = "готов к работе";
    } catch (error) {
      modelName.textContent = "нет соединения";
      connection.classList.add("offline");
      connection.classList.remove("online");
      connectionLabel.textContent = "соединение потеряно";
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

    sending = true;
    input.value = "";
    optionInputs.forEach((field) => { field.disabled = true; });
    resizeComposer();
    updateSendButton();
    newChatButton.disabled = true;
    sendButton.classList.add("sending");

    addMessage({ role: "user", text: message, time: Date.now() });
    const pending = addPendingMessage();
    scrollToLatest();

    try {
      const response = await fetch("/api/chat", {
        method: "POST",
        headers: {
          Accept: "application/json",
          "Content-Type": "application/json",
          "X-Codex-Chat": "1"
        },
        body: JSON.stringify({ message, ...responseOptions })
      });
      const payload = await response.json().catch(() => ({}));
      pending.remove();
      if (!response.ok) {
        throw new Error(payload.error || `Ошибка сервера (${response.status})`);
      }
      addMessage({ role: "assistant", text: payload.answer, time: Date.now() });
    } catch (error) {
      pending.remove();
      const messageText = error instanceof Error ? error.message : "Не удалось получить ответ";
      addErrorMessage(messageText);
      showToast("Запрос не выполнен. Проверьте сервер и API-ключ.");
    } finally {
      sending = false;
      optionInputs.forEach((field) => { field.disabled = false; });
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
      saveTranscript();
      renderTranscript();
      input.value = "";
      optionInputs.forEach((field) => { field.value = ""; });
      responseOptionsDetails.open = false;
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
    if (transcript.length > 80) {
      transcript = transcript.slice(-80);
    }
    saveTranscript();
    chat.classList.add("has-messages");
    messagesNode.append(createMessage(message));
  }

  function renderTranscript() {
    messagesNode.replaceChildren();
    chat.classList.toggle("has-messages", transcript.length > 0);
    emptyState.hidden = transcript.length > 0;
    transcript.forEach((message) => messagesNode.append(createMessage(message)));
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
    article.append(avatar, main);
    return article;
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
    sendButton.disabled = sending || !input.value.trim();
  }

  function scrollToLatest() {
    chat.scrollTop = chat.scrollHeight;
  }

  function formatTime(timestamp) {
    const date = new Date(timestamp || Date.now());
    return new Intl.DateTimeFormat("ru", { hour: "2-digit", minute: "2-digit" }).format(date);
  }

  function loadTranscript() {
    try {
      const value = JSON.parse(sessionStorage.getItem(storageKey) || "[]");
      return Array.isArray(value) ? value.filter(isStoredMessage).slice(-80) : [];
    } catch (error) {
      return [];
    }
  }

  function isStoredMessage(message) {
    return message && (message.role === "user" || message.role === "assistant") && typeof message.text === "string";
  }

  function saveTranscript() {
    try {
      sessionStorage.setItem(storageKey, JSON.stringify(transcript));
    } catch (error) {
      // The chat continues even when private browsing blocks storage.
    }
  }

  function showToast(message) {
    window.clearTimeout(toastTimer);
    toast.textContent = message;
    toast.classList.add("show");
    toastTimer = window.setTimeout(() => toast.classList.remove("show"), 2400);
  }
})();
