package ui

import (
	"context"
	"strings"
	"testing"

	"github.com/juanhuttemann/deep-research/internal/agent"
)

func quotePages(texts ...string) map[string]*pageText {
	pages := map[string]*pageText{}
	for i, t := range texts {
		pages[string(rune('a'+i))] = newPageText(t)
	}
	return pages
}

// src is the link that attributes a quotation to a source.
const src = " ([src](https://a.example/))"

func TestUnquoteUnfoundKeepsQuotesInAPageAndUnquotesTheRest(t *testing.T) {
	pages := quotePages(
		"When called as seq(yield), seq calls yield(v) for each value v in the sequence, stopping early if yield returns false.",
		"However, it's a slightly better value for money , as it's $90.00 cheaper! The RAM crunch is still bleeding into GPU street prices.",
		"The time (in seconds) the connection needs to remain idle before TCP starts sending keepalive probes.",
		`Taylor stated that there was "no evidence for a global saving glut". Before MERGE, developers relied on INSERT ... ON CONFLICT DO UPDATE for upserts.`,
		"Using a managed service significantly reduces operational overhead by removing the need for manual installation. "+
			"Edge options exist but require more thought about where your primary is. One ended the Dutch Revolt, creating a powerful, independent Dutch Republic.",
	)
	for _, tc := range []struct{ name, report, want string }{
		{"verbatim", `The docs say "seq calls yield(v) for each value v in the sequence"` + src + `.`,
			`The docs say "seq calls yield(v) for each value v in the sequence"` + src + `.`},
		{"invented", `The docs say "A Seq stops early if yield returns false"` + src + `.`,
			`The docs say A Seq stops early if yield returns false` + src + `.`},
		{"curly invented", "It is “an LLM-powered search and answer engine” today" + src + ".",
			"It is an LLM-powered search and answer engine today" + src + "."},
		{"trailing punctuation", `"stopping early if yield returns false," it says` + src + `.`,
			`"stopping early if yield returns false," it says` + src + `.`},
		{"bracketed letter", `"[t]he time (in seconds) the connection needs to remain idle"` + src,
			`"[t]he time (in seconds) the connection needs to remain idle"` + src},
		{"capital the sentence needed", `"Stopping early if yield returns false" is the rule` + src + `.`,
			`"Stopping early if yield returns false" is the rule` + src + `.`},
		// A bracketed edit stands for the word or two it supplies or changes.
		{"letters added in brackets", `Edge Postgres "require[s] more thought about where your primary is"` + src + `.`,
			`Edge Postgres "require[s] more thought about where your primary is"` + src + `.`},
		{"letters removed, marked by empty brackets", `It will "significantly reduce[] operational overhead by removing the need"` + src + `.`,
			`It will "significantly reduce[] operational overhead by removing the need"` + src + `.`},
		{"a word supplied in brackets", `One treaty "[ended] the Dutch Revolt, creating a powerful, independent Dutch Republic"` + src + `.`,
			`One treaty "[ended] the Dutch Revolt, creating a powerful, independent Dutch Republic"` + src + `.`},
		{"brackets do not join distant passages", `It "significantly reduce[d] the Dutch Revolt, creating a powerful"` + src + `.`,
			`It significantly reduce[d] the Dutch Revolt, creating a powerful` + src + `.`},
		{"elision of found pieces", `"it's a slightly better value for money … $90.00 cheaper!"` + src,
			`"it's a slightly better value for money … $90.00 cheaper!"` + src},
		{"elision with an invented piece", `"it's a slightly better value for money … and the fastest card sold"` + src,
			`it's a slightly better value for money … and the fastest card sold` + src},
		{"space before a comma on the page", `"better value for money, as it's $90.00 cheaper"` + src,
			`"better value for money, as it's $90.00 cheaper"` + src},
		{"the other quotation mark nested inside", `Taylor "stated that there was 'no evidence for a global saving glut'"` + src + `.`,
			`Taylor "stated that there was 'no evidence for a global saving glut'"` + src + `.`},
		{"an ellipsis inside a code span is code", "\"Before MERGE, developers relied on `INSERT ... ON CONFLICT DO UPDATE` for upserts\"" + src,
			"\"Before MERGE, developers relied on `INSERT ... ON CONFLICT DO UPDATE` for upserts\"" + src},
		{"a phrase named, not quoted", `a source that answers "which boards are popular" instead` + src,
			`a source that answers "which boards are popular" instead` + src},
		// Without a link in its sentence a quoted span claims no source's
		// words: it names a phrase or a position.
		{"a named phrase with no link", `Neither a confident "start with SQLite and migrate later" nor the opposite follows.`,
			`Neither a confident "start with SQLite and migrate later" nor the opposite follows.`},
		{"a link in the previous sentence does not attribute", `See it` + src + `. The rule is "always start with SQLite and migrate later".`,
			`See it` + src + `. The rule is "always start with SQLite and migrate later".`},
		{"a table cell attributed by the next cell", `| Origin | "widgets will double in price next year" | [src](https://a.example/) |`,
			`| Origin | widgets will double in price next year | [src](https://a.example/) |`},
		// A short quote before an invented one: paired with a minimum length,
		// the short one's closing mark opened the next span, and the invented
		// quote after it was never checked.
		{"a short quote before an invented one", `The "x" option means the docs say "widgets will double in price next year"` + src + `.`,
			`The "x" option means the docs say widgets will double in price next year` + src + `.`},
		{"a short quote before an invented one, across a link", `"x" ([a](https://a.example/)) and later "y is shown on no page at all"` + src,
			`"x" ([a](https://a.example/)) and later y is shown on no page at all` + src},
		// Pieces of an elided quote found on different pages, or out of
		// order, vouched for a sentence no page has.
		{"elided pieces from two pages", `"The time (in seconds) the connection needs … the RAM crunch is still bleeding into GPU street prices"` + src,
			`The time (in seconds) the connection needs … the RAM crunch is still bleeding into GPU street prices` + src},
		{"elided pieces out of order", `"before TCP starts sending keepalive probes … the connection needs to remain idle"` + src,
			`before TCP starts sending keepalive probes … the connection needs to remain idle` + src},
		{"a one-word piece after an elision is checked too", `"it's a slightly better value for money … infinite"` + src,
			`it's a slightly better value for money … infinite` + src},
		// Quotation marks that are syntax are not quoting.
		{"code span", "Call `fmt.Println(\"please read the documentation before you continue\")` first" + src + ".",
			"Call `fmt.Println(\"please read the documentation before you continue\")` first" + src + "."},
		{"fenced code", "```go\nlog.Print(\"please read the documentation before you continue\")\n```",
			"```go\nlog.Print(\"please read the documentation before you continue\")\n```"},
		{"link title", `See [docs](https://a.example/x "read the full documentation for this option").`,
			`See [docs](https://a.example/x "read the full documentation for this option").`},
		{"a code span inside an invented quote", "The guide says \"set `keepalive` to on and never touch it again\"" + src + " and \"x\".",
			"The guide says set `keepalive` to on and never touch it again" + src + " and \"x\"."},
	} {
		got, _ := unquoteUnfound(tc.report, pages)
		if got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, got, tc.want)
		}
	}
}

