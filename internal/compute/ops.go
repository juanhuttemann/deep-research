package compute

import (
	"fmt"
	"math"
	"math/big"
	"slices"
	"strconv"
	"strings"
)

// The operations an analysis may ask for. Each is exact over the inputs'
// stated values, except cagr, whose root is taken in float64.
const (
	OpSum           = "sum"
	OpDifference    = "difference"
	OpProduct       = "product"
	OpRatio         = "ratio"
	OpPercentChange = "percent_change"
	OpShare         = "share"
	OpMean          = "mean"
	OpMin           = "min"
	OpMax           = "max"
	OpCAGR          = "cagr"
)

// Ops lists the operations, for the analyzer's instructions and the docs.
var Ops = []string{OpSum, OpDifference, OpProduct, OpRatio, OpPercentChange, OpShare, OpMean, OpMin, OpMax, OpCAGR}

// Input is one operand: a claim's stated value or an earlier result, by ID.
type Input struct {
	ID    string
	Value Number
	Unit  string
}

// Result is what an operation gives: the value, its unit and the formula
// written over the inputs' IDs, which is how a reader retraces it.
type Result struct {
	Value   Number
	Unit    string
	Formula string
}

// UnitKey is a unit as units are compared: case and spacing ignored. "TEU"
// and "teu" are one unit; "TEU" and "containers" are not, whatever they
// count, since converting one to the other is not arithmetic. The spellings
// of a percentage are one unit: a run's claim of growth "in percent" was
// contradicted by its own computation "in %". Percentage points are not.
func UnitKey(u string) string {
	k := strings.ToLower(strings.Join(strings.Fields(u), " "))
	// A rate's period is said in its formula, not its unit: "9.25 percent"
	// and the program's cagr of 9.25% are one figure.
	for _, per := range []string{" a period", " per period", " a year", " per year", " per annum", " annually", "/yr", "/year"} {
		k = strings.TrimSuffix(k, per)
	}
	switch k {
	case "percent", "per cent", "pct", "percentage", "%":
		return "%"
	case "percentage points", "percentage point", "percent points", "% points", "pp", "pps", "p.p.":
		return "percentage points"
	}
	return k
}

// Apply evaluates op over in. The error says, in words fit for the report,
// why the operation does not apply: the wrong number of inputs, units that
// differ where they must agree, a zero divisor, a growth rate between
// values that are not both positive.
func Apply(op string, in []Input, periods int) (Result, error) {
	switch op {
	case OpSum, OpMean, OpMin, OpMax:
		return aggregate(op, in)
	case OpDifference:
		return difference(in)
	case OpProduct:
		return product(in)
	case OpRatio, OpShare, OpPercentChange:
		return quotient(op, in)
	case OpCAGR:
		return cagr(in, periods)
	}
	return Result{}, fmt.Errorf("%q is not an operation (use one of %s)", op, strings.Join(Ops, ", "))
}

func ids(in []Input) []string {
	out := make([]string, len(in))
	for i, x := range in {
		out[i] = x.ID
	}
	return out
}

func maxDecimals(in []Input) int {
	d := 0
	for _, x := range in {
		d = max(d, x.Value.dec)
	}
	return d
}

// sameUnit is the unit every input shares, or an error naming two that
// differ.
func sameUnit(in []Input) (string, error) {
	for _, x := range in[1:] {
		if UnitKey(x.Unit) != UnitKey(in[0].Unit) {
			return "", fmt.Errorf("units differ: %s is %s, %s is %s", in[0].ID, unitName(in[0].Unit), x.ID, unitName(x.Unit))
		}
	}
	return in[0].Unit, nil
}

func unitName(u string) string {
	if strings.TrimSpace(u) == "" {
		return "unitless"
	}
	return strconv.Quote(strings.TrimSpace(u))
}

func arity(op string, in []Input, n int, exact bool) error {
	switch {
	case exact && len(in) != n:
		return fmt.Errorf("%s takes %d inputs, given %d", op, n, len(in))
	case !exact && len(in) < n:
		return fmt.Errorf("%s takes at least %d inputs, given %d", op, n, len(in))
	}
	return nil
}

