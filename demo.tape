Output demo.gif

Set Shell "bash"
Set FontSize 16
Set Width 1200
Set Height 800
Set Theme "Dracula"
Set Padding 20
Set Framerate 25
Set WaitTimeout 3m

# Replays a captured run instead of running one: no model requests, and the
# ~3.5 min run plays 4x faster. Recording it live at full length filled
# tmpfs /tmp with frames and froze the machine. The capture is 113x39, the
# size these font/window settings give; recapture after changing them (spends
# one run's requests), from an empty directory so no past run shows:
#   script -q -T demo.timing -c 'stty cols 113 rows 39; ./drsh' demo.tty
# then at the start screen type /mode quick, Enter, the question
# "How do mixture-of-experts LLMs cut inference cost?", Enter; Enter at the
# brief; and Ctrl-D at ask ▸ once the report is in. Then add 8 seconds to
# the first delay over 2.5 s in demo.timing, the pause before /mode, so the
# start screen's default settings row stays up long enough to read.
# The command is typed while hidden and the replay clears the screen first,
# so the recording opens on the start screen rather than on a prompt.
Hide
Type `export PS1='$ '; ./drsh() { clear; sleep 0.5; scriptreplay -d 4 -t demo.timing demo.tty >/dev/tty; }; clear`
Enter
Wait /\$ ?$/
Type `./drsh`
Enter
Sleep 300ms
Show

Wait /\$ ?$/
Sleep 5s
