<!-- codex-ai-review:v1 -->
## AI-ревью кода

Статус: **completed** · модель: gpt-5.3-codex · 3.6 с · 4168 токенов.

Проверен HEAD `d9c8d19a4a67c17104b7e65de7c8d311f894e4dc` относительно `05b157b35e36e100466efa10b8daf96a92e2ca36` (merge base `05b157b35e36e100466efa10b8daf96a92e2ca36`).

RAG: **BM25**, 164 файлов / 1802 чанков базовой ревизии; 9 источников передано модели.

### Потенциальные баги

**[P1] Утечка OPENAI&#95;API&#95;KEY через публичный API статуса** — [internal/web/server.go:542–542 (RIGHT)](https://github.com/lagrange777/aichallenge_hw/blob/d9c8d19a4a67c17104b7e65de7c8d311f894e4dc/internal/web/server.go#L542-L542)

Сценарий: Любой клиент (включая браузерный JS или сторонний сайт при доступном endpoint) вызывает GET /api/status.

Последствие: Секретный ключ OpenAI раскрывается вне сервера, что позволяет несанкционированно использовать API от имени сервиса и приводит к компрометации биллинга/безопасности.

Исправление: Удалить поле APIKey из apiResponse и не включать OPENAI&#95;API&#95;KEY в ответы HTTP. Если нужен признак настройки, возвращать только булев флаг вроде hasApiKey без значения секрета.

Источник [D1 · internal/web/server.go · PR patch (patch line numbers) · 25–27](https://github.com/lagrange777/aichallenge_hw/blob/d9c8d19a4a67c17104b7e65de7c8d311f894e4dc/internal/web/server.go), chunk `diff-1`:

> -	writeJSON(w, http.StatusOK, apiResponse{Model: s.model, Models: s.models})
> +	writeJSON(w, http.StatusOK, apiResponse{Model: s.model, Models: s.models, APIKey: os.Getenv(&#34;OPENAI&#95;API&#95;KEY&#34;)})
>  }

Источник [D1 · internal/web/server.go · PR patch (patch line numbers) · 16–18](https://github.com/lagrange777/aichallenge_hw/blob/d9c8d19a4a67c17104b7e65de7c8d311f894e4dc/internal/web/server.go), chunk `diff-1`:

>  type apiResponse struct {
> + APIKey string &#96;json:&#34;apiKey,omitempty&#34;&#96;
>  	MCP      &#42;mcpView                &#96;json:&#34;mcp,omitempty&#34;&#96;

Источник [R1 · docs/architecture.md · Назначение и структура каталогов · 7–8](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/docs/architecture.md#L7-L8), chunk `51db41edcac623d6`:

> &#96;internal/agent/&#96; управляет диалогом, стратегиями контекста, памятью, инструментами и проверкой ответов. Интерфейс &#96;agent.LLM&#96; отделяет агента от транспорта модели. &#96;internal/openai/&#96; реализует этот интерфейс через OpenAI Responses API. Ключ &#96;OPENAI&#95;API&#95;KEY&#96; остаётся на сервере, в браузер не передаётся. Генерация в этом проекте использует облачную модель OpenAI; это не сервис локальной Qwen из соседнего репозитория.
> 

Источник [R3 · docs/api.md · История и состояние · 23–24](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/docs/api.md#L23-L24), chunk `09975fd3e764dbc5`:

> &#96;GET /api/history&#96; восстанавливает сообщения текущего браузерного диалога. &#96;POST /api/reset&#96; сбрасывает его контекст. Диалоги разделяются cookie &#96;codex&#95;chat&#95;session&#96;. &#96;GET /api/status&#96; возвращает модели и настройки, &#96;GET /healthz&#96; проверяет готовность HTTP-приложения.
> 

### Архитектурные проблемы

В проверенной части обоснованных замечаний нет.

### Рекомендации

В проверенной части обоснованных замечаний нет.

<details><summary>Покрытие и источники RAG</summary>

- M internal/web/server.go — передан diff

Файлов корпуса пропущено: 0.
- [R1 · documentation · docs/architecture.md:3–10](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/docs/architecture.md#L3-L10), chunk `51db41edcac623d6`, BM25 20.37
- [R2 · documentation · ARCHITECTURE.md:24–48](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/ARCHITECTURE.md#L24-L48), chunk `2368b800ffe81791`, BM25 19.94
- [R3 · documentation · docs/api.md:21–28](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/docs/api.md#L21-L28), chunk `09975fd3e764dbc5`, BM25 18.92
- [R4 · documentation · docs/development.md:13–20](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/docs/development.md#L13-L20), chunk `bd31eb135cb8a8a3`, BM25 16.54
- [R5 · code · internal/web/server.go:534–542](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/internal/web/server.go#L534-L542), chunk `01a88a4ea30db896`, BM25 107.49
- [R6 · code · internal/web/server.go:77–90](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/internal/web/server.go#L77-L90), chunk `cdcd45362529897e`, BM25 84.44
- [R7 · code · internal/web/profile.go:13–31](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/internal/web/profile.go#L13-L31), chunk `19195b94cc8d5793`, BM25 48.55
- [R8 · code · internal/web/mcp.go:40–79](https://github.com/lagrange777/aichallenge_hw/blob/05b157b35e36e100466efa10b8daf96a92e2ca36/internal/web/mcp.go#L40-L79), chunk `5dc23ed798c14558`, BM25 45.49

</details>

Автоматическое консультативное ревью. Проверка ссылок и цитат не доказывает правильность вывода модели; замечания проверяет разработчик. Тесты кода PR этим пайплайном не выполняются.