func aggregate(op string, in []Input) (Result, error) {
	if err := arity(op, in, 2, false); err != nil {
		return Result{}, err
	}
	unit, err := sameUnit(in)
	if err != nil {
		return Result{}, err
	}
	switch op {
	case OpSum:
		sum := new(big.Rat)
		for _, x := range in {
			sum.Add(sum, x.Value.Rat())
		}
		return Result{Number{sum, maxDecimals(in)}, unit, strings.Join(ids(in), " + ")}, nil
	case OpMean:
		sum := new(big.Rat)
		for _, x := range in {
			sum.Add(sum, x.Value.Rat())
		}
		mean := sum.Quo(sum, new(big.Rat).SetInt64(int64(len(in))))
		return Result{Number{mean, maxDecimals(in) + 2}, unit, "mean(" + strings.Join(ids(in), ", ") + ")"}, nil
	}
	pick := slices.MinFunc(in, func(a, b Input) int { return a.Value.Rat().Cmp(b.Value.Rat()) })
	if op == OpMax {
		pick = slices.MaxFunc(in, func(a, b Input) int { return a.Value.Rat().Cmp(b.Value.Rat()) })
	}
	return Result{pick.Value, unit, op + "(" + strings.Join(ids(in), ", ") + ")"}, nil
}

func difference(in []Input) (Result, error) {
	if err := arity(OpDifference, in, 2, true); err != nil {
		return Result{}, err
	}
	unit, err := sameUnit(in)
	if err != nil {
		return Result{}, err
	}
	d := new(big.Rat).Sub(in[0].Value.Rat(), in[1].Value.Rat())
	// The difference of two percentages is in percentage points: 88.9% less
	// 82.4% is 6.5 points, not 6.5%, which would be a relative change.
	if UnitKey(unit) == "%" {
		unit = "percentage points"
	}
	return Result{Number{d, maxDecimals(in)}, unit, in[0].ID + " − " + in[1].ID}, nil
}

func product(in []Input) (Result, error) {
	if err := arity(OpProduct, in, 2, false); err != nil {
		return Result{}, err
	}
	p, dec := big.NewRat(1, 1), 0
	var units []string
	for _, x := range in {
		p.Mul(p, x.Value.Rat())
		dec += x.Value.dec
		if u := strings.TrimSpace(x.Unit); u != "" {
			units = append(units, u)
		}
	}
	return Result{Number{p, dec}, strings.Join(units, " × "), strings.Join(ids(in), " × ")}, nil
}

// quotient is ratio (a ÷ b), share (a as a percentage of b) and
// percent_change (from a to b, as a percentage of a).
func quotient(op string, in []Input) (Result, error) {
	if err := arity(op, in, 2, true); err != nil {
		return Result{}, err
	}
	a, b := in[0].Value.Rat(), in[1].Value.Rat()
	divisor := in[1]
	if op == OpPercentChange {
		divisor = in[0]
	}
	if divisor.Value.Rat().Sign() == 0 {
		return Result{}, fmt.Errorf("%s is zero and cannot divide", divisor.ID)
	}
	if op == OpRatio {
		q := new(big.Rat).Quo(a, b)
		return Result{Number{q, sigDecimals(q, 4)}, ratioUnit(in[0].Unit, in[1].Unit), in[0].ID + " ÷ " + in[1].ID}, nil
	}
	if _, err := sameUnit(in); err != nil {
		return Result{}, err
	}
	hundred := big.NewRat(100, 1)
	if op == OpShare {
		q := new(big.Rat).Quo(a, b)
		return Result{Number{q.Mul(q, hundred), 2}, "%", in[0].ID + " ÷ " + in[1].ID + " × 100"}, nil
	}
	q := new(big.Rat).Sub(b, a)
	q.Quo(q, a).Mul(q, hundred)
	return Result{Number{q, 2}, "%", "(" + in[1].ID + " − " + in[0].ID + ") ÷ " + in[0].ID + " × 100"}, nil
}

