// Copyright 2013 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"io"
	"log"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"code.google.com/p/go.exp/ebnf"
	"github.com/cznic/ebnfutil"
	"github.com/cznic/strutil"
)

const (
	sep = ""
)

var todo = strings.ToUpper("todo")

func dbg(s string, va ...interface{}) {
	_, fn, fl, _ := runtime.Caller(1)
	fmt.Printf("%s:%d: ", path.Base(fn), fl)
	fmt.Printf(s, va...)
	fmt.Println()
}

type job struct {
	grm         ebnfutil.Grammar
	rep         *ebnfutil.Report
	names       map[string]bool
	repetitions map[string]bool
	tPrefix     string
	term2name   map[string]string
}

func (j *job) inventName(prefix, sep string) (s string) {
	for i := 0; ; i++ {
		switch {
		case i == 0 && sep == "":
			s = fmt.Sprintf("%s%s", prefix, sep)
		case i == 0:
			continue
		case i != 0:
			s = fmt.Sprintf("%s%s%d", prefix, sep, i)
		}
		if _, ok := j.names[s]; !ok {
			j.names[s] = true
			return s
		}
	}
}

func (j *job) toBnf(start string) {
	var err error
	j.grm, j.repetitions, err = j.grm.BNF(start, func(name string) string {
		return j.inventName(name, sep)
	})
	if err != nil {
		log.Fatal(err)
	}
}

func (j *job) checkTerminals(start string) {
	var err error
	j.rep, err = j.grm.Analyze(start)
	if err != nil {
		log.Fatal(err)
	}
}

func toAscii(s string) string {
	var r []byte
	for _, b := range s {
		if b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
			r = append(r, byte(b))
		}
	}
	return string(r)
}

func (j *job) str(expr ebnf.Expression) (s string) {
	switch x := expr.(type) {
	case nil:
		return "/* EMPTY */"
	case *ebnf.Name:
		switch name := x.String; ast.IsExported(name) {
		case true:
			return name
		default:
			return j.term2name[name]
		}
	case ebnf.Sequence:
		a := []string{}
		for _, v := range x {
			a = append(a, j.str(v))
		}
		return strings.Join(a, " ")
	case *ebnf.Token:
		switch s := x.String; len(s) {
		case 1:
			return strconv.QuoteRune(rune(s[0]))
		default:
			hint := ""
			if _, ok := j.rep.Literals[s]; ok && toAscii(s) == "" {
				hint = fmt.Sprintf(" /* %q */", s)
			}
			return fmt.Sprintf("%s%s", j.term2name[s], hint)
		}
	default:
		log.Fatalf("%T(%#v)", x, x)
		panic("unreachable")
	}
}

var sIsStart = map[bool]string{
	false: "$$",
	true:  "_parserResult",
}

const (
	rep0 = iota
	rep1
)

func (j *job) ystr(expr ebnf.Expression, name, start string, rep int) (s string) {
	a := []string{}

	var f func(ebnf.Expression)
	f = func(expr ebnf.Expression) {
		switch x := expr.(type) {
		case nil:
			// nop
		case *ebnf.Name:
			a = append(a, fmt.Sprintf("$%d", len(a)+1))
		case ebnf.Sequence:
			for _, v := range x {
				f(v)
			}
		case *ebnf.Token:
			a = append(a, fmt.Sprintf("%q", x.String))
		default:
			log.Fatalf("%T(%#v)", x, x)
			panic("unreachable")
		}
	}

	f(expr)
	switch j.repetitions[name] {
	case true:
		switch rep {
		case 0:
			return fmt.Sprintf("$$ = []%s(nil)", name)
		default:
			return fmt.Sprintf("$$ = append($1.([]%s), %s)", name, strings.Join(a[1:], ", "))
			//default:
			//	log.Fatal("internal error")
			//	panic("unreachable")
		}
	case false:
		switch len(a) {
		case 0:
			return fmt.Sprintf("%s = nil", sIsStart[name == start])
		case 1:
			return fmt.Sprintf("%s = %s", sIsStart[name == start], a[0])
		default:
			return fmt.Sprintf("%s = []%s{%s}", sIsStart[name == start], name, strings.Join(a, ", "))
		}
	}
	panic("unreachable")
}

