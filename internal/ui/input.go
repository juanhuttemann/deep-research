package ui

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"slices"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

// Key identifies the single-byte keys recognised in interactive mode.
type Key byte

const (
	KeyA   Key = 'a' // show or hide the activity tail
	KeyC   Key = 'c' // answer the clarifying questions again
	KeyQ   Key = 'q' // cancel the run
	KeyD   Key = 'd' // change depth mode
	KeyE   Key = 'e' // add a sub-topic
	KeyF   Key = 'f' // turn the follow-up round on or off
	KeyR   Key = 'r' // rename a sub-topic
	KeyT   Key = 't' // change the report format
	KeyX   Key = 'x' // delete a sub-topic
	KeyEsc Key = 27  // escape: cancel the run
	// KeyCtrlC cancels. Raw mode clears ISIG, so the terminal never turns
	// ^C into SIGINT and it has to be handled here or it does nothing —
	// which is not what anyone who has used a shell expects it to do.
	KeyCtrlC   Key = 3
	KeyUnknown Key = 0xff
)

// escapeWindow is how long a lone Esc waits for the rest of a sequence.
//
// Arrow and function keys arrive as ESC [ A / ESC O P: several bytes, one
// keypress. Nothing separates that leading byte from a real Esc except how
// fast the rest follows, so a short wait tells them apart. It is long enough
// to cover a sequence already sitting in the terminal buffer and short enough
// that Esc still feels immediate.
const escapeWindow = 40 * time.Millisecond

// errNoKeyPending reports that nothing arrived inside the escape window. It
// is here, not with the unix reader that returns it, because the ask box
// waits on it on every platform's build.
var errNoKeyPending = errors.New("no key pending")

// Keystroke is one key press: the key itself plus, in line mode, whatever else
// was typed on the same line. A terminal that could not be switched to raw
// mode delivers "e Performance Benchmarks" as a single line, and the argument
// after the key is part of the same keystroke rather than something to discard.
type Keystroke struct {
	Key  Key
	Rest string
}

// timedInput is an Input that can report whether another byte is already
// available. It is what separates a lone Esc from the start of an escape
// sequence; an Input without it is treated as having nothing pending.
type timedInput interface {
	// NextWithin returns the next byte if one arrives within d, and otherwise
	// an error. It must not consume a byte it does not return.
	NextWithin(d time.Duration) (byte, error)
}

// Input reads keys and lines from the controlling terminal (raw mode) or, for
// tests and non-interactive fallbacks, from an in-memory buffer.
type Input interface {
	// Next returns the next raw byte, or an error at end of input.
	Next() (byte, error)
	// NextLine reads a full line (CR/LF trimmed).
	NextLine() (string, error)
	// Close releases any resources (e.g. restoring terminal mode).
	Close()
	// Raw reports whether single keypresses arrive as they are typed. When
	// false the terminal is still line-buffered and a key only arrives once
	// Enter is pressed, which callers must account for.
	Raw() bool
}

// byteReader is a scripted Input used in tests and non-TTY fallback. It reads
// from a single buffered reader so that Next and NextLine consume a shared
// stream: a key read by Next is not re-read by NextLine.
type byteReader struct {
	scan *bufio.Reader
}

// NewByteReader builds a scripted Input from raw bytes.
func NewByteReader(data []byte) Input {
	return &byteReader{scan: bufio.NewReader(bytes.NewReader(data))}
}

func (b *byteReader) Next() (byte, error) {
	return b.scan.ReadByte()
}

// NextWithin never waits: a scripted stream either has the byte buffered
// already or has ended.
func (b *byteReader) NextWithin(time.Duration) (byte, error) {
	if b.scan.Buffered() == 0 {
		return 0, io.EOF
	}
	return b.scan.ReadByte()
}

func (b *byteReader) NextLine() (string, error) {
	line, err := b.scan.ReadString('\n')
	return string(bytes.TrimRight([]byte(line), "\r\n")), err
}

func (b *byteReader) Close() {}

func (b *byteReader) Raw() bool { return true }

