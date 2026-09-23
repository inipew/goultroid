package calculator

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxExpressionBytes = 128
	maxParserDepth     = 32
)

type expressionParser struct {
	runes []rune
	pos   int
	depth int
}

func evaluateExpression(input string) (float64, error) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > maxExpressionBytes {
		return 0, fmt.Errorf("invalid expression length")
	}
	p := &expressionParser{runes: []rune(input)}
	value, err := p.parseExpression()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.pos != len(p.runes) {
		return 0, fmt.Errorf("unexpected token %q", p.runes[p.pos])
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("non-finite result")
	}
	return value, nil
}

func (p *expressionParser) parseExpression() (float64, error) {
	left, err := p.parseTerm()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		switch p.peek() {
		case '+':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			left += right
		case '-':
			p.pos++
			right, err := p.parseTerm()
			if err != nil {
				return 0, err
			}
			left -= right
		default:
			return left, nil
		}
	}
}

func (p *expressionParser) parseTerm() (float64, error) {
	left, err := p.parsePower()
	if err != nil {
		return 0, err
	}
	for {
		p.skipSpace()
		op := p.peek()
		if op != '*' && op != '/' && op != '%' {
			return left, nil
		}
		p.pos++
		right, err := p.parsePower()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, fmt.Errorf("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, fmt.Errorf("modulo by zero")
			}
			left = math.Mod(left, right)
		}
		if math.IsNaN(left) || math.IsInf(left, 0) {
			return 0, fmt.Errorf("non-finite result")
		}
	}
}

func (p *expressionParser) parsePower() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	p.skipSpace()
	if p.peek() != '^' {
		return left, nil
	}
	p.pos++
	if err := p.enter(); err != nil {
		return 0, err
	}
	right, err := p.parsePower()
	p.leave()
	if err != nil {
		return 0, err
	}
	value := math.Pow(left, right)
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("non-finite result")
	}
	return value, nil
}

func (p *expressionParser) parseUnary() (float64, error) {
	p.skipSpace()
	switch p.peek() {
	case '+':
		p.pos++
		if err := p.enter(); err != nil {
			return 0, err
		}
		value, err := p.parseUnary()
		p.leave()
		return value, err
	case '-':
		p.pos++
		if err := p.enter(); err != nil {
			return 0, err
		}
		value, err := p.parseUnary()
		p.leave()
		return -value, err
	default:
		return p.parsePrimary()
	}
}

func (p *expressionParser) parsePrimary() (float64, error) {
	p.skipSpace()
	if p.peek() == '(' {
		p.pos++
		if err := p.enter(); err != nil {
			return 0, err
		}
		value, err := p.parseExpression()
		p.leave()
		if err != nil {
			return 0, err
		}
		p.skipSpace()
		if p.peek() != ')' {
			return 0, fmt.Errorf("missing closing parenthesis")
		}
		p.pos++
		return value, nil
	}
	return p.parseNumber()
}

func (p *expressionParser) parseNumber() (float64, error) {
	p.skipSpace()
	start := p.pos
	dot := false
	digits := 0
	for p.pos < len(p.runes) {
		r := p.runes[p.pos]
		if r >= '0' && r <= '9' {
			digits++
			p.pos++
			continue
		}
		if r == '.' && !dot {
			dot = true
			p.pos++
			continue
		}
		break
	}
	if digits == 0 {
		return 0, fmt.Errorf("number expected")
	}
	if p.pos < len(p.runes) && (p.runes[p.pos] == 'e' || p.runes[p.pos] == 'E') {
		exponentStart := p.pos
		p.pos++
		if p.pos < len(p.runes) && (p.runes[p.pos] == '+' || p.runes[p.pos] == '-') {
			p.pos++
		}
		exponentDigits := p.pos
		for p.pos < len(p.runes) && p.runes[p.pos] >= '0' && p.runes[p.pos] <= '9' {
			p.pos++
		}
		if exponentDigits == p.pos {
			p.pos = exponentStart
		}
	}
	value, err := strconv.ParseFloat(string(p.runes[start:p.pos]), 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %w", err)
	}
	return value, nil
}

func (p *expressionParser) enter() error {
	if p.depth >= maxParserDepth {
		return fmt.Errorf("expression nesting exceeds %d", maxParserDepth)
	}
	p.depth++
	return nil
}

func (p *expressionParser) leave() {
	if p.depth > 0 {
		p.depth--
	}
}

func (p *expressionParser) skipSpace() {
	for p.pos < len(p.runes) && unicode.IsSpace(p.runes[p.pos]) {
		p.pos++
	}
}

func (p *expressionParser) peek() rune {
	if p.pos >= len(p.runes) {
		return 0
	}
	return p.runes[p.pos]
}

func formatResult(value float64) string {
	if value == 0 {
		return "0"
	}
	return strconv.FormatFloat(value, 'g', 12, 64)
}
