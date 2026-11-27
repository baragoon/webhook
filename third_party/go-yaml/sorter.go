// Modified in the webhook project.
package yaml

import (
	"math"
	"reflect"
	"unicode"
)

type keyList []reflect.Value

func (l keyList) Len() int      { return len(l) }
func (l keyList) Swap(i, j int) { l[i], l[j] = l[j], l[i] }
func (l keyList) Less(i, j int) bool {
	a := l[i]
	b := l[j]
	ak := a.Kind()
	bk := b.Kind()
	for (ak == reflect.Interface || ak == reflect.Ptr) && !a.IsNil() {
		a = a.Elem()
		ak = a.Kind()
	}
	for (bk == reflect.Interface || bk == reflect.Ptr) && !b.IsNil() {
		b = b.Elem()
		bk = b.Kind()
	}
	if comparison, ok := compareIntegerKeys(a, b); ok {
		if comparison != 0 {
			return comparison < 0
		}
		return ak < bk
	}
	if comparison, ok := compareIntegerFloatKeys(a, b); ok {
		if comparison != 0 {
			return comparison < 0
		}
		return ak < bk
	}
	af, aok := keyFloat(a)
	bf, bok := keyFloat(b)
	if aok && bok {
		if math.IsNaN(af) || math.IsNaN(bf) {
			return math.IsNaN(af) && !math.IsNaN(bf)
		}
		if af != bf {
			return af < bf
		}
		if ak != bk {
			return ak < bk
		}
		return numLess(a, b)
	}
	if ak != reflect.String || bk != reflect.String {
		return ak < bk
	}
	ar, br := []rune(a.String()), []rune(b.String())
	for i := 0; i < len(ar) && i < len(br); {
		ad, bd := unicode.IsDigit(ar[i]), unicode.IsDigit(br[i])
		if ad && bd {
			ai, bi := i, i
			for ai < len(ar) && unicode.IsDigit(ar[ai]) {
				ai++
			}
			for bi < len(br) && unicode.IsDigit(br[bi]) {
				bi++
			}
			as, bs := i, i
			for as < ai && ar[as] == '0' {
				as++
			}
			for bs < bi && br[bs] == '0' {
				bs++
			}
			if ai-as != bi-bs {
				return ai-as < bi-bs
			}
			for k := 0; k < ai-as; k++ {
				if ar[as+k] != br[bs+k] {
					return ar[as+k] < br[bs+k]
				}
			}
			if ai != bi {
				return ai < bi
			}
			i = ai
			continue
		}
		if ar[i] == br[i] {
			i++
			continue
		}
		al, bl := unicode.IsLetter(ar[i]), unicode.IsLetter(br[i])
		if al && bl {
			return ar[i] < br[i]
		}
		if al || bl {
			return bl
		}
		if ad || bd {
			return bd
		}
		return ar[i] < br[i]
	}
	return len(ar) < len(br)
}

func compareIntegerFloatKeys(a, b reflect.Value) (int, bool) {
	isInteger := func(k reflect.Kind) bool {
		return k >= reflect.Int && k <= reflect.Int64 || k >= reflect.Uint && k <= reflect.Uintptr
	}
	isFloat := func(k reflect.Kind) bool { return k == reflect.Float32 || k == reflect.Float64 }
	reversed := false
	if isFloat(a.Kind()) && isInteger(b.Kind()) {
		a, b = b, a
		reversed = true
	}
	if !isInteger(a.Kind()) || !isFloat(b.Kind()) {
		return 0, false
	}
	f := b.Float()
	comparison := 0
	switch {
	case math.IsNaN(f), math.IsInf(f, -1):
		comparison = 1
	case math.IsInf(f, 1):
		comparison = -1
	case a.Kind() >= reflect.Int && a.Kind() <= reflect.Int64:
		switch {
		case f < -0x1p63:
			comparison = 1
		case f >= 0x1p63:
			comparison = -1
		default:
			i, truncated := a.Int(), int64(f)
			switch {
			case i < truncated:
				comparison = -1
			case i > truncated:
				comparison = 1
			case f > float64(truncated):
				comparison = -1
			case f < float64(truncated):
				comparison = 1
			}
		}
	default:
		switch {
		case f < 0:
			comparison = 1
		case f >= 0x1p64:
			comparison = -1
		default:
			i, truncated := a.Uint(), uint64(f)
			switch {
			case i < truncated:
				comparison = -1
			case i > truncated:
				comparison = 1
			case f > float64(truncated):
				comparison = -1
			}
		}
	}
	if reversed {
		comparison = -comparison
	}
	return comparison, true
}

func compareIntegerKeys(a, b reflect.Value) (int, bool) {
	signed := func(k reflect.Kind) bool { return k >= reflect.Int && k <= reflect.Int64 }
	unsigned := func(k reflect.Kind) bool { return k >= reflect.Uint && k <= reflect.Uintptr }
	ak, bk := a.Kind(), b.Kind()
	if !(signed(ak) || unsigned(ak)) || !(signed(bk) || unsigned(bk)) {
		return 0, false
	}
	if signed(ak) && a.Int() < 0 {
		if unsigned(bk) || b.Int() >= 0 {
			return -1, true
		}
		if a.Int() < b.Int() {
			return -1, true
		}
		if a.Int() > b.Int() {
			return 1, true
		}
		return 0, true
	}
	if signed(bk) && b.Int() < 0 {
		return 1, true
	}
	var av, bv uint64
	if signed(ak) {
		av = uint64(a.Int())
	} else {
		av = a.Uint()
	}
	if signed(bk) {
		bv = uint64(b.Int())
	} else {
		bv = b.Uint()
	}
	if av < bv {
		return -1, true
	}
	if av > bv {
		return 1, true
	}
	return 0, true
}

// keyFloat returns a float value for v if it is a number/bool
// and whether it is a number/bool or not.
func keyFloat(v reflect.Value) (f float64, ok bool) {
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(v.Int()), true
	case reflect.Float32, reflect.Float64:
		return v.Float(), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return float64(v.Uint()), true
	case reflect.Bool:
		if v.Bool() {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

// numLess returns whether a < b.
// a and b must necessarily have the same kind.
func numLess(a, b reflect.Value) bool {
	switch a.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return a.Int() < b.Int()
	case reflect.Float32, reflect.Float64:
		return a.Float() < b.Float()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return a.Uint() < b.Uint()
	case reflect.Bool:
		return !a.Bool() && b.Bool()
	}
	panic("not a number")
}
