# Records site/assets/demo/reservation-conflict.gif. Run through
# demo/record.sh from the repository root.
Output site/assets/demo/reservation-conflict.gif

Set Shell bash
Set FontSize 15
Set Width 1000
Set Height 500
Set Padding 16
Set Framerate 15
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "demo/02-reservation-conflict.sh setup" Enter
Sleep 4s
Show

Type "demo/02-reservation-conflict.sh claim-a" Enter
Sleep 2.5s
Type "demo/02-reservation-conflict.sh claim-b" Enter
Sleep 4.5s
Type "demo/02-reservation-conflict.sh claim-b-retry" Enter
Sleep 5s

Hide
Type "demo/02-reservation-conflict.sh cleanup" Enter
Sleep 1s
Show
