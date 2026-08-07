package cli

import (
	"html/template"
)

var boardTemplates = mustParseBoardTemplates()

func mustParseBoardTemplates() *template.Template {
	tmpl := template.New("board")
	parsed, err := tmpl.ParseFS(boardAssetsFS, "board_assets/*.tmpl")
	if err != nil {
		panic(err)
	}
	return parsed
}