// Each case is a way a quotation was checked wrongly in review: code that
// lost its quotation marks, code read as a gap, bold or a link that kept a
// quotation from being checked, an abbreviation that cut a quote off from
// its link.
func TestUnquoteUnfoundHoldsCodeFormattingAndAttribution(t *testing.T) {
	for _, tc := range []struct {
		name, report string
		pages        []string
		removed      int
	}{
		{"double-backtick code", "Use ``fmt.Println(\"please read the documentation before you continue\")``" + src, []string{"unrelated"}, 0},
		{"code span over two lines", "Use `fmt.Println(\"please read the documentation before you continue\") // [src](https://a.example/)\n`", []string{"unrelated"}, 0},
		{"tilde fence", "~~~go\nfmt.Println(\"please read the documentation before you continue\") // [src](https://a.example/)\n~~~", []string{"unrelated"}, 0},
		{"an ellipsis in code is literal", "\"Before MERGE, developers relied on `INSERT ... ON CONFLICT DO UPDATE` for upserts\"" + src,
			[]string{"Before MERGE, developers relied on INSERT arbitrary fabricated text ON CONFLICT DO UPDATE for upserts"}, 1},
		{"brackets in code are literal", "\"The value at `data[0]` must always remain zero\"" + src, []string{"The value at data[9] must always remain zero"}, 1},
		{"bold invented words", `The docs say "widgets will **double** in price next year"` + src, []string{"unrelated"}, 1},
		{"bold verbatim words", `The docs say "widgets will **double** in price next year"` + src, []string{"Widgets will double in price next year."}, 0},
		{"a verbatim quote with links in it", `"It is the [angle](https://en.wikipedia.org/wiki/Angle "Angle") between the orbital plane and the plane"` + src,
			[]string{"It is the [angle](https://en.wikipedia.org/wiki/Angle \"Angle\") between the orbital plane and the plane of reference."}, 0},
		{"a link inside the quote", `The docs say "widgets will [double](https://a.example/) in price next year".`, []string{"unrelated"}, 1},
		{"a link before an abbreviation", `[src](https://a.example/) says the U.S. agency reports "widgets will double in price next year".`, []string{"unrelated"}, 1},
		{"a link before an abbreviation and a capital", `[src](https://a.example/) says the U.S. Department of Energy reports "widgets will double in price next year".`, []string{"unrelated"}, 1},
		{"a link in the sentence before still does not attribute", `It is in [src](https://a.example/). The U.S. Department of Energy reports "widgets will double in price next year".`, []string{"unrelated"}, 0},
		{"multibyte text around quotes", "日é “widgets will double in price next year”" + src + " ö \"widgets will triple in price next year\"" + src, []string{"unrelated"}, 2},
		{"an escaped backtick before code", "Type \\` then use `fmt.Println(\"please read the documentation before you continue\")`" + src, []string{"unrelated"}, 0},
		{"a four-backtick fence around a three-backtick one",
			"````markdown\n```go\nfmt.Println(\"please read the documentation before you continue\") // [src](https://a.example/)\n```\n````", []string{"unrelated"}, 0},
		// Code is held where the quote is, not anywhere on the page.
		{"code found only in another passage", "\"The value at `data[0]` must always remain zero\"" + src,
			[]string{"The value at data[9] must always remain zero. Elsewhere, print data[0]."}, 1},
		// In Markdown code an entity is its characters: printf '&amp;' prints
		// &amp;. Prose entities are decoded.
		{"an entity in the page's code is not its character", "\"The condition `x < 5` must always remain true\"" + src,
			[]string{"The condition `x &lt; 5` must always remain true"}, 1},
		{"an entity in the quote's code is not its character", "\"Run `printf '&amp;'` to print an ampersand sign\"" + src,
			[]string{"Run `printf '&'` to print an ampersand sign"}, 1},
		{"an entity in prose around code is", "\"Use `ls` for files &amp; folders in the shell\"" + src,
			[]string{"Use `ls` for files & folders in the shell"}, 0},
		// A page is read as it is: a word put before it to read a fragment
		// safely turned an opening code block into prose.
		{"a page that opens with a code block", "\"The expression `ptr` must always remain unchanged\"" + src,
			[]string{"    The expression *ptr* must always remain unchanged"}, 1},
		{"a page that opens with a fence", "\"The expression `ptr` must always remain unchanged\"" + src,
			[]string{"~~~\nThe expression *ptr* must always remain unchanged\n~~~"}, 1},
		{"code in backticks on the page", "\"The value at `data[0]` must always remain zero\"" + src,
			[]string{"The value at `data[0]` must always remain zero"}, 0},
		// Two edits in a row: the first place the middle piece is found can be
		// too far from the last piece while a second place in reach is not.
		{"two bracketed edits in a row", `"The system [now] uses [a] reliable storage for all users"` + src,
			[]string{"The system uses old code and uses highly reliable storage for all users"}, 0},
		{"a link before a title", `[src](https://a.example/) says Dr. Smith reports "widgets will double in price next year".`, []string{"unrelated"}, 1},
		// Code is compared as the characters it is: as visible text, | read
		// as a table bar, *x* as emphasis and \\* as an escape.
		{"a bar in code", "\"The expression `a | b` must always remain unchanged\"" + src, []string{"The expression a b must always remain unchanged"}, 1},
		{"asterisks in code", "\"The expression `*ptr*` must always remain unchanged\"" + src, []string{"The expression ptr must always remain unchanged"}, 1},
		{"an escape in code", "\"The expression `a\\*b` must always remain unchanged\"" + src, []string{"The expression a*b must always remain unchanged"}, 1},
		// Code is kept as written, link syntax and backticks inside it too.
		{"link syntax in code", "\"The expression ``[x](y)`` must always remain unchanged\"" + src, []string{"The expression x must always remain unchanged"}, 1},
		{"a different link target in code", "\"The expression `[x](wrong)` must always remain unchanged\"" + src,
			[]string{"The expression `[x](right)` must always remain unchanged"}, 1},
		{"backticks inside a double-backtick span", "\"Run `` `uname` `` to print the kernel name\"" + src, []string{"Run uname to print the kernel name"}, 1},
		{"a link around code on the page", "\"Add SQL `MERGE` command to adjust one table to match another\"" + src,
			[]string{"Add SQL [`MERGE`](sql-merge.html \"MERGE\") command to adjust one table to match another"}, 0},
		{"a stray backtick in an earlier paragraph", "\"sets `net.ipv4.tcp_keepalive_time` to `120` in the conf\"" + src,
			[]string{"Use ` for code.\n\n[filesystem](https://x.example/) sets `net.ipv4.tcp_keepalive_time` to `120` in the conf."}, 0},
		{"link syntax in code on the page too", "\"The expression `[x](y)` must always remain unchanged\"" + src,
			[]string{"The expression `[x](y)` must always remain unchanged"}, 0},
		// The page's code is kept as written too: dropped there, its backticks
		// and link syntax let a different code match.
		{"backticks in the page's code", "\"The expression `echo whoami` must always remain unchanged\"" + src,
			[]string{"The expression ``echo `whoami` `` must always remain unchanged"}, 1},
		{"link syntax in the page's code", "\"The expression `x` must always remain unchanged\"" + src,
			[]string{"The expression `[x](y)` must always remain unchanged"}, 1},
		{"code quoted with print('x') is not print(\"x\")", "\"Then call `print('done')` before you exit the loop\"" + src,
			[]string{"Then call `print(\"done\")` before you exit the loop"}, 1},
		{"an autolink attributes", `"widgets will double in price next year" <https://a.example/>`, []string{"unrelated"}, 1},
		// Where Markdown puts a quote: what goldmark reads as prose is checked,
		// what it reads as code, HTML or a definition is not.
		{"in a heading", `## "widgets will double in price next year"` + src, []string{"unrelated"}, 1},
		{"in a table cell", "| Quote | Source |\n|---|---|\n| \"widgets will double in price next year\" | [src](https://a.example/) |", []string{"unrelated"}, 1},
		{"in a blockquote", `> "widgets will double in price next year"` + src, []string{"unrelated"}, 1},
		{"in indented code", `    print("widgets will double in price next year")` + src, []string{"unrelated"}, 0},
		{"in an HTML attribute", `<span title="widgets will double in price next year">word</span>` + src, []string{"unrelated"}, 0},
		{"in a link reference definition", "[src]: https://a.example/ \"widgets will double in price next year\"\n\nSee [src].", []string{"unrelated"}, 0},
		// The search once gave up after a thousand tries, and a page that
		// repeated the quote's first word that often lost a valid quote.
		{"a first piece repeated across the page", `"require[s] more thought about where your primary is"` + src,
			[]string{strings.Repeat("require ", 1100) + "options require more thought about where your primary is."}, 0},
	} {
		got, n := unquoteUnfound(tc.report, quotePages(tc.pages...))
		if n != tc.removed {
			t.Errorf("%s: removed %d, want %d:\n%s", tc.name, n, tc.removed, got)
		}
	}
}

