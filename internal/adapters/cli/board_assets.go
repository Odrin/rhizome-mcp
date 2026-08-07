package cli

import "embed"

//go:embed board_assets/board.css
//go:embed board_assets/board_live.js
//go:embed board_assets/board_search.js
//go:embed board_assets/*.tmpl
var boardAssetsFS embed.FS

var (
	boardHTMLStyle         = mustReadBoardAsset("board_assets/board.css")
	boardLiveRefreshScript = mustReadBoardAsset("board_assets/board_live.js")
	boardSearchScript      = mustReadBoardAsset("board_assets/board_search.js")
)

func mustReadBoardAsset(path string) string {
	contents, err := boardAssetsFS.ReadFile(path)
	if err != nil {
		panic(err)
	}
	return string(contents)
}
