// Unless explicitly stated otherwise all files in this repository are licensed
// under the MIT License.
// This product includes software developed at Guance Cloud (https://www.guance.com/).
// Copyright 2021-present Guance, Inc.
//
// ====================================================================================
// Copyright 2015 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package parser

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"

	"github.com/GuanceCloud/ppl/internal/logger"
	"github.com/GuanceCloud/ppl/pkg/ast"

	plToken "github.com/GuanceCloud/ppl/pkg/token"
)

var log logger.Logger = logger.NewStdoutLogger("iploc", "debug")

func InitLog(logger logger.Logger) {
	log = logger
}

var parserPool = sync.Pool{
	New: func() interface{} {
		return &parser{}
	},
}

type parser struct {
	lex      Lexer
	yyParser yyParserImpl

	parseResult ast.Stmts
	lastClosing plToken.Pos
	errs        ParseErrors

	inject    ItemType
	injecting bool
}

func (p *parser) InjectItem(typ ItemType) {
	if p.injecting {
		log.Warnf("current inject is %v, new inject is %v", p.inject, typ)
		panic("cannot inject multiple Items into the token stream")
	}

	if typ != 0 && (typ <= startSymbolsStart || typ >= startSymbolsEnd) {
		log.Warnf("current inject is %v", typ)
		panic("cannot inject symbol that isn't start symbol")
	}
	p.inject = typ
	p.injecting = true
}

var errUnexpected = errors.New("unexpected error")

func (p *parser) unexpected(context string, expected string) {
	var errMsg strings.Builder

	if p.yyParser.lval.item.Typ == ERROR { // do not report lex error twice
		return
	}

	errMsg.WriteString("unexpected: ")
	errMsg.WriteString(p.yyParser.lval.item.desc())

	if context != "" {
		errMsg.WriteString(" in: ")
		errMsg.WriteString(context)
	}

	if expected != "" {
		errMsg.WriteString(", expected: ")
		errMsg.WriteString(expected)
	}

	p.addParseErr(p.yyParser.lval.item.PositionRange(), errors.New(errMsg.String()))
}

func (p *parser) recover(errp *error) {
	e := recover() //nolint: ifshort
	if _, ok := e.(runtime.Error); ok {
		buf := make([]byte, 64<<10) // 64k
		buf = buf[:runtime.Stack(buf, false)]
		fmt.Fprintf(os.Stderr, "parser panic: %v\n%s", e, buf)
		*errp = errUnexpected
	} else if e != nil {
		if x, ok := e.(error); ok {
			*errp = x
		}
	}
}

func (p *parser) addParseErr(pr *PositionRange, err error) {
	p.errs = append(p.errs, ParseError{
		Pos:   pr,
		Err:   err,
		Query: p.lex.input,
	})
}

func (p *parser) addParseErrf(pr *PositionRange, format string, args ...interface{}) {
	p.addParseErr(pr, fmt.Errorf(format, args...))
}

func (p *parser) unquoteString(s string) string {
	unq, err := Unquote(s)
	if err != nil {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"error unquoting string %q: %s", s, err)
		return ""
	}
	return unq
}

func (p *parser) unquoteMultilineString(s string) string {
	unq, err := UnquoteMultiline(s)
	if err != nil {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"error unquoting multiline string %q: %s", s, err)
		return ""
	}
	return unq
}

// literal

func (p *parser) newBoolLiteral(pos plToken.Pos, val bool) *ast.Node {
	return ast.WrapBoolLiteral(&ast.BoolLiteral{
		Val:   val,
		Start: pos,
	})
}

func (p *parser) newNilLiteral(pos plToken.Pos) *ast.Node {
	return ast.WrapNilLiteral(&ast.NilLiteral{
		Start: pos,
	})
}

func (p *parser) newIdentifierLiteral(name Item) *ast.Node {
	return ast.WrapIdentifier(&ast.Identifier{
		Name:  name.Val,
		Start: name.Pos,
	})
}