// cagr is the compound growth rate a period from the first input to the
// second over periods periods, as a percentage. The root is irrational in
// general, so this one result is a float64 rounded to its places: still the
// same on every run and platform, IEEE arithmetic being specified.
func cagr(in []Input, periods int) (Result, error) {
	if err := arity(OpCAGR, in, 2, true); err != nil {
		return Result{}, err
	}
	if _, err := sameUnit(in); err != nil {
		return Result{}, err
	}
	if periods <= 0 {
		return Result{}, fmt.Errorf("cagr needs the number of periods between %s and %s", in[0].ID, in[1].ID)
	}
	start, end := in[0].Value.Float(), in[1].Value.Float()
	if in[0].Value.Rat().Sign() <= 0 || in[1].Value.Rat().Sign() <= 0 {
		return Result{}, fmt.Errorf("cagr needs %s and %s both positive", in[0].ID, in[1].ID)
	}
	if math.IsInf(start, 0) || math.IsInf(end, 0) || start == 0 || end == 0 {
		return Result{}, fmt.Errorf("cagr of %s and %s is beyond float range", in[0].ID, in[1].ID)
	}
	// In logs: the ratio of the two can underflow where their logs do not.
	v := math.Expm1((math.Log(end)-math.Log(start))/float64(periods)) * 100
	r := new(big.Rat)
	if r.SetFloat64(v) == nil {
		return Result{}, fmt.Errorf("cagr of %s and %s is not a finite number", in[0].ID, in[1].ID)
	}
	formula := fmt.Sprintf("((%s ÷ %s)^(1/%d) − 1) × 100", in[1].ID, in[0].ID, periods)
	return Result{Number{r, 2}, "%", formula}, nil
}

// sigDecimals are the places that show sig significant digits of r, within
// zero to twelve: a ratio of 4 is 4.000, one of 0.0123 is 0.01230.
func sigDecimals(r *big.Rat, sig int) int {
	f, _ := r.Float64()
	if f == 0 {
		return sig - 1
	}
	e := int(math.Floor(math.Log10(math.Abs(f))))
	return min(max(sig-1-e, 0), 12)
}

// ratioUnit is the unit of a ÷ b: "×" for like units, which cancel, and
// "a per b" otherwise.
func ratioUnit(a, b string) string {
	a, b = cancelScale(strings.TrimSpace(a), strings.TrimSpace(b))
	switch {
	case UnitKey(a) == UnitKey(b):
		return "×"
	case b == "":
		return a
	case a == "":
		return "per " + b
	}
	return a + " per " + b
}

// scaleWords multiply a unit: "USD million", "million paid memberships".
var scaleWords = []string{"thousand", "million", "billion", "trillion"}

// cancelScale drops a scale word both units of a ratio carry: revenue in
// USD million over memberships in millions is USD per membership, which a
// run printed as "129.1 USD million per million paid memberships".
func cancelScale(a, b string) (string, string) {
	for _, w := range scaleWords {
		if hasWord(a, w) && hasWord(b, w) {
			return dropWord(a, w), dropWord(b, w)
		}
	}
	return a, b
}

func hasWord(s, w string) bool {
	for _, f := range strings.Fields(strings.ToLower(s)) {
		if f == w {
			return true
		}
	}
	return false
}

func dropWord(s, w string) string {
	var out []string
	dropped := false
	for _, f := range strings.Fields(s) {
		if !dropped && strings.ToLower(f) == w {
			dropped = true
			continue
		}
		out = append(out, f)
	}
	return strings.Join(out, " ")
}

// UnitsAgree reports whether a unit a claim gives its figure says the same
// as the program's: the same unit, or wording that differs only where it
// does not change the figure. Two units disagree when they are a
// percentage and percentage points, or name different currencies or
// scales; "USD per paid membership per year" and "USD per paid membership"
// do not. A missing unit agrees with any.
func UnitsAgree(a, b string) bool {
	ka, kb := UnitKey(a), UnitKey(b)
	if ka == "" || kb == "" || ka == kb {
		return true
	}
	pa, pb := ka == "%" || ka == "percentage points", kb == "%" || kb == "percentage points"
	if pa || pb {
		return false
	}
	return wordIn(ka, kb, scaleWords) && wordIn(ka, kb, []string{"usd", "eur", "gbp", "jpy", "cny", "$", "€", "£"})
}

// wordIn reports whether a and b name the same one of words, or neither
// names any.
func wordIn(a, b string, words []string) bool {
	first := func(s string) string {
		for _, w := range words {
			if hasWord(s, w) || strings.Contains(s, w) && len([]rune(w)) == 1 {
				return w
			}
		}
		return ""
	}
	return first(a) == first(b)
}
