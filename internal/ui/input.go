package ui

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

// Key identifies the single-byte keys recognised in interactive mode.
type Key byte

const (
	KeyB     Key = 'b' // send to background
	KeyQ     Key = 'q' // cancel the run
	KeyD     Key = 'd' // change depth mode
	KeyE     Key = 'e' // add a sub-topic
	KeyR     Key = 'r' // rename a sub-topic
	KeyX     Key = 'x' // delete a sub-topic
	KeyEsc   Key = 27  // escape: cancel the run
	KeyCtrlZ Key = 26  // ctrl-z: detach
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
	case KeyB:
		return KeyB
	case KeyQ:
		return KeyQ
	case KeyD:
		return KeyD
	case KeyE:
		return KeyE
	case KeyR:
		return KeyR
	case KeyX:
		return KeyX
	case KeyEsc:
		return KeyEsc
	case KeyCtrlZ:
		return KeyCtrlZ
	case KeyCtrlC:
		return KeyCtrlC
	default:
		return KeyUnknown
	}
}

// readEscape resolves the byte after an Esc. A key sequence (ESC [ ... final,
// ESC O final) is swallowed whole and reported as unknown so an arrow press
// does nothing; a bare Esc, with no byte following it, is the cancel key.
func readEscape(in Input) Key {
	t, ok := in.(timedInput)
	if !ok {
		return KeyEsc
	}
	b, err := t.NextWithin(escapeWindow)
	if err != nil {
		return KeyEsc
	}
	if b != '[' && b != 'O' {
		// ESC followed by something else (alt-<key>): not a cancel, and not a
		// sequence we act on either.
		return KeyUnknown
	}
	// CSI parameter bytes run 0x30-0x3f and intermediates 0x20-0x2f; the
	// sequence ends at the first final byte (0x40-0x7e). ESC O takes exactly
	// one final byte.
	for i := 0; i < 16; i++ {
		c, err := t.NextWithin(escapeWindow)
		if err != nil {
			break
		}
		if b == 'O' || (c >= 0x40 && c <= 0x7e) {
			break
		}
	}
	return KeyUnknown
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
func readPromptSeeded(in Input, seed string, echo func(string)) (string, bool) {
	if seed != "" {
		if echo != nil {
			echo(seed)
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
			echo(line)
		}
		return line, line != ""
	}
	return readPromptRaw(in, nil, echo)
}

// readPromptEdit opens the prompt with current already in the buffer, so a
// rename starts from the old name rather than an empty field. Only raw mode
// can edit that buffer; a line-mode terminal has no per-keystroke echo to edit
// against, so there the reader types a whole fresh line.
func readPromptEdit(in Input, current string, echo func(string)) (string, bool) {
	if !in.Raw() {
		return readPromptSeeded(in, "", echo)
	}
	return readPromptRaw(in, []byte(current), echo)
}

// readPromptRaw runs the keystroke-at-a-time edit loop over buf.
func readPromptRaw(in Input, buf []byte, echo func(string)) (string, bool) {
	show := func() {
		if echo != nil {
			echo(string(buf))
		}
	}
	show()
	for {
		c, err := in.Next()
		if err != nil {
			return strings.TrimSpace(string(buf)), len(buf) > 0
		}
		switch c {
		case '\r', '\n':
			return strings.TrimSpace(string(buf)), true
		case 0x1b: // Esc abandons the edit
			return "", false
		case 0x7f, 0x08: // Backspace deletes a whole rune, not a byte
			if _, n := utf8.DecodeLastRune(buf); n > 0 {
				buf = buf[:len(buf)-n]
			}
		default:
			// Printable bytes only: control codes would corrupt the frame,
			// and UTF-8 continuation bytes (>= 0x80) are appended as they
			// arrive so multi-byte characters survive.
			if c >= 0x20 {
				buf = append(buf, c)
			}
		}
		show()
	}
}