func (p *parser) newStringLiteral(val Item) *ast.Node {
	return ast.WrapStringLiteral(&ast.StringLiteral{
		Val:   val.Val,
		Start: val.Pos,
	})
}

func (p *parser) newParenExpr(lParen Item, node *ast.Node, rParen Item) *ast.Node {
	return ast.WrapParenExpr(&ast.ParenExpr{
		Param:  node,
		LParen: lParen.Pos,
		RParen: rParen.Pos,
	})
}

func (p *parser) newListInitStartExpr(pos plToken.Pos) *ast.Node {
	return ast.WrapListInitExpr(&ast.ListInitExpr{
		List:     []*ast.Node{},
		LBracket: pos,
	})
}

func (p *parser) newListInitAppendExpr(initExpr *ast.Node, elem *ast.Node) *ast.Node {
	if initExpr.NodeType != ast.TypeListInitExpr {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"%s object is not ListInitExpr", initExpr.NodeType)
		return nil
	}

	initExpr.ListInitExpr.List = append(initExpr.ListInitExpr.List, elem)
	return initExpr
}

func (p *parser) newListInitEndExpr(initExpr *ast.Node, pos plToken.Pos) *ast.Node {
	if initExpr.NodeType != ast.TypeListInitExpr {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"%s object is not ListInitExpr", initExpr.NodeType)
		return nil
	}
	initExpr.ListInitExpr.RBracket = pos
	return initExpr
}

func (p *parser) newMapInitStartExpr(pos plToken.Pos) *ast.Node {
	return ast.WrapMapInitExpr(&ast.MapInitExpr{
		KeyValeList: [][2]*ast.Node{},
		LBrace:      pos,
	})
}

func (p *parser) newMapInitAppendExpr(initExpr *ast.Node, keyNode *ast.Node, valueNode *ast.Node) *ast.Node {
	if initExpr.NodeType != ast.TypeMapInitExpr {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"%s object is not MapInitExpr", initExpr.NodeType)
		return nil
	}

	initExpr.MapInitExpr.KeyValeList = append(initExpr.MapInitExpr.KeyValeList,
		[2]*ast.Node{keyNode, valueNode})
	return initExpr
}

func (p *parser) newMapInitEndExpr(initExpr *ast.Node, pos plToken.Pos) *ast.Node {
	if initExpr.NodeType != ast.TypeMapInitExpr {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			"%s object is not MapInitExpr", initExpr.NodeType)
		return nil
	}
	initExpr.MapInitExpr.RBrace = pos
	return initExpr
}

func (p *parser) newNumberLiteral(v Item) *ast.Node {
	if n, err := strconv.ParseInt(v.Val, 0, 64); err != nil {
		f, err := strconv.ParseFloat(v.Val, 64)
		if err != nil {
			p.addParseErrf(p.yyParser.lval.item.PositionRange(),
				"error parsing number: %s", err)
			return nil
		}
		return ast.WrapFloatLiteral(&ast.FloatLiteral{
			Val:   f,
			Start: v.Pos,
		})
	} else {
		return ast.WrapIntegerLiteral(&ast.IntegerLiteral{
			Val:   n,
			Start: v.Pos,
		})
	}
}

func (p *parser) newBlockStmt(lBrace Item, stmts ast.Stmts, rBrace Item) *ast.BlockStmt {
	return &ast.BlockStmt{
		LBracePos: lBrace.Pos,
		Stmts:     stmts,
		RBracePos: rBrace.Pos,
	}
}

func (p *parser) newBreakStmt(pos plToken.Pos) *ast.Node {
	return ast.WrapBreakStmt(&ast.BreakStmt{
		Start: pos,
	})
}

func (p *parser) newContinueStmt(pos plToken.Pos) *ast.Node {
	return ast.WrapContinueStmt(&ast.ContinueStmt{
		Start: pos,
	})
}