// keyFromByte maps a raw byte to a Key.
func keyFromByte(b byte) Key {
	switch Key(b) {
	case KeyA:
		return KeyA
	case KeyC:
		return KeyC
	case KeyQ:
		return KeyQ
	case KeyD:
		return KeyD
	case KeyE:
		return KeyE
	case KeyF:
		return KeyF
	case KeyR:
		return KeyR
	case KeyT:
		return KeyT
	case KeyX:
		return KeyX
	case KeyEsc:
		return KeyEsc
	case KeyCtrlC:
		return KeyCtrlC
	default:
		return KeyUnknown
	}
}

// readEscape resolves the byte after an Esc. A key sequence (ESC [ ... final,
// ESC O final) is swallowed whole and reported as unknown so an arrow press
// does nothing; a bare Esc, with no byte following it, is the cancel key.
// ESC followed by something else (alt-<key>) is not a cancel, and not a
// sequence we act on either.
func readEscape(in Input) Key {
	lead, final, params := escapeAfter(in)
	if lead == 0 {
		return KeyEsc
	}
	if isPasteStart(lead, final, params) {
		// Text pasted where keys are read is not keys: its x, q and Enter
		// deleted a sub-topic, cancelled or started the run.
		readPaste(in)
	}
	return KeyUnknown
}

// Bracketed paste (enabled with raw mode, see pasteOn) wraps what is pasted
// in ESC [ 200 ~ and ESC [ 201 ~, which is how a paste is told from typing.
const (
	pasteOn    = "\x1b[?2004h"
	pasteOff   = "\x1b[?2004l"
	pasteStart = "\x1b[200~"
	pasteEnd   = "\x1b[201~"
)

// pasteWait is how long a paste's next byte may take before the paste is
// taken as ended: a terminal sends it at once, and one that lost the end
// marker must not leave the box swallowing every key after it.
const pasteWait = 500 * time.Millisecond

// maxPaste bounds what one paste puts in a box.
const maxPaste = 64 << 10

func isPasteStart(lead, final byte, params string) bool {
	return lead == '[' && final == '~' && params == "200"
}

// readPaste reads a bracketed paste's text, after its start marker, up to
// and without its end marker, keeping at most maxPaste bytes of it. Past
// the bound it still reads to the marker: stopping there left the rest of
// the paste to be read as keys, a q in it cancelling the run.
func readPaste(in Input) string {
	t, ok := in.(timedInput)
	if !ok {
		return ""
	}
	var buf []byte
	// tail is the last bytes read, where the end marker is looked for once
	// buf has stopped growing.
	var tail []byte
	for {
		c, err := t.NextWithin(pasteWait)
		if err != nil {
			break
		}
		if len(buf) < maxPaste+len(pasteEnd) {
			buf = append(buf, c)
		}
		tail = append(tail, c)
		if len(tail) > len(pasteEnd) {
			tail = tail[1:]
		}
		if string(tail) == pasteEnd {
			if bytes.HasSuffix(buf, []byte(pasteEnd)) {
				buf = buf[:len(buf)-len(pasteEnd)]
			}
			break
		}
	}
	return string(buf[:min(len(buf), maxPaste)])
}

// pastedText is a paste as one line of a box: its line breaks and tabs
// are spaces, as a question is one line, and copied text ending in a line
// break no longer submitted it before it could be read; other control
// codes, which would corrupt the frame, are dropped.
func pastedText(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	return strings.Join(strings.Fields(s), " ")
}

// escapeAfter reads what follows an Esc: lead is 0 for a bare Esc, '[' or
// 'O' for a key sequence, whose remaining bytes it swallows and whose final
// byte it returns ('A' up, 'B' down) with its parameters ("1;5" for a
// Ctrl-arrow, "3" for Delete's ESC [ 3 ~), or an alt-<key>'s key.
func escapeAfter(in Input) (lead, final byte, params string) {
	t, ok := in.(timedInput)
	if !ok {
		return 0, 0, ""
	}
	b, err := t.NextWithin(escapeWindow)
	if err != nil {
		return 0, 0, ""
	}
	if b != '[' && b != 'O' {
		return b, 0, ""
	}
	// CSI parameter bytes run 0x30-0x3f and intermediates 0x20-0x2f; the
	// sequence ends at the first final byte (0x40-0x7e). ESC O takes exactly
	// one final byte.
	var ps []byte
	for i := 0; i < 16; i++ {
		c, err := t.NextWithin(escapeWindow)
		if err != nil {
			break
		}
		if b == 'O' || (c >= 0x40 && c <= 0x7e) {
			return b, c, string(ps)
		}
		ps = append(ps, c)
	}
	return b, 0, string(ps)
}

