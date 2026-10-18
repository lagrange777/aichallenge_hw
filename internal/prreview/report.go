package prreview

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func escaped(s string) string {
	s = html.EscapeString(s)
	return strings.NewReplacer("@", "&#64;", "[", "&#91;", "]", "&#93;", "*", "&#42;", "_", "&#95;", "`", "&#96;", "!", "&#33;", "\r", "", "\n", " ").Replace(s)
}
func sourceURL(repo, rev, p string, start, end int) string {
	parts := strings.Split(p, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	u := "https://github.com/" + repo + "/blob/" + rev + "/" + strings.Join(parts, "/")
	if start > 0 {
		u += fmt.Sprintf("#L%d-L%d", start, end)
	}
	return u
}

const Marker = "<!-- codex-ai-review:v1 -->"

func Markdown(r *Report) string {
	var b strings.Builder
	fmt.Fprintln(&b, Marker+"\n## AI-ревью кода")
	fmt.Fprintf(&b, "\nСтатус: **%s** · модель: %s · %.1f с · %d токенов.\n", escaped(r.Status), escaped(r.Model), float64(r.DurationMS)/1000, r.Usage.TotalTokens)
	fmt.Fprintf(&b, "\nПроверен HEAD `%s` относительно `%s` (merge base `%s`).\n", r.Input.Head, r.Input.Base, r.Input.MergeBase)
	fmt.Fprintf(&b, "\nRAG: **BM25**, %d файлов / %d чанков базовой ревизии; %d источников передано модели.\n", r.Input.CorpusFiles, r.Input.CorpusChunks, len(r.Input.Sources))
	if r.Status == "partial" || r.Status == "skipped" {
		fmt.Fprintln(&b, "\n⚠️ Покрытие неполное. Отсутствие замечаний не означает, что пропущенные изменения проверены.")
	}
	if r.Status == "failed" {
		fmt.Fprintln(&b, "\nРевью не завершено: "+escaped(r.Error))
		return b.String()
	}
	refs := map[string]Source{}
	for _, s := range r.Input.Sources {
		refs[s.Ref] = s
	}
	for _, section := range [][2]string{{"bug", "Потенциальные баги"}, {"architecture", "Архитектурные проблемы"}, {"recommendation", "Рекомендации"}} {
		fmt.Fprintln(&b, "\n### "+section[1])
		count := 0
		for _, f := range r.Findings {
			if f.Category != section[0] {
				continue
			}
			count++
			rev, p := r.Input.Head, f.Path
			if f.Side == "LEFT" {
				rev = r.Input.MergeBase
				for _, file := range r.Input.Files {
					if file.Path == p && file.PreviousPath != "" {
						p = file.PreviousPath
						break
					}
				}
			}
			fmt.Fprintf(&b, "\n**[%s] %s** — [%s:%d–%d (%s)](%s)\n\n", f.Priority, escaped(f.Title), escaped(f.Path), f.Start, f.End, f.Side, sourceURL(r.Repository, rev, p, f.Start, f.End))
			fmt.Fprintf(&b, "Сценарий: %s\n\nПоследствие: %s\n\nИсправление: %s\n", escaped(f.Trigger), escaped(f.Impact), escaped(f.Fix))
			for _, e := range f.Evidence {
				s := refs[e.Ref]
				u := sourceURL(r.Repository, s.Revision, s.Path, e.Start, e.End)
				if s.Kind == "diff" {
					// A deleted file does not exist at HEAD; link its immutable diff.
					u = "https://github.com/" + r.Repository + "/compare/" + r.Input.MergeBase + "..." + r.Input.Head
				}
				fmt.Fprintf(&b, "\nИсточник [%s · %s · %s · %d–%d](%s), chunk `%s`:\n", e.Ref, escaped(s.Path), escaped(s.Section), e.Start, e.End, u, s.ChunkID)
				// Preserve +/- signs, indentation and Markdown literally. Escaping
				// HTML and mentions prevents quotes becoming active page content.
				fmt.Fprintf(&b, "\n<blockquote><pre>%s</pre></blockquote>\n", strings.ReplaceAll(html.EscapeString(e.Quote), "@", "&#64;"))
			}
		}
		if count == 0 {
			if r.Status == "skipped" {
				fmt.Fprintln(&b, "\nНе проверено: нет доступных для анализа изменений.")
			} else {
				fmt.Fprintln(&b, "\nВ проверенной части обоснованных замечаний нет.")
			}
		}
	}
	fmt.Fprint(&b, "\n<details><summary>Покрытие и источники RAG</summary>\n\n")
	for _, f := range r.Input.Files {
		status := "передан diff"
		if f.Reason != "" {
			status = "ПРОПУЩЕН: " + f.Reason
		}
		fmt.Fprintf(&b, "- %s %s — %s\n", f.Status, escaped(f.Path), escaped(status))
	}
	fmt.Fprintf(&b, "\nФайлов корпуса пропущено: %d.\n", r.Input.CorpusSkipped)
	for _, s := range r.Input.Sources {
		if s.Kind != "diff" {
			fmt.Fprintf(&b, "- [%s · %s · %s:%d–%d](%s), chunk `%s`, BM25 %.2f\n", s.Ref, escaped(s.Kind), escaped(s.Path), s.Start, s.End, sourceURL(r.Repository, s.Revision, s.Path, s.Start, s.End), s.ChunkID, s.Score)
		}
	}
	fmt.Fprintln(&b, "\n</details>\n\nАвтоматическое консультативное ревью. Проверка ссылок и цитат не доказывает правильность вывода модели; замечания проверяет разработчик. Тесты кода PR этим пайплайном не выполняются.")
	return b.String()
}

func Save(dir string, r *Report) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err = os.WriteFile(filepath.Join(dir, "review.json"), append(data, '\n'), 0644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "review.md"), []byte(Markdown(r)), 0644)
}