func (j *job) render(w io.Writer, start string) (err error) {
	f := strutil.IndentFormatter(w, "\t")
	f.Format(`%%{

//%s Put your favorite license here
		
// yacc source generated by ebnf2y[1]
// at %s.
//
// CAUTION: If this file is a Go source file (*.go), it was generated
// automatically by '$ go tool yacc' from a *.y file - DO NOT EDIT in that case!
// 
//   [1]: http://github.com/cznic/ebnf2y

package main //%s real package name

//%s required only be the demo _dump function
import (
	"bytes"
	"fmt"
	"strings"

	"github.com/cznic/strutil"
)

%%}

%%union {
	item interface{} //%s insert real field(s)
}

`, todo, time.Now(), todo, todo, todo)
	j.term2name = map[string]string{}
	a := []string{}
	for name := range j.rep.Tokens {
		token := j.inventName(j.tPrefix+strings.ToUpper(name), "")
		j.term2name[name] = token
		a = append(a, token)
	}
	if len(a) != 0 {
		sort.Strings(a)
		for _, name := range a {
			f.Format("%%token\t%s\n", name)
		}
		f.Format("\n%%type\t<item> \t/*%s real type(s), if/where applicable */\n", todo)
		for _, name := range a {
			f.Format("\t%s\n", name)
		}
		f.Format("\n")
	}

	j.inventName(j.tPrefix+"TOK", "")
	a = a[:0]
	for lit := range j.rep.Literals {
		if len(lit) == 1 || toAscii(lit) != "" {
			continue
		}

		j.term2name[lit] = j.inventName(j.tPrefix+"TOK", "")
		a = append(a, lit)
	}
	if len(a) != 0 {
		for _, lit := range a {
			f.Format("%%token\t%s\t/*%s Name for %q */\n", j.term2name[lit], todo, lit)
		}
		f.Format("\n")
		f.Format("%%type\t<item> \t/*%s real type(s), if/where applicable */\n", todo)
		for _, lit := range a {
			f.Format("\t%s\n", j.term2name[lit])
		}
		f.Format("\n")
	}

	a = a[:0]
	for lit := range j.rep.Literals {
		nm := toAscii(lit)
		if len(lit) == 1 || nm == "" {
			continue
		}

		name := j.inventName(j.tPrefix+strings.ToUpper(nm), "")
		j.term2name[lit] = name
		a = append(a, name)
	}
	if len(a) != 0 {
		sort.Strings(a)
		for _, name := range a {
			f.Format("%%token %s\n", name)
		}
		f.Format("\n")
	}

	a = a[:0]
	for name := range j.rep.NonTerminals {
		a = append(a, name)
	}
	sort.Strings(a)
	f.Format("%%type\t<item> \t/*%s real type(s), if/where applicable */\n", todo)
	for _, name := range a {
		f.Format("\t%s\n", name)
	}
	f.Format("\n")

	f.Format("/*%s %%left, %%right, ... declarations */\n\n%%start %s\n\n%%%%\n\n", todo, start)

	rule := 0
	for _, name := range a {
		f.Format("%s:\n\t", name)
		expr := j.grm[name].Expr
		switch x := expr.(type) {
		case ebnf.Alternative:
			for i, v := range x {
				if i != 0 {
					f.Format("|\t")
				}
				rule++
				f.Format("%s\n\t{\n\t\t%s //%s %d\n\t}\n", j.str(v), j.ystr(v, name, start, i), todo, rule)
			}
		default:
			rule++
			f.Format("%s\n\t{\n\t\t%s //%s %d\n\t}\n", j.str(x), j.ystr(x, name, start, -1), todo, rule)
		}
		f.Format("\n")
	}

	f.Format(`%%%%

//%s remove demo stuff below

var _parserResult interface{}

type (%i
`, todo)

	for _, name := range a {
		f.Format("%s interface{}\n", name)
	}

	f.Format(`%u)
	
func _dump() {
	s := fmt.Sprintf("%%#v", _parserResult)
	s = strings.Replace(s, "%%", "%%%%", -1)
	s = strings.Replace(s, "{", "{%%i\n", -1)
	s = strings.Replace(s, "}", "%%u\n}", -1)
	s = strings.Replace(s, ", ", ",\n", -1)
	var buf bytes.Buffer
	strutil.IndentFormatter(&buf, ". ").Format(s)
	buf.WriteString("\n")
	a := strings.Split(buf.String(), "\n")
	for _, v := range a {
		if strings.HasSuffix(v, "(nil)") || strings.HasSuffix(v, "(nil),") {
			continue
		}
	
		fmt.Println(v)
	}
}

// End of demo stuff
`)
	return
}