// readKey reads one keystroke.
//
// In raw mode that is a byte, with multi-byte escape sequences resolved so an
// arrow key is not mistaken for Esc. In line mode the terminal delivers a whole
// line at once: its first character is the key, the rest is the key's argument,
// and the trailing newline is consumed with it — otherwise every key would be
// followed by an Enter that launches the run before its effect could be seen.
func readKey(in Input) (Keystroke, error) {
	if !in.Raw() {
		line, err := in.NextLine()
		if err != nil && line == "" {
			return Keystroke{}, err
		}
		if line == "" {
			return Keystroke{Key: Key('\r')}, nil
		}
		return Keystroke{
			Key:  keyOrLiteral(line[0]),
			Rest: strings.TrimSpace(line[1:]),
		}, nil
	}
	b, err := in.Next()
	if err != nil {
		return Keystroke{}, err
	}
	if Key(b) == KeyEsc {
		return Keystroke{Key: readEscape(in)}, nil
	}
	return Keystroke{Key: keyOrLiteral(b)}, nil
}

// keyOrLiteral keeps the bytes the brief loop matches directly (Enter, CR, NUL)
// intact while mapping everything else through the recognised-key table.
func keyOrLiteral(b byte) Key {
	switch b {
	case '\r', '\n', 0x00:
		return Key(b)
	}
	if k := keyFromByte(b); k != KeyUnknown {
		return k
	}
	return KeyUnknown
}

// keyPoll is how long one wait for a key lasts before the reader checks
// whether it was closed, or its run ended: the most a Close waits for a read
// in progress.
const keyPoll = 100 * time.Millisecond

// ctxInput is an Input that ends, as at the end of input, once ctx is done.
// A picker waiting for a key took no notice of the run's deadline or of a
// cancel, and the process sat on the screen past both until a key came. A
// reader that cannot wait in steps (a cooked terminal) still blocks.
type ctxInput struct {
	Input
	ctx context.Context
}

func (c ctxInput) Next() (byte, error) {
	// Only a terminal waits in steps: a scripted reader's NextWithin ends
	// the input at a byte not yet buffered.
	t, ok := c.Input.(timedInput)
	if _, tty := c.Input.(terminalOwner); !ok || !tty {
		return c.Input.Next()
	}
	for {
		if c.ctx.Err() != nil {
			return 0, io.EOF
		}
		b, err := t.NextWithin(keyPoll)
		if !errors.Is(err, errNoKeyPending) {
			return b, err
		}
	}
}

func (c ctxInput) NextWithin(d time.Duration) (byte, error) {
	if c.ctx.Err() != nil {
		return 0, io.EOF
	}
	if t, ok := c.Input.(timedInput); ok {
		return t.NextWithin(d)
	}
	return 0, errNoKeyPending
}

// lineReader reads from a terminal that could not be put in raw mode. Keys
// still work, but only once Enter is pressed, so Raw reports false and callers
// treat a whole line as one keystroke.
type lineReader struct{ scan *bufio.Reader }

func (l *lineReader) Next() (byte, error) { return l.scan.ReadByte() }

func (l *lineReader) NextLine() (string, error) {
	line, err := l.scan.ReadString('\n')
	return string(bytes.TrimRight([]byte(line), "\r\n")), err
}

func (l *lineReader) Close()    {}
func (l *lineReader) Raw() bool { return false }

// NewInput returns an Input for the given reader.
//
// On a terminal it is opened in raw mode so single keypresses arrive as they
// are typed. When raw mode is unavailable the terminal is still usable — it
// falls back to reading whole lines rather than to a dead reader, because a
// reader that only returns EOF makes every key silently do nothing. warn, when
// non-nil, is told why the downgrade happened.
func NewInput(in io.Reader, warn func(string)) Input {
	f, ok := in.(*os.File)
	if !ok {
		return NewByteReader(nil)
	}
	r, err := newTTYInput(f)
	if err == nil {
		return r
	}
	if !IsTTYFile(f) {
		return NewByteReader(nil)
	}
	if warn != nil {
		warn("could not switch the terminal to raw mode (" + err.Error() +
			"); keys need Enter after them")
	}
	return &lineReader{scan: bufio.NewReader(f)}
}