// Escaped quotation marks are removed with their backslashes, which shown
// alone are noise; a backslash that escapes no mark is text and stays. Every
// trailing backslash was trimmed, and a Windows path in curly quotes lost
// its last one.
func TestUnquoteUnfoundRemovesEscapedMarksWhole(t *testing.T) {
	for _, tc := range []struct{ name, report, want string }{
		{"escaped straight marks",
			`It says \"widgets will double in price next year\"` + src + `.`,
			`It says widgets will double in price next year` + src + `.`},
		{"curly marks are not escapable",
			`Install it under “the default Windows path C:\Program Files\”` + src + `.`,
			`Install it under the default Windows path C:\Program Files\` + src + `.`},
		{"an escaped backslash before a straight mark",
			`Install it under "the default Windows path C:\Program Files\\"` + src + `.`,
			`Install it under the default Windows path C:\Program Files\\` + src + `.`},
	} {
		got, n := unquoteUnfound(tc.report, quotePages("unrelated"))
		if n != 1 || got != tc.want {
			t.Errorf("%s: got %q (%d removed), want %q", tc.name, got, n, tc.want)
		}
	}
}

// A quote of many bracketed edits against a page of one repeated word: each
// state was tried again from every path that reached it, and seven edits
// took seconds; twelve would not finish.
func TestUnquoteUnfoundManyEditsOnARepetitivePage(t *testing.T) {
	report := `"` + strings.Repeat("a[x] ", 12) + `missing final words that never appear"` + src
	if _, n := unquoteUnfound(report, quotePages(strings.Repeat("a ", 200))); n != 1 {
		t.Errorf("removed %d, want 1", n)
	}
}

// The summarizer quotes the question it was asked; that is not a page, and
// not an invented quote. The analysis's own wording, a claim's or a
// conclusion's, is its account of a source: quoted beside the source's link,
// it reads as the source's words, which the source never wrote.
func TestCheckQuotesAcceptsTheQuestionNotTheAnalysisWording(t *testing.T) {
	report := `Nobody defines "the best bang for the buck in 2026"` + src + `. One source uses "a team larger than one engineer" as its line` + src + `.`
	findings := []agent.Finding{{URL: "https://a.example/", Content: "PostgreSQL the moment you need a team larger than one.", Status: "ok"}}
	sum := &agent.Summary{Report: report}
	NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1).checkQuotes(sum, "Which GPU is the best bang for the buck in 2026?", fetchedPages(findings), "")
	want := `Nobody defines "the best bang for the buck in 2026"` + src + `. One source uses a team larger than one engineer as its line` + src + `.`
	if sum.Report != want {
		t.Errorf("got  %q\nwant %q", sum.Report, want)
	}
}

// With no fetched page there is nothing to check a quote against: a run
// without search is already reported unverified.
func TestCheckQuotesWithoutPagesChangesNothing(t *testing.T) {
	report := `It says "these words are in no page at all"` + src + `.`
	sum := &agent.Summary{Report: report}
	NewDriver(&fakeAssistant{}, &MultiSink{}, nil, 1).checkQuotes(sum, "q", fetchedPages(nil), "")
	if sum.Report != report {
		t.Errorf("got %q; want the report as it was", sum.Report)
	}
}

// quotingSummarizer writes a report with one verbatim quote and one
// invented one, over a single fetched page.
type quotingSummarizer struct{ *fakeAssistant }

func (q *quotingSummarizer) Summarize(context.Context, string) (*agent.Summary, error) {
	return &agent.Summary{Report: `## Why
The page says "widget prices stayed flat through 2024" ([Survey](https://a.example/)), and "widgets will double in price next year" ([Survey](https://a.example/)).`}, nil
}

func TestRunUnquotesTheSummarizersInventedQuotes(t *testing.T) {
	topics := []agent.SubTopic{{ID: "1", Name: "Widgets"}}
	plan := newTestPlan("quick", topics)
	q := subQueries(plan.Question, topics[0])[0]
	fa := &quotingSummarizer{&fakeAssistant{
		planTopics: topics,
		findings: map[string][]agent.Finding{q: {{Query: q, Title: "Survey", URL: "https://a.example/",
			Content: "Widget prices stayed flat through 2024, the survey found."}}},
		signals: map[string][]agent.SourceSignal{q: {{URL: "https://a.example/", Domain: "a.example", Status: "ok"}}},
	}}
	res, err := NewDriver(fa, &MultiSink{}, nil, 1).Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	r := res.Summary.Report
	if !strings.Contains(r, `"widget prices stayed flat through 2024"`) || !strings.Contains(r, ` widgets will double in price next year (`) ||
		strings.Contains(r, `"widgets will double`) {
		t.Errorf("want the verbatim quote kept and the invented one unquoted:\n%s", r)
	}
}

// A quote with an elision or an edit is matched against every page; on
// repetitive pages, backtracking over each place of each piece took seconds.
func BenchmarkUnquoteUnfoundRepetitivePages(b *testing.B) {
	text := strings.Repeat("The timeout is configured by the administrator. ", 900)
	var report strings.Builder
	for range 20 {
		report.WriteString(`"The timeout is … never configured by an administrator"` + src + ".\n")
		report.WriteString(`"The timeout [was] never configured by an administrator"` + src + ".\n")
	}
	for b.Loop() {
		pages := map[string]*pageText{}
		for i := range 25 {
			pages[string(rune('a'+i))] = newPageText(text)
		}
		unquoteUnfound(report.String(), pages)
	}
}
