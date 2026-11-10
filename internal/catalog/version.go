package catalog

import (
	"cmp"
	"strings"
)

// CompareVersion returns the SemVer precedence of a relative to b.
// Build metadata does not affect precedence.
func CompareVersion(a, b string) int {
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	coreA, preA, prereleaseA := strings.Cut(a, "-")
	coreB, preB, prereleaseB := strings.Cut(b, "-")
	partsA, partsB := strings.Split(coreA, "."), strings.Split(coreB, ".")
	if len(partsA) != 3 || len(partsB) != 3 {
		return cmp.Compare(a, b)
	}
	for i := range partsA {
		if result := compareDecimal(partsA[i], partsB[i]); result != 0 {
			return result
		}
	}
	if !prereleaseA && !prereleaseB {
		return 0
	}
	if prereleaseA != prereleaseB {
		if prereleaseA {
			return -1
		}
		return 1
	}
	partsA, partsB = strings.Split(preA, "."), strings.Split(preB, ".")
	for i := 0; i < min(len(partsA), len(partsB)); i++ {
		left, right := partsA[i], partsB[i]
		numericA, numericB := decimal(left), decimal(right)
		switch {
		case numericA && numericB:
			if result := compareDecimal(left, right); result != 0 {
				return result
			}
		case numericA:
			return -1
		case numericB:
			return 1
		default:
			if result := cmp.Compare(left, right); result != 0 {
				return result
			}
		}
	}
	return cmp.Compare(len(partsA), len(partsB))
}

func compareDecimal(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if result := cmp.Compare(len(a), len(b)); result != 0 {
		return result
	}
	return cmp.Compare(a, b)
}

func decimal(s string) bool {
	return s != "" && strings.IndexFunc(s, func(ch rune) bool { return ch < '0' || ch > '9' }) < 0
}