var _ Input = (*byteReader)(nil)

// terminalOwner is an Input that put the terminal in raw mode and has to give
// it back however the process ends. Only the unix tty reader is one: a
// scripted reader reports Raw too, with no terminal behind it.
type terminalOwner interface{ ownsTerminal() }

// catchSignals starts holding the signals whose default action ends the
// process, before the terminal goes raw: one arriving between raw mode and
// guardTerminal would otherwise end the run with the terminal still raw. It
// holds nothing when no terminal will be taken.
func catchSignals(interactive bool) chan os.Signal {
	if !interactive {
		return nil
	}
	sig := make(chan os.Signal, 1)
	// SIGQUIT too: its default action dumps the goroutines and exits with no
	// defer run, raw mode and the hidden cursor left behind. Raw mode makes
	// Ctrl-\ a key, so it comes from outside, as the others do.
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	return sig
}

// guardTerminal takes over the signals catchSignals holds. Raw mode makes
// Ctrl-C a key, so a signal here comes from outside (kill, timeout(1), a
// closed terminal window), and its default action exits without running a
// single defer: the shell was left with no echo, no line editing and no
// cursor. So the terminal, and the cursor the renderer hid, are given back
// first; then the signal is raised again with its default action, and the
// process still ends by it with an exit status that says so.
//
// stop restores as well before it stops listening, so a signal arriving
// between the two finds the terminal already given back.
func guardTerminal(sig chan os.Signal, input Input, r *Renderer) (stop func()) {
	if sig == nil {
		return func() {}
	}
	restore := func() {}
	if _, ok := input.(terminalOwner); ok {
		restore = func() { input.Close(); r.Close() }
	}
	done := make(chan struct{})
	go func() {
		select {
		case s := <-sig:
			restore()
			signal.Reset(s)
			if p, err := os.FindProcess(os.Getpid()); err != nil || p.Signal(s) != nil {
				os.Exit(1)
			}
		case <-done:
		}
	}()
	return func() {
		restore()
		signal.Stop(sig)
		close(done)
	}
}

// readPromptSeeded reads a line of typed input, reporting it to echo after
// every keystroke. Raw mode disables the terminal's own echo, so a prompt that
// does not draw what is being typed leaves the reader entering text into what
// looks like a frozen screen.
//
// It returns the text and whether it was confirmed: Enter confirms, Esc
// abandons it. On end of input whatever was typed is kept, which is what makes
// scripted input ("eSome topic\n") behave the same as a person typing.
//
// seed is the text already in hand. In line mode the argument arrives on the
// same line as the key that opened the prompt, so it is the answer and there
// is nothing left to read.
func readPromptSeeded(in Input, seed string, echo func(text string, cursor int)) (string, bool) {
	if seed != "" {
		if echo != nil {
			echo(seed, len(seed))
		}
		return strings.TrimSpace(seed), true
	}
	if !in.Raw() {
		line, err := in.NextLine()
		line = strings.TrimSpace(line)
		if err != nil && line == "" {
			return "", false
		}
		if echo != nil {
			echo(line, len(line))
		}
		return line, line != ""
	}
	return readPromptRaw(in, nil, nil, echo, nil, nil)
}

// readPromptEdit opens the prompt with current already in the buffer, so a
// rename starts from the old name rather than an empty field. Only raw mode
// can edit that buffer; a line-mode terminal has no per-keystroke echo to edit
// against, so there the reader types a whole fresh line.
func readPromptEdit(in Input, current string, echo func(text string, cursor int)) (string, bool) {
	if !in.Raw() {
		return readPromptSeeded(in, "", echo)
	}
	return readPromptRaw(in, []byte(current), nil, echo, nil, nil)
}

