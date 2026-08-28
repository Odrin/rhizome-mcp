# Records site/assets/demo/board.png (a still of the terminal status board on
# a seeded project). Run through demo/record.sh from the repository root.
# NOTE: the tape must not end with Hide/Show after Screenshot — vhs then
# silently skips writing the PNG. record.sh runs the cleanup subcommand after.
Output site/assets/demo/board-recording.gif

Set Shell bash
Set FontSize 15
Set Width 1000
Set Height 560
Set Padding 16
Set Framerate 15
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "demo/03-seed-board.sh setup" Enter
Sleep 4s
Show

Type "demo/03-seed-board.sh board" Enter
Sleep 4s
Screenshot site/assets/demo/board.png
Sleep 1s
