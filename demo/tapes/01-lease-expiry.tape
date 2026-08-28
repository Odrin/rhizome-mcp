# Records site/assets/demo/lease-expiry.gif. Run through demo/record.sh from
# the repository root (it builds the binary and sets the working directory).
Output site/assets/demo/lease-expiry.gif

Set Shell bash
Set FontSize 15
Set Width 1000
Set Height 560
Set Padding 16
Set Framerate 15
Set TypingSpeed 40ms
Set Theme "Catppuccin Mocha"

Hide
Type "demo/01-lease-expiry.sh setup" Enter
Sleep 4s
Show

Type "demo/01-lease-expiry.sh claim-a" Enter
Sleep 2.5s
Type "demo/01-lease-expiry.sh claim-b" Enter
Sleep 3s
Type "demo/01-lease-expiry.sh checkpoint" Enter
Sleep 3.5s
Type "demo/01-lease-expiry.sh board" Enter
Sleep 4s

Hide
Sleep 66s
Show

Type "# 60 seconds later: the lease has expired on its own" Enter
Sleep 1.5s
Type "demo/01-lease-expiry.sh reclaim" Enter
Sleep 6s

Hide
Type "demo/01-lease-expiry.sh cleanup" Enter
Sleep 1s
Show
