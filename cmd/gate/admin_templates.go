package main

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"strings"
)

//go:embed templates/admin-*.html
var adminTemplateFS embed.FS

var adminTemplates = template.Must(template.New("admin").Funcs(template.FuncMap{
	"psQuote": func(value string) string { return "'" + strings.ReplaceAll(value, "'", "''") + "'" },
}).ParseFS(adminTemplateFS, "templates/admin-*.html"))

func renderAdminTemplate(w io.Writer, name string, data any) error {
	if err := adminTemplates.ExecuteTemplate(w, name, data); err != nil {
		return fmt.Errorf("render admin template %q: %w", name, err)
	}
	return nil
}
