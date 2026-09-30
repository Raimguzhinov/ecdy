// Package calc parses and evaluates arithmetic expressions.
package calc

import (
	"strconv"
	"strings"
)

// Node is an expression tree: a number, or an operator with two operands.
type Node struct {
	Op    byte
	Value float64
	L, R  *Node
}

// Parse parses space-separated tokens: numbers and + - * /.
func Parse(s string) *Node {
	toks := strings.Fields(s)
	return parse(&toks, 0)
}

var prec = map[string]int{"+": 1, "-": 1, "*": 2, "/": 2}

func parse(toks *[]string, min int) *Node {
	v, _ := strconv.ParseFloat((*toks)[0], 64)
	left := &Node{Value: v}
	*toks = (*toks)[1:]
	for len(*toks) > 0 && prec[(*toks)[0]] > min {
		op := (*toks)[0]
		*toks = (*toks)[1:]
		left = &Node{Op: op[0], L: left, R: parse(toks, prec[op])}
	}
	return left
}

// Eval returns the value of the expression.
func Eval(n *Node) float64 {
	if n.Op == 0 {
		return n.Value
	}
	l, r := Eval(n.L), Eval(n.R)
	switch n.Op {
	case '+':
		return l + r
	case '-':
		return l - r
	case '*':
		return l * r
	}
	return r / l
}