func scoreN(s string, a []string) (y int) {
	if len(a) == 0 {
		log.Fatal("internal error")
	}

	sn := a[0]
	if len(sn) == 0 {
		log.Fatal("internal error")
	}

	if len(sn) == len(s) {
		return -1
	}

	i := len(sn)
	k := 1
	for i > 0 {
		switch c := sn[i-1]; {
		case c < '0' || c > '9':
			return
		default:
			y += k * (int(c) - '0')
			k *= 10
			i--
		}
	}
	return
}

func score(fn string) (y int) {
	cmd := exec.Command("go", "tool", "yacc", fn)
	var yout bytes.Buffer
	cmd.Stdout = &yout
	if err := cmd.Run(); err != nil {
		log.Fatalf("execuing 'go tool yacc': %v", err)
	}

	s := yout.String()
	a := strings.Split(s, " shift/reduce")
	y = scoreN(s, a)
	if y < 0 {
		return
	}

	a = strings.Split(s, " reduce/reduce")
	return y + scoreN(s, a)
}

func main() {
	oIE := flag.Uint("ie", 0, "Inline EBNF. 0: none, 1: used once, 2: all (illegal with -m).")
	oIY := flag.Uint("iy", 0, "Inline BNF (.y). 0: none, 1: used once, 2: all (illegal with -m).")
	oM := flag.Bool("m", false, "Magic: reduce yacc conflicts, maybe (slow).")
	oMBig := flag.Bool("M", false, "Like -m and report to stderr.")
	oOE := flag.String("oe", "", "Pretty print EBNF to <arg> if non blank.")
	oOut := flag.String("o", "", "Output file. Stdout if left blank.")
	oPrefix := flag.String("p", "", "Prefix for token names, eg. \"_\". Default blank.")
	oStart := flag.String("start", "SourceFile", "Start production name.")
	flag.Parse()

	if *oMBig {
		*oM = true
	}
	if *oM {
		switch {
		case *oOut == "":
			log.Fatal("'-m' requires using a named output file ('-o name').")
		case *oIE > 1 || *oIY > 1:
			log.Fatal("'-m' cannot be used with '-ie' > 1 or '-iy' > 1.")
		}
	}

	log.SetFlags(log.LstdFlags | log.Lshortfile)
	if flag.NArg() > 1 {
		log.Fatal("Atmost one input file may be specified.")
	}

	var err error
	var in *os.File

	switch name := flag.Arg(0); {
	case name == "":
		in = os.Stdin
	default:
		if in, err = os.Open(name); err != nil {
			log.Fatal(err)
		}
	}

	grm, err := ebnfutil.Parse(in.Name(), in)
	if err != nil {
		log.Fatal(err)
	}

	if err := grm.Verify(*oStart); err != nil {
		log.Fatal(err)
	}

	switch *oIE {
	case 0:
		// nop
	case 1:
		if err = grm.Inline(*oStart, false); err != nil {
			log.Fatal(err)
		}
	case 2:
		if err = grm.Inline(*oStart, true); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("-ie: <arg> must be 0, 1 or 2")
	}

	if fn := *oOE; fn != "" {
		f, err := os.Create(fn)
		if err != nil {
			log.Fatal(err)
		}

		b := []byte(grm.String())
		n, err := f.Write(b)
		if n != len(b) || err != nil {
			log.Fatal(err)
		}

		if err = f.Close(); err != nil {
			log.Fatal(err)
		}
	}

	j := &job{
		grm:     grm,
		names:   map[string]bool{},
		tPrefix: *oPrefix,
	}
	for _, name := range []string{
		"break", "default", "func", "interface", "select",
		"case", "defer", "go", "map", "struct",
		"chan", "else", "goto", "package", "switch",
		"const", "fallthrough", "if", "range", "type",
		"continue", "for", "import", "return", "var",
	} {
		j.names[name] = true
	}
	for name := range grm {
		if j.names[name] {
			log.Fatalf("Reserved word %q cannot be used as a production name.", name)
		}

		j.names[name] = true
	}
	start := j.inventName("Start", "")
	j.grm[start] = &ebnf.Production{
		Name: &ebnf.Name{String: start},
		Expr: &ebnf.Name{String: *oStart},
	}

	j.toBnf(*oStart)
	switch *oIY {
	case 0:
		// nop
	case 1:
		if err = j.grm.Inline(*oStart, false); err != nil {
			log.Fatal(err)
		}
	case 2:
		if err = j.grm.Inline(*oStart, true); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("-ie: <arg> must be 0, 1 or 2")
	}

	var out *os.File
	emit := func() {
		n0 := map[string]bool{}
		for name := range j.names {
			n0[name] = true
		}
		out = os.Stdout
		if s := *oOut; s != "" {
			if out, err = os.Create(s); err != nil {
				log.Fatal(err)
			}
		}

		w := bufio.NewWriter(out)
		j.checkTerminals(start)
		if err = j.render(w, start); err != nil {
			log.Fatal(err)
		}

		if err = w.Flush(); err != nil {
			log.Fatal(err)
		}

		if err = out.Close(); err != nil {
			log.Fatal(err)
		}
		j.names = n0
	}

	log2 := log.New(os.Stderr, "[magic] ", 0)
	tried := map[string]bool{}
magic:
	emit()
	if !*oM {
		return
	}

	g0 := j.grm.Normalize()
	bestName := ""
	best0 := score(out.Name())
	var best int
	if best0 < 0 {
		goto magic2
	}

	best = best0
	for name := range j.grm {
		g1 := g0.Normalize()
		if err = g1.InlineOne(name, true); err != nil {
			log.Fatal(err)
		}

		j.grm = g1
		emit()
		if n := score(out.Name()); n >= 0 && n < best {
			best = n
			bestName = name
			if *oMBig {
				log2.Printf("%q: %d", bestName, best)
			}
		}
	}

	j.grm = g0
	if best < best0 {
		if g0.InlineOne(bestName, true); err != nil {
			log.Fatal(err)
		}

		log2.Printf("Inlined %q: conflicts %d -> %d", bestName, best0, best)
		goto magic
	}
	emit()

magic2:
	emit()
	if !*oM {
		return
	}

	cmd := exec.Command("go", "tool", "yacc", out.Name())
	var yout bytes.Buffer
	cmd.Stdout = &yout
	if err = cmd.Run(); err != nil {
		log.Fatalf("executing 'go tool yacc': %v", err)
	}

	if *oMBig {
		log2.Println("----")
		a := strings.Split(strings.TrimSpace(yout.String()), "\n")
		for _, v := range a {
			log2.Println(v)
		}
	}
	a := strings.Split(yout.String(), "\n")
next:
	for _, v := range a {
		s := strings.TrimSpace(v)
		if strings.HasPrefix(s, "rule ") && strings.HasSuffix(s, " never reduced") {
			s = strings.TrimSpace(s[len("rule "):])
			for i := range s {
				switch c := s[i]; {
				default:
					log.Fatalf("internal error %#x", c)
				case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_':
					// nop
				case c == ':':
					name := s[:i]
					if tried[name] {
						continue next
					}

					tried[name] = true
					if err = j.grm.InlineOne(name, true); err != nil {
						log.Fatal(err)
					}

					goto magic2
				}
			}
		}
	}
}
