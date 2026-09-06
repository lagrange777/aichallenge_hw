(() => {
  "use strict";

  const blockPatterns = {
    fence: /^\s*```([^`]*)$/,
    heading: /^\s*(#{1,6})\s+(.+?)\s*#*\s*$/,
    quote: /^\s*>\s?(.*)$/,
    list: /^\s*([-+*]|\d+[.)])\s+(.+)$/,
    rule: /^\s{0,3}((\*\s*){3,}|(-\s*){3,}|(_\s*){3,})$/,
    tableDivider: /^\s*\|?\s*:?-{3,}:?\s*(\|\s*:?-{3,}:?\s*)+\|?\s*$/
  };

  function render(container, markdown, notify) {
    container.replaceChildren();
    const lines = String(markdown || "").replace(/\r\n?/g, "\n").split("\n");
    let index = 0;

    while (index < lines.length) {
      if (!lines[index].trim()) {
        index += 1;
        continue;
      }

      const fence = lines[index].match(blockPatterns.fence);
      if (fence) {
        const codeLines = [];
        index += 1;
        while (index < lines.length && !blockPatterns.fence.test(lines[index])) {
          codeLines.push(lines[index]);
          index += 1;
        }
        if (index < lines.length) {
          index += 1;
        }
        container.append(createCodeBlock(fence[1].trim(), codeLines.join("\n"), notify));
        continue;
      }

      const heading = lines[index].match(blockPatterns.heading);
      if (heading) {
        const node = document.createElement(`h${heading[1].length}`);
        node.className = "markdown-block markdown-heading";
        appendInline(node, heading[2]);
        container.append(node);
        index += 1;
        continue;
      }

      if (isTableStart(lines, index)) {
        const headers = splitTableRow(lines[index]);
        const alignments = splitTableRow(lines[index + 1]).map(tableAlignment);
        const rows = [];
        index += 2;
        while (index < lines.length && lines[index].includes("|") && lines[index].trim()) {
          rows.push(splitTableRow(lines[index]));
          index += 1;
        }
        container.append(createTable(headers, alignments, rows));
        continue;
      }

      if (blockPatterns.rule.test(lines[index])) {
        const rule = document.createElement("hr");
        rule.className = "markdown-block";
        container.append(rule);
        index += 1;
        continue;
      }

      if (blockPatterns.quote.test(lines[index])) {
        const quoteLines = [];
        while (index < lines.length) {
          const quote = lines[index].match(blockPatterns.quote);
          if (!quote) {
            break;
          }
          quoteLines.push(quote[1]);
          index += 1;
        }
        const quote = document.createElement("blockquote");
        quote.className = "markdown-block";
        appendInline(quote, quoteLines.join(" "));
        container.append(quote);
        continue;
      }

      const listItem = lines[index].match(blockPatterns.list);
      if (listItem) {
        const ordered = /^\d/.test(listItem[1]);
        const list = document.createElement(ordered ? "ol" : "ul");
        list.className = "markdown-block markdown-list";
        while (index < lines.length) {
          const item = lines[index].match(blockPatterns.list);
          if (!item || /^\d/.test(item[1]) !== ordered) {
            break;
          }
          const listNode = document.createElement("li");
          const task = item[2].match(/^\[([ xX])\]\s+(.+)$/);
          if (task) {
            listNode.className = "task-item";
            const checkbox = document.createElement("input");
            checkbox.type = "checkbox";
            checkbox.checked = task[1].toLowerCase() === "x";
            checkbox.disabled = true;
            listNode.append(checkbox);
            appendInline(listNode, task[2]);
          } else {
            appendInline(listNode, item[2]);
          }
          list.append(listNode);
          index += 1;
        }
        container.append(list);
        continue;
      }

      const paragraphLines = [];
      while (index < lines.length && lines[index].trim() && !isBlockStart(lines, index)) {
        paragraphLines.push(lines[index].trim());
        index += 1;
      }
      if (paragraphLines.length === 0) {
        paragraphLines.push(lines[index].trim());
        index += 1;
      }
      const paragraph = document.createElement("p");
      paragraph.className = "markdown-block markdown-paragraph";
      appendInline(paragraph, paragraphLines.join(" "));
      container.append(paragraph);
    }
  }

  function isBlockStart(lines, index) {
    const line = lines[index] || "";
    return blockPatterns.fence.test(line) ||
      blockPatterns.heading.test(line) ||
      blockPatterns.quote.test(line) ||
      blockPatterns.list.test(line) ||
      blockPatterns.rule.test(line) ||
      isTableStart(lines, index);
  }

  function isTableStart(lines, index) {
    return index + 1 < lines.length && lines[index].includes("|") && blockPatterns.tableDivider.test(lines[index + 1]);
  }

  function splitTableRow(line) {
    return line.trim().replace(/^\|/, "").replace(/\|$/, "").split("|").map((cell) => cell.trim());
  }

  function tableAlignment(cell) {
    const value = cell.trim();
    if (value.startsWith(":") && value.endsWith(":")) {
      return "center";
    }
    return value.endsWith(":") ? "right" : "left";
  }

  function createTable(headers, alignments, rows) {
    const wrapper = document.createElement("div");
    wrapper.className = "markdown-block markdown-table-wrap";
    const table = document.createElement("table");
    const head = document.createElement("thead");
    const headRow = document.createElement("tr");
    headers.forEach((header, column) => {
      const cell = document.createElement("th");
      cell.classList.add(`align-${alignments[column] || "left"}`);
      appendInline(cell, header);
      headRow.append(cell);
    });
    head.append(headRow);
    table.append(head);

    const body = document.createElement("tbody");
    rows.forEach((row) => {
      const tableRow = document.createElement("tr");
      headers.forEach((_, column) => {
        const cell = document.createElement("td");
        cell.classList.add(`align-${alignments[column] || "left"}`);
        appendInline(cell, row[column] || "");
        tableRow.append(cell);
      });
      body.append(tableRow);
    });
    table.append(body);
    wrapper.append(table);
    return wrapper;
  }

  function createCodeBlock(language, codeText, notify) {
    const block = document.createElement("div");
    block.className = "markdown-block code-block";
    const head = document.createElement("div");
    head.className = "code-head";
    const label = document.createElement("span");
    label.textContent = language || "code";
    const copy = document.createElement("button");
    copy.type = "button";
    copy.className = "copy-code";
    copy.textContent = "Копировать";
    copy.addEventListener("click", async () => {
      try {
        await navigator.clipboard.writeText(codeText);
        copy.textContent = "Скопировано";
        window.setTimeout(() => { copy.textContent = "Копировать"; }, 1300);
      } catch (error) {
        if (typeof notify === "function") {
          notify("Не удалось скопировать код");
        }
      }
    });
    head.append(label, copy);

    const pre = document.createElement("pre");
    const code = document.createElement("code");
    code.textContent = codeText;
    pre.append(code);
    block.append(head, pre);
    return block;
  }

  function appendInline(parent, source) {
    let remaining = String(source || "");
    while (remaining) {
      const token = nextInlineToken(remaining);
      if (!token) {
        parent.append(document.createTextNode(remaining));
        break;
      }
      if (token.index > 0) {
        parent.append(document.createTextNode(remaining.slice(0, token.index)));
      }
      appendInlineToken(parent, token);
      remaining = remaining.slice(token.index + token.raw.length);
    }
  }

  function nextInlineToken(source) {
    const patterns = [
      { type: "escape", expression: /\\([\\`*_[\]{}()#+\-.!>])/ },
      { type: "code", expression: /`([^`\n]+)`/ },
      { type: "link", expression: /\[([^\]\n]+)\]\(([^)\s]+)\)/ },
      { type: "autolink", expression: /<(https?:\/\/[^>\s]+)>/ },
      { type: "strong", expression: /\*\*(.+?)\*\*/ },
      { type: "strong", expression: /__(.+?)__/ },
      { type: "strike", expression: /~~(.+?)~~/ },
      { type: "emphasis", expression: /\*([^*\n]+)\*/ },
      { type: "emphasis", expression: /_([^_\n]+)_/ }
    ];
    let selected = null;
    patterns.forEach((pattern, priority) => {
      const match = pattern.expression.exec(source);
      if (!match) {
        return;
      }
      if (!selected || match.index < selected.index || (match.index === selected.index && priority < selected.priority)) {
        selected = { type: pattern.type, match, index: match.index, raw: match[0], priority };
      }
    });
    return selected;
  }

  function appendInlineToken(parent, token) {
    if (token.type === "escape") {
      parent.append(document.createTextNode(token.match[1]));
      return;
    }
    if (token.type === "code") {
      const code = document.createElement("code");
      code.className = "inline-code";
      code.textContent = token.match[1];
      parent.append(code);
      return;
    }
    if (token.type === "link" || token.type === "autolink") {
      const label = token.type === "link" ? token.match[1] : token.match[1];
      const href = token.type === "link" ? token.match[2] : token.match[1];
      const safeURL = safeLinkURL(href);
      if (!safeURL) {
        parent.append(document.createTextNode(token.raw));
        return;
      }
      const link = document.createElement("a");
      link.href = safeURL;
      link.target = "_blank";
      link.rel = "noopener noreferrer";
      appendInline(link, label);
      parent.append(link);
      return;
    }

    const tag = token.type === "strong" ? "strong" : token.type === "strike" ? "del" : "em";
    const node = document.createElement(tag);
    appendInline(node, token.match[1]);
    parent.append(node);
  }

  function safeLinkURL(value) {
    try {
      const parsed = new URL(value, window.location.href);
      return ["http:", "https:", "mailto:"].includes(parsed.protocol) ? parsed.href : "";
    } catch (error) {
      return "";
    }
  }

  window.CodexMarkdown = Object.freeze({ render });
})();
