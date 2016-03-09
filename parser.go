package quicktemplate

import (
	"bytes"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

type parser struct {
	s           *scanner
	w           io.Writer
	packageName string
	prefix      string
	forDepth    int
}

func parse(w io.Writer, r io.Reader, filePath string) error {
	packageName, err := getPackageName(filePath)
	if err != nil {
		return err
	}
	p := &parser{
		s:           newScanner(r, filePath),
		w:           w,
		packageName: packageName,
	}
	return p.parseTemplate()
}

func (p *parser) parseTemplate() error {
	s := p.s
	p.Printf("package %s\n", p.packageName)
	for s.Next() {
		t := s.Token()
		switch t.ID {
		case text:
			// just skip top-level text
		case tagName:
			switch string(t.Value) {
			case "code":
				if err := p.parseCode(); err != nil {
					return err
				}
			case "func":
				if err := p.parseFunc(); err != nil {
					return err
				}
			default:
				return fmt.Errorf("unexpected tag found outside func: %s at %s", t.Value, s.Context())
			}
		default:
			return fmt.Errorf("unexpected token found %s outside func at %s", t, s.Context())
		}
	}
	if err := s.LastError(); err != nil {
		return fmt.Errorf("cannot parse template: %s", err)
	}
	return nil
}

func (p *parser) parseFunc() error {
	s := p.s
	t, err := expectTagContents(s)
	if err != nil {
		return err
	}
	fname, fargs, fargsNoTypes, err := parseFnameFargsNoTypes(s, t.Value)
	if err != nil {
		return err
	}
	p.emitFuncStart(fname, fargs)
	for s.Next() {
		t := s.Token()
		switch t.ID {
		case text:
			p.emitText(t.Value)
		case tagName:
			ok, err := p.tryParseCommonTags(t.Value)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			switch string(t.Value) {
			case "endfunc":
				if err = skipTagContents(s); err != nil {
					return err
				}
				p.emitFuncEnd(fname, fargs, fargsNoTypes)
				return nil
			default:
				return fmt.Errorf("unexpected tag found inside func: %s at %s", t.Value, s.Context())
			}
		default:
			return fmt.Errorf("unexpected token found %s when parsing func at %s", t, s.Context())
		}
	}
	if err := s.LastError(); err != nil {
		return fmt.Errorf("cannot parse func: %s", err)
	}
	return fmt.Errorf("cannot find endfunc tag at %s", s.Context())
}

func (p *parser) parseFor() error {
	s := p.s
	t, err := expectTagContents(s)
	if err != nil {
		return err
	}
	p.Printf("for %s {", t.Value)
	p.prefix += "\t"
	p.forDepth++
	for s.Next() {
		t := s.Token()
		switch t.ID {
		case text:
			p.emitText(t.Value)
		case tagName:
			ok, err := p.tryParseCommonTags(t.Value)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			switch string(t.Value) {
			case "endfor":
				if err = skipTagContents(s); err != nil {
					return err
				}
				p.forDepth--
				p.prefix = p.prefix[1:]
				p.Printf("}")
				return nil
			default:
				return fmt.Errorf("unexpected tag found inside for loop: %s at %s", t.Value, s.Context())
			}
		default:
			return fmt.Errorf("unexpected token found %s when parsing for loop at %s", t, s.Context())
		}
	}
	if err := s.LastError(); err != nil {
		return fmt.Errorf("cannot parse for loop: %s", err)
	}
	return fmt.Errorf("cannot find endfor tag at %s", s.Context())
}

func (p *parser) parseIf() error {
	s := p.s
	t, err := expectTagContents(s)
	if err != nil {
		return err
	}
	if len(t.Value) == 0 {
		return fmt.Errorf("empty if condition at %s", s.Context())
	}
	p.Printf("if %s {", t.Value)
	p.prefix += "\t"
	elseUsed := false
	for s.Next() {
		t := s.Token()
		switch t.ID {
		case text:
			p.emitText(t.Value)
		case tagName:
			ok, err := p.tryParseCommonTags(t.Value)
			if err != nil {
				return err
			}
			if ok {
				continue
			}
			switch string(t.Value) {
			case "endif":
				if err = skipTagContents(s); err != nil {
					return err
				}
				p.prefix = p.prefix[1:]
				p.Printf("}")
				return nil
			case "else":
				if elseUsed {
					return fmt.Errorf("duplicate else branch found at %s", s.Context())
				}
				if err = skipTagContents(s); err != nil {
					return err
				}
				p.prefix = p.prefix[1:]
				p.Printf("} else {")
				p.prefix += "\t"
				elseUsed = true
			case "elseif":
				if elseUsed {
					return fmt.Errorf("unexpected elseif branch found after else branch at %s", s.Context())
				}
				t, err = expectTagContents(s)
				if err != nil {
					return err
				}
				p.prefix = p.prefix[1:]
				p.Printf("} else if %s {", t.Value)
				p.prefix += "\t"
			default:
				return fmt.Errorf("unexpected tag found inside if condition: %s at %s", t.Value, s.Context())
			}
		}
	}
	if err := s.LastError(); err != nil {
		return fmt.Errorf("cannot parse if branch: %s", err)
	}
	return fmt.Errorf("cannot find endif tag at %s", s.Context())
}

func (p *parser) tryParseCommonTags(tagName []byte) (bool, error) {
	s := p.s
	tagNameStr := string(tagName)
	switch tagNameStr {
	case "s", "v", "d", "f", "s=", "v=", "d=", "f=":
		t, err := expectTagContents(s)
		if err != nil {
			return false, err
		}
		filter := ""
		if len(tagNameStr) == 1 {
			filter = "e."
		} else {
			tagNameStr = tagNameStr[:len(tagNameStr)-1]
		}
		p.Printf("qw.%s%s(%s)", filter, tagNameStr, t.Value)
	case "=":
		t, err := expectTagContents(s)
		if err != nil {
			return false, err
		}
		fname, fargs, err := parseFnameFargs(s, t.Value)
		if err != nil {
			return false, err
		}
		p.Printf("%sStream(qw.w, %s)", fname, fargs)
	case "return":
		if err := skipTagContents(s); err != nil {
			return false, err
		}
		p.Printf("quicktemplate.ReleaseWriter(qw)")
		p.Printf("return")
	case "break":
		if p.forDepth <= 0 {
			return false, fmt.Errorf("found break tag outside for loop at %s", s.Context())
		}
		if err := skipTagContents(s); err != nil {
			return false, err
		}
		p.Printf("break")
	case "code":
		if err := p.parseCode(); err != nil {
			return false, err
		}
	case "for":
		if err := p.parseFor(); err != nil {
			return false, err
		}
	case "if":
		if err := p.parseIf(); err != nil {
			return false, err
		}
	default:
		return false, nil
	}
	return true, nil
}

func (p *parser) parseCode() error {
	t, err := expectTagContents(p.s)
	if err != nil {
		return err
	}
	p.Printf("%s\n", t.Value)
	return nil
}

func parseFnameFargsNoTypes(s *scanner, f []byte) (string, string, string, error) {
	fname, fargs, err := parseFnameFargs(s, f)
	if err != nil {
		return "", "", "", err
	}

	var args []string
	for _, a := range strings.Split(fargs, ",") {
		a = string(stripLeadingSpace([]byte(a)))
		n := 0
		for n < len(a) && !isSpace(a[n]) {
			n++
		}
		args = append(args, a[:n])
	}
	fargsNoTypes := strings.Join(args, ", ")
	return fname, fargs, fargsNoTypes, nil
}

func parseFnameFargs(s *scanner, f []byte) (string, string, error) {
	// TODO: use real Go parser here
	n := bytes.IndexByte(f, '(')
	if n < 0 {
		return "", "", fmt.Errorf("missing '(' for function arguments at %s", s.Context())
	}
	fname := string(stripTrailingSpace(f[:n]))
	if len(fname) == 0 {
		return "", "", fmt.Errorf("empty function name at %s", s.Context())
	}

	f = f[n+1:]
	n = bytes.LastIndexByte(f, ')')
	if n < 0 {
		return "", "", fmt.Errorf("missing ')' for function arguments at %s", s.Context())
	}
	fargs := string(f[:n])
	return fname, fargs, nil
}

func (p *parser) emitText(text []byte) {
	for len(text) > 0 {
		n := bytes.IndexByte(text, '`')
		if n < 0 {
			p.Printf("qw.s(`%s`)", text)
			return
		}
		p.Printf("qw.s(`%s`)", text[:n])
		p.Printf("qw.s(\"`\")")
		text = text[n+1:]
	}
}

func (p *parser) emitFuncStart(fname, fargs string) {
	p.Printf("func %sStream(w io.Writer, %s) {", fname, fargs)
	p.prefix = "\t"
	p.Printf("qw := quicktemplate.AcquireWriter(w)")
}

func (p *parser) emitFuncEnd(fname, fargs, fargsNoTypes string) {
	p.Printf("quicktemplate.ReleaseWriter(qw)")
	p.prefix = ""
	p.Printf("}\n")

	p.Printf("func %s(%s) string {", fname, fargs)
	p.prefix = "\t"
	p.Printf("bb := quicktemplate.AcquireByteBuffer()")
	p.Printf("%sStream(bb, %s)", fname, fargsNoTypes)
	p.Printf("s := string(bb.Bytes())")
	p.Printf("quicktemplate.ReleaseByteBuffer(bb)")
	p.Printf("return s")
	p.prefix = ""
	p.Printf("}\n")
}

func (p *parser) Printf(format string, args ...interface{}) {
	w := p.w
	fmt.Fprintf(w, "%s", p.prefix)
	p.s.WriteLineComment(w)
	fmt.Fprintf(w, "%s", p.prefix)
	fmt.Fprintf(w, format, args...)
	fmt.Fprintf(w, "\n")
}

func skipTagContents(s *scanner) error {
	_, err := expectTagContents(s)
	return err
}

func expectTagContents(s *scanner) (*token, error) {
	return expectToken(s, tagContents)
}

func expectToken(s *scanner, id int) (*token, error) {
	if !s.Next() {
		return nil, fmt.Errorf("cannot find token %s: %v", tokenIDToStr(id), s.LastError())
	}
	t := s.Token()
	if t.ID != id {
		return nil, fmt.Errorf("unexpected token found %s. Expecting %s at %s", t, tokenIDToStr(id), s.Context())
	}
	return t, nil
}

func getPackageName(filePath string) (string, error) {
	fname := filepath.Base(filePath)
	n := strings.LastIndex(fname, ".")
	if n < 0 {
		n = len(fname)
	}
	packageName := fname[:n]

	if len(packageName) == 0 {
		return "", fmt.Errorf("cannot derive package name from filePath %q", filePath)
	}
	return packageName, nil
}