// readPromptRaw runs the keystroke-at-a-time edit loop over buf, with a
// cursor the arrows move: a typo early in a long question used to mean
// deleting everything after it. tab, when set, turns the line into what Tab
// makes of it: the start screen's takes its example, as the page's question
// box does, or completes a command. arrow, when set, is handed the final
// byte of each key sequence the line does not use (up and down, which move
// a menu). echo is given the line and the cursor's byte offset in it.
// quit, when set, makes Ctrl-C clear the line and leave only when pressed
// on an empty one twice; without it Ctrl-C leaves at once, as Esc does.
func readPromptRaw(in Input, buf []byte, tab func(string) string, echo func(text string, cursor int), arrow func(byte), quit *twice) (string, bool) {
	l := &lineEdit{buf: buf, cur: len(buf)}
	show := func() {
		if echo != nil {
			echo(string(l.buf), l.cur)
		}
	}
	show()
	for {
		c, err := in.Next()
		if err != nil {
			return strings.TrimSpace(string(l.buf)), len(l.buf) > 0
		}
		if c != byte(KeyCtrlC) {
			quit.disarm()
		}
		switch c {
		case '\r', '\n':
			return strings.TrimSpace(string(l.buf)), true
		case 0x1b:
			if !l.afterEsc(in, arrow) {
				return "", false
			}
		case byte(KeyCtrlC): // as Esc: raw mode makes it a key
			if quit.press(l) {
				return "", false
			}
		case 0x04: // Ctrl-D ends an empty line, as at a shell; else deletes forward
			if len(l.buf) == 0 {
				return "", false
			}
			l.deleteForward()
		default:
			l.edit(c, tab)
			if l.midRune() {
				// Drawn now, the frame would hold a lone lead byte: invalid
				// UTF-8 until the rest of the character arrives.
				continue
			}
		}
		show()
	}
}

// midRune reports whether the cursor sits after the first bytes of a
// character still arriving.
func (l *lineEdit) midRune() bool {
	i := l.cur - 1
	for i > 0 && !utf8.RuneStart(l.buf[i]) {
		i--
	}
	return i >= 0 && !utf8.FullRune(l.buf[i:l.cur])
}

// afterEsc applies what follows an Esc, and reports false for an Esc that
// leaves the line. A key sequence starts with Esc, as an Alt-<key> and a
// paste do; taken as Esc, each threw the typed line away.
func (l *lineEdit) afterEsc(in Input, arrow func(byte)) bool {
	lead, final, params := escapeAfter(in)
	switch {
	case lead != 0 && l.escape(lead, final, params):
	case isPasteStart(lead, final, params):
		l.insert(pastedText(readPaste(in)))
	case lead == '[' || lead == 'O':
		if arrow != nil {
			arrow(final)
		}
	default:
		// A bare Esc, or Esc and a key the line does not use (Esc then
		// Enter typed fast reads as Alt-Enter): Esc, as it was.
		return false
	}
	return true
}

// twice is Ctrl-C in a box that leaves on it only twice over an empty line:
// once was all it took, and a Ctrl-C meant to clear a line, as at a shell,
// threw the session away. armed is told when the next press will leave and
// when a key other than Ctrl-C makes it not.
type twice struct {
	armed bool
	show  func(armed bool)
}

// press handles a Ctrl-C on l and reports whether it leaves the box. A nil
// twice always leaves.
func (t *twice) press(l *lineEdit) bool {
	switch {
	case t == nil || t.armed:
		return true
	case len(l.buf) > 0:
		l.cut(0, len(l.buf))
	default:
		t.armed = true
		t.show(true)
	}
	return false
}

func (t *twice) disarm() {
	if t != nil && t.armed {
		t.armed = false
		t.show(false)
	}
}

// lineEdit is a line being typed and the cursor in it, a byte offset on a
// rune boundary.
type lineEdit struct {
	buf []byte
	cur int
}

