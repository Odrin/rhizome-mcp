# Records site/assets/demo/review-superseded.gif. Run through demo/record.sh
# from the repository root.
Output site/assets/demo/review-superseded.gif

Set Shell bash
Set FontSize 15
Set Width 1000
Set Height 500
Set Padding 16
Set Framerate 15
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "demo/04-review-superseded.sh setup" Enter
Sleep 5s
Show

Type "demo/04-review-superseded.sh request" Enter
Sleep 2.5s
Type "demo/04-review-superseded.sh edit" Enter
Sleep 2.5s
Type "demo/04-review-superseded.sh approve-fails" Enter
Sleep 4s
Type "demo/04-review-superseded.sh replace" Enter
Sleep 5s

Hide
Type "demo/04-review-superseded.sh cleanup" Enter
Sleep 1s
Show