func (p *parser) newForStmt(initExpr *ast.Node, condExpr *ast.Node, loopExpr *ast.Node, body *ast.BlockStmt) *ast.Node {
	pos := p.yyParser.lval.item.PositionRange()

	return ast.WrapForStmt(&ast.ForStmt{
		Init: initExpr,
		Loop: loopExpr,
		Cond: condExpr,
		Body: body,

		ForPos: pos.Start,
	})
}

func (p *parser) newForInStmt(varb *ast.Node, iter *ast.Node, body *ast.BlockStmt, forTk, inTk Item) *ast.Node {
	switch varb.NodeType { //nolint:exhaustive
	case ast.TypeIdentifier:
	default:
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "%s object is not identifier", varb.NodeType)
		return nil
	}

	switch iter.NodeType { //nolint:exhaustive
	case ast.TypeBoolLiteral, ast.TypeNilLiteral,
		ast.TypeIntegerLiteral, ast.TypeFloatLiteral:
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "%s object is not iterable", iter.NodeType)
		return nil
	}

	return ast.WrapForInStmt(&ast.ForInStmt{
		Varb:   varb,
		Iter:   iter,
		Body:   body,
		ForPos: forTk.Pos,
		InPos:  inTk.Pos,
	})
}

func (p *parser) newIfElifStmt(ifElifList []*ast.IfStmtElem) *ast.Node {
	if len(ifElifList) == 0 {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "invalid ifelse stmt is empty")
		return nil
	}

	return ast.WrapIfelseStmt(
		&ast.IfelseStmt{
			IfList: ast.IfList(ifElifList),
		},
	)
}

func (p *parser) newIfElifelseStmt(ifElifList []*ast.IfStmtElem,
	elseTk Item, elseElem *ast.BlockStmt,
) *ast.Node {
	if len(ifElifList) == 0 {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "invalid ifelse stmt is empty")
		return nil
	}

	return ast.WrapIfelseStmt(
		&ast.IfelseStmt{
			IfList:  ast.IfList(ifElifList),
			Else:    elseElem,
			ElsePos: elseTk.Pos,
		},
	)
}

func (p *parser) newIfElem(ifTk Item, condition *ast.Node, block *ast.BlockStmt) *ast.IfStmtElem {
	if condition == nil {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "invalid if/elif condition")
		return nil
	}

	ifElem := &ast.IfStmtElem{
		Condition: condition,
		Block:     block,
		Start:     ifTk.Pos,
	}

	return ifElem
}

func (p *parser) newAssignmentExpr(l, r *ast.Node, eqOp Item) *ast.Node {
	return ast.WrapAssignmentExpr(&ast.AssignmentExpr{
		LHS:   l,
		RHS:   r,
		OpPos: eqOp.Pos,
	})
}

func (p *parser) newConditionalExpr(l, r *ast.Node, op Item) *ast.Node {
	return ast.WrapConditionExpr(&ast.ConditionalExpr{
		RHS:   r,
		LHS:   l,
		Op:    AstOp(op.Typ),
		OpPos: op.Pos,
	})
}

func (p *parser) newArithmeticExpr(l, r *ast.Node, op Item) *ast.Node {
	switch op.Typ {
	case DIV, MOD: // div 0 or mod 0
		switch r.NodeType { //nolint:exhaustive
		case ast.TypeFloatLiteral:
			if r.FloatLiteral.Val == 0 {
				p.addParseErrf(p.yyParser.lval.item.PositionRange(), "division or modulo by zero")
				return nil
			}
		case ast.TypeIntegerLiteral:
			if r.IntegerLiteral.Val == 0 {
				p.addParseErrf(p.yyParser.lval.item.PositionRange(), "division or modulo by zero")
				return nil
			}
		}
	}

	return ast.WrapArithmeticExpr(
		&ast.ArithmeticExpr{
			RHS: r,
			LHS: l,
			Op:  AstOp(op.Typ),

			OpPos: op.Pos,
		},
	)
}