// edit applies a key that edits the line rather than ends it.
func (l *lineEdit) edit(c byte, tab func(string) string) {
	switch c {
	case '\t':
		if tab != nil {
			l.buf = []byte(tab(string(l.buf)))
			l.cur = len(l.buf)
		}
	case 0x7f, 0x08: // Backspace deletes the whole rune before the cursor
		_, n := utf8.DecodeLastRune(l.buf[:l.cur])
		l.cut(l.cur-n, l.cur)
	case 0x01: // Ctrl-A, Ctrl-E: start and end, as at a shell
		l.cur = 0
	case 0x05:
		l.cur = len(l.buf)
	case 0x02: // Ctrl-B, Ctrl-F: back and forward a character
		l.left()
	case 0x06:
		l.right()
	case 0x15: // Ctrl-U, Ctrl-K: delete to the start, to the end
		l.cut(0, l.cur)
	case 0x0b:
		l.cut(l.cur, len(l.buf))
	case 0x17: // Ctrl-W deletes the word before the cursor
		l.cut(l.wordLeft(), l.cur)
	default:
		// Printable bytes only: control codes would corrupt the frame. A
		// multi-byte character's bytes arrive one after another and are
		// inserted in turn, so it lands whole at the cursor.
		if c >= 0x20 {
			l.buf = slices.Insert(l.buf, l.cur, c)
			l.cur++
		}
	}
}

// escape applies a key sequence, or an Alt-<key>, and reports whether the
// line used it.
func (l *lineEdit) escape(lead, final byte, params string) bool {
	if lead != '[' && lead != 'O' {
		return l.altKey(lead)
	}
	return l.sequence(final, params)
}

// altKey is Alt-B, Alt-F and Alt-Backspace: what macOS terminals send for
// Option with the arrows and Backspace.
func (l *lineEdit) altKey(k byte) bool {
	switch k {
	case 'b':
		l.cur = l.wordLeft()
	case 'f':
		l.cur = l.wordRight()
	case 0x7f:
		l.cut(l.wordLeft(), l.cur)
	default:
		return false
	}
	return true
}

// sequence is an arrow, Home, End or Delete. A modified arrow carries its
// modifier as the last parameter (ESC [ 1 ; 5 D is Ctrl-Left): any modifier
// moves by word.
func (l *lineEdit) sequence(final byte, params string) bool {
	if final == '~' {
		return l.tilde(params)
	}
	byWord := strings.Contains(params, ";")
	switch {
	case final == 'D' && byWord:
		l.cur = l.wordLeft()
	case final == 'C' && byWord:
		l.cur = l.wordRight()
	case final == 'D':
		l.left()
	case final == 'C':
		l.right()
	case final == 'H':
		l.cur = 0
	case final == 'F':
		l.cur = len(l.buf)
	default:
		return false
	}
	return true
}

// tilde is the ESC [ n ~ keys: Home (1, 7), End (4, 8) and Delete (3).
func (l *lineEdit) tilde(params string) bool {
	switch params {
	case "1", "7":
		l.cur = 0
	case "4", "8":
		l.cur = len(l.buf)
	case "3":
		l.deleteForward()
	default:
		return false
	}
	return true
}

// insert puts text in at the cursor and leaves the cursor after it.
func (l *lineEdit) insert(text string) {
	l.buf = slices.Insert(l.buf, l.cur, []byte(text)...)
	l.cur += len(text)
}

func (l *lineEdit) left() {
	_, n := utf8.DecodeLastRune(l.buf[:l.cur])
	l.cur -= n
}

func (l *lineEdit) right() {
	_, n := utf8.DecodeRune(l.buf[l.cur:])
	l.cur += n
}

func (l *lineEdit) deleteForward() {
	_, n := utf8.DecodeRune(l.buf[l.cur:])
	l.cut(l.cur, l.cur+n)
}

// cut deletes buf[from:to] and leaves the cursor where it was.
func (l *lineEdit) cut(from, to int) {
	l.buf = slices.Delete(l.buf, from, to)
	l.cur = from
}

// wordLeft is the start of the word before the cursor, past any spaces.
func (l *lineEdit) wordLeft() int {
	i := l.cur
	for i > 0 && l.buf[i-1] == ' ' {
		i--
	}
	for i > 0 && l.buf[i-1] != ' ' {
		i--
	}
	return i
}

// wordRight is the end of the word after the cursor, past any spaces.
func (l *lineEdit) wordRight() int {
	i := l.cur
	for i < len(l.buf) && l.buf[i] == ' ' {
		i++
	}
	for i < len(l.buf) && l.buf[i] != ' ' {
		i++
	}
	return i
}
