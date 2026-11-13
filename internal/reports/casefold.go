package reports

import (
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// The code points where Python's str.casefold() differs from lowercasing
// (Unicode 16, as in Python 3.14). `a-b:d` shifts a run by d; `c=x.y` maps one
// code point to a sequence. Everything else folds as it lowercases.
const foldExceptionList = "b5=3bc df=73.73 149=2bc.6e 17f=73 1f0=6a.30c 345=3b9 390=3b9.308.301 3b0=3c5.308.301 3c2=3c3 3d0=3b2 " +
	"3d1=3b8 3d5=3c6 3d6=3c0 3f0=3ba 3f1=3c1 3f5=3b5 587=565.582 13a0-13f5:0 13f8-13fd:-8 1c80=432 " +
	"1c81=434 1c82=43e 1c83-1c84:-6210 1c85=442 1c86=44a 1c87=463 1c88=a64b 1e96=68.331 1e97=74.308 " +
	"1e98=77.30a 1e99=79.30a 1e9a=61.2be 1e9b=1e61 1e9e=73.73 1f50=3c5.313 1f52=3c5.313.300 " +
	"1f54=3c5.313.301 1f56=3c5.313.342 1f80=1f00.3b9 1f81=1f01.3b9 1f82=1f02.3b9 1f83=1f03.3b9 " +
	"1f84=1f04.3b9 1f85=1f05.3b9 1f86=1f06.3b9 1f87=1f07.3b9 1f88=1f00.3b9 1f89=1f01.3b9 1f8a=1f02.3b9 " +
	"1f8b=1f03.3b9 1f8c=1f04.3b9 1f8d=1f05.3b9 1f8e=1f06.3b9 1f8f=1f07.3b9 1f90=1f20.3b9 1f91=1f21.3b9 " +
	"1f92=1f22.3b9 1f93=1f23.3b9 1f94=1f24.3b9 1f95=1f25.3b9 1f96=1f26.3b9 1f97=1f27.3b9 1f98=1f20.3b9 " +
	"1f99=1f21.3b9 1f9a=1f22.3b9 1f9b=1f23.3b9 1f9c=1f24.3b9 1f9d=1f25.3b9 1f9e=1f26.3b9 1f9f=1f27.3b9 " +
	"1fa0=1f60.3b9 1fa1=1f61.3b9 1fa2=1f62.3b9 1fa3=1f63.3b9 1fa4=1f64.3b9 1fa5=1f65.3b9 1fa6=1f66.3b9 " +
	"1fa7=1f67.3b9 1fa8=1f60.3b9 1fa9=1f61.3b9 1faa=1f62.3b9 1fab=1f63.3b9 1fac=1f64.3b9 1fad=1f65.3b9 " +
	"1fae=1f66.3b9 1faf=1f67.3b9 1fb2=1f70.3b9 1fb3=3b1.3b9 1fb4=3ac.3b9 1fb6=3b1.342 1fb7=3b1.342.3b9 " +
	"1fbc=3b1.3b9 1fbe=3b9 1fc2=1f74.3b9 1fc3=3b7.3b9 1fc4=3ae.3b9 1fc6=3b7.342 1fc7=3b7.342.3b9 " +
	"1fcc=3b7.3b9 1fd2=3b9.308.300 1fd3=3b9.308.301 1fd6=3b9.342 1fd7=3b9.308.342 1fe2=3c5.308.300 " +
	"1fe3=3c5.308.301 1fe4=3c1.313 1fe6=3c5.342 1fe7=3c5.308.342 1ff2=1f7c.3b9 1ff3=3c9.3b9 1ff4=3ce.3b9 " +
	"1ff6=3c9.342 1ff7=3c9.342.3b9 1ffc=3c9.3b9 ab70-abbf:-38864 fb00=66.66 fb01=66.69 fb02=66.6c " +
	"fb03=66.66.69 fb04=66.66.6c fb05=73.74 fb06=73.74 fb13=574.576 fb14=574.565 fb15=574.56b " +
	"fb16=57e.576 fb17=574.56d"

// Letters new in Unicode 16 that Go's unicode tables (Unicode 15) do not
// lowercase yet. Same notation; harmless once Go knows them too.
const lowerUnicode16List = "1c89=1c8a a7cb=264 a7cc=a7cd a7da=a7db a7dc=19b 10d50-10d65:32"

var foldTable = sync.OnceValue(func() map[rune]string {
	hex := func(s string) rune {
		n, _ := strconv.ParseInt(s, 16, 32)
		return rune(n)
	}
	table := map[rune]string{}
	for _, item := range strings.Fields(lowerUnicode16List + " " + foldExceptionList) {
		if from, to, ok := strings.Cut(item, "="); ok {
			var b strings.Builder
			for _, cp := range strings.Split(to, ".") {
				b.WriteRune(hex(cp))
			}
			table[hex(from)] = b.String()
			continue
		}
		run, shift, _ := strings.Cut(item, ":")
		first, last, _ := strings.Cut(run, "-")
		d, _ := strconv.Atoi(shift)
		for cp := hex(first); cp <= hex(last); cp++ {
			table[cp] = string(cp + rune(d))
		}
	}
	return table
})

// lowerRune is one code point lowercased in full, as JavaScript's
// toLowerCase() and Python's str.lower() do: 'İ' becomes 'i' and a combining
// dot, where unicode.ToLower gives the bare 'i'.
func lowerRune(r rune) string {
	if r == 0x130 {
		return "i̇"
	}
	return string(unicode.ToLower(r))
}

// Casefold is Python's str.casefold(): caseless-comparison form, stronger than
// lowercasing ('ß' becomes 'ss'). Each code point is folded on its own, as
// Python does.
func Casefold(text string) string {
	// Plain ASCII, which is nearly every name, folds exactly as it lowercases.
	ascii := true
	for i := 0; i < len(text); i++ {
		if text[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return strings.ToLower(text)
	}
	table := foldTable()
	var out strings.Builder
	out.Grow(len(text))
	for _, r := range text {
		if folded, ok := table[r]; ok {
			out.WriteString(folded)
		} else {
			out.WriteString(lowerRune(r))
		}
	}
	return out.String()
}