func (p *parser) newAttrExpr(obj, attr *ast.Node) *ast.Node {
	pos := p.yyParser.lval.item.PositionRange()

	return ast.WrapAttrExpr(&ast.AttrExpr{
		Obj:   obj,
		Attr:  attr,
		Start: pos.Start,
	})
}

func (p *parser) newIndexExpr(obj *ast.Node, lBracket Item, index *ast.Node, rBracket Item) *ast.Node {
	if index == nil {
		p.addParseErrf(p.yyParser.lval.item.PositionRange(), "invalid array index is emepty")
		return nil
	}

	if obj == nil {
		// .[idx]
		return ast.WrapIndexExpr(&ast.IndexExpr{
			Index:    []*ast.Node{index},
			LBracket: []plToken.Pos{lBracket.Pos},
			RBracket: []plToken.Pos{rBracket.Pos},
		})
	}

	switch obj.NodeType { //nolint:exhaustive
	case ast.TypeIdentifier:
		return ast.WrapIndexExpr(&ast.IndexExpr{
			Obj: obj.Identifier, Index: []*ast.Node{index},
			LBracket: []plToken.Pos{lBracket.Pos},
			RBracket: []plToken.Pos{rBracket.Pos},
		})
	case ast.TypeIndexExpr:
		obj.IndexExpr.Index = append(obj.IndexExpr.Index, index)
		obj.IndexExpr.LBracket = append(obj.IndexExpr.LBracket, lBracket.Pos)
		obj.IndexExpr.RBracket = append(obj.IndexExpr.RBracket, rBracket.Pos)
		return obj
	default:
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			fmt.Sprintf("invalid indexExpr object type %s", obj.NodeType))
	}
	return nil
}

func (p *parser) newCallExpr(fn *ast.Node, args []*ast.Node, lParen, rParen Item) *ast.Node {
	var fname string

	switch fn.NodeType { //nolint:exhaustive
	case ast.TypeIdentifier:
		fname = fn.Identifier.Name
	default:
		p.addParseErrf(p.yyParser.lval.item.PositionRange(),
			fmt.Sprintf("invalid fn name object type %s", fn.NodeType))
		return nil
	}
	f := &ast.CallExpr{
		Name:    fname,
		NamePos: fn.Identifier.Start,
		LParen:  lParen.Pos,
		RParen:  rParen.Pos,
	}

	// TODO: key-value param support
	f.Param = append(f.Param, args...)

	return ast.WrapCallExpr(f)
}

// end of yylex.(*parser).newXXXX

// impl Lex interface.
func (p *parser) Lex(lval *yySymType) int {
	var typ ItemType

	if p.injecting {
		p.injecting = false
		return int(p.inject)
	}

	for { // skip comment
		p.lex.NextItem(&lval.item)
		typ = lval.item.Typ
		if typ != COMMENT {
			break
		}
	}

	switch typ {
	case ERROR:
		pos := PositionRange{
			Start: p.lex.start,
			End:   plToken.Pos(len(p.lex.input)),
		}

		p.addParseErr(&pos, errors.New(p.yyParser.lval.item.Val))
		return 0 // tell yacc it's the end of input

	case EOF:
		lval.item.Typ = EOF
		p.InjectItem(0)
	case RIGHT_PAREN:
		p.lastClosing = lval.item.Pos + plToken.Pos(len(lval.item.Val))
	}
	return int(typ)
}

func (p *parser) Error(e string) {}

func newParser(input string) *parser {
	p, ok := parserPool.Get().(*parser)
	if !ok {
		return nil
	}

	p.injecting = false
	p.errs = nil
	p.parseResult = nil
	p.lex = Lexer{
		input: input,
		state: lexStatements,
	}
	return p
}

func ParsePipeline(input string) (res ast.Stmts, err error) {
	p := newParser(input)
	defer parserPool.Put(p)
	defer p.recover(&err)

	p.InjectItem(START_STMTS)
	p.yyParser.Parse(p)

	if p.parseResult != nil {
		res = p.parseResult
	}

	if len(p.errs) != 0 {
		err = p.errs
	}

	return res, err
}
