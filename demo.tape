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
# size these font/window settings give; recapture after changing them, with
# the same question as below (spends one run's requests, Enter at the brief):
#   script -q -T demo.timing -c 'stty cols 113 rows 39; ./deep-research --mode quick -p "..."' demo.tty
Hide
Type `export PS1='$ '; ./deep-research() { scriptreplay -d 4 -t demo.timing demo.tty >/dev/tty; }; clear`
Enter
Wait /\$ ?$/
Show
Sleep 1s

Type `./deep-research --mode quick -p "How do mixture-of-experts LLMs cut inference cost?"`
Sleep 500ms
Enter

Wait /\$ ?$/
Sleep 5s
