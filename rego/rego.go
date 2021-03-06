// Copyright 2017 The OPA Authors.  All rights reserved.
// Use of this source code is governed by an Apache2
// license that can be found in the LICENSE file.

// Package rego exposes high level APIs for evaluating Rego policies.
package rego

import (
	"context"
	"fmt"
	"strings"

	"github.com/open-policy-agent/opa/ast"
	"github.com/open-policy-agent/opa/metrics"
	"github.com/open-policy-agent/opa/storage"
	"github.com/open-policy-agent/opa/storage/inmem"
	"github.com/open-policy-agent/opa/topdown"
)

const defaultPartialNamespace = "partial"

// PartialResult represents the result of partial evaluation. The result can be
// used to generate a new query that can be run when inputs are known.
type PartialResult struct {
	compiler *ast.Compiler
	store    storage.Store
	body     ast.Body
}

// Rego returns an object that can be evaluated to produce a query result.
func (pr PartialResult) Rego(options ...func(*Rego)) *Rego {
	options = append(options, Compiler(pr.compiler), Store(pr.store), ParsedQuery(pr.body))
	return New(options...)
}

// Result defines the output of Rego evaluation.
type Result struct {
	Expressions []*ExpressionValue `json:"expressions"`
	Bindings    Vars               `json:"bindings,omitempty"`
}

func newResult() Result {
	return Result{
		Bindings: Vars{},
	}
}

// Location defines a position in a Rego query or module.
type Location struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

// ExpressionValue defines the value of an expression in a Rego query.
type ExpressionValue struct {
	Value    interface{} `json:"value"`
	Text     string      `json:"text"`
	Location *Location   `json:"location"`
}

func newExpressionValue(expr *ast.Expr, value interface{}) *ExpressionValue {
	return &ExpressionValue{
		Value: value,
		Text:  string(expr.Location.Text),
		Location: &Location{
			Row: expr.Location.Row,
			Col: expr.Location.Col,
		},
	}
}

// ResultSet represents a collection of output from Rego evaluation. An empty
// result set represents an undefined query.
type ResultSet []Result

// Vars represents a collection of variable bindings. The keys are the variable
// names and the values are the binding values.
type Vars map[string]interface{}

// WithoutWildcards returns a copy of v with wildcard variables removed.
func (v Vars) WithoutWildcards() Vars {
	n := Vars{}
	for k, v := range v {
		if ast.Var(k).IsWildcard() || ast.Var(k).IsGenerated() {
			continue
		}
		n[k] = v
	}
	return n
}

// Errors represents a collection of errors returned when evaluating Rego.
type Errors []error

func (errs Errors) Error() string {
	if len(errs) == 0 {
		return "no error"
	}
	if len(errs) == 1 {
		return fmt.Sprintf("1 error occurred: %v", errs[0].Error())
	}
	buf := []string{fmt.Sprintf("%v errors occurred", len(errs))}
	for _, err := range errs {
		buf = append(buf, err.Error())
	}
	return strings.Join(buf, "\n")
}

// Rego constructs a query and can be evaluated to obtain results.
type Rego struct {
	query            string
	parsedQuery      ast.Body
	pkg              string
	imports          []string
	rawInput         *interface{}
	input            ast.Value
	unknowns         []string
	partialNamespace string
	modules          []rawModule
	compiler         *ast.Compiler
	store            storage.Store
	txn              storage.Transaction
	metrics          metrics.Metrics
	tracer           topdown.Tracer

	termVarID int
}

// Query returns an argument that sets the Rego query.
func Query(q string) func(r *Rego) {
	return func(r *Rego) {
		r.query = q
	}
}

// ParsedQuery returns an argument that sets the Rego query.
func ParsedQuery(q ast.Body) func(r *Rego) {
	return func(r *Rego) {
		r.parsedQuery = q
	}
}

// Package returns an argument that sets the Rego package on the query's
// context.
func Package(p string) func(r *Rego) {
	return func(r *Rego) {
		r.pkg = p
	}
}

// Imports returns an argument that adds a Rego import to the query's context.
func Imports(p []string) func(r *Rego) {
	return func(r *Rego) {
		r.imports = append(r.imports, p...)
	}
}

// Input returns an argument that sets the Rego input document. Input should be
// a native Go value representing the input document.
func Input(x interface{}) func(r *Rego) {
	return func(r *Rego) {
		r.rawInput = &x
	}
}

// Unknowns returns an argument that sets the values to treat as unknown during
// partial evaluation.
func Unknowns(unknowns []string) func(r *Rego) {
	return func(r *Rego) {
		r.unknowns = unknowns
	}
}

// PartialNamespace returns an argument that sets the namespace to use for
// partial evaluation results. The namespace must be a valid package path
// component.
func PartialNamespace(ns string) func(r *Rego) {
	return func(r *Rego) {
		r.partialNamespace = ns
	}
}

// Module returns an argument that adds a Rego module.
func Module(filename, input string) func(r *Rego) {
	return func(r *Rego) {
		r.modules = append(r.modules, rawModule{
			filename: filename,
			module:   input,
		})
	}
}

// Compiler returns an argument that sets the Rego compiler.
func Compiler(c *ast.Compiler) func(r *Rego) {
	return func(r *Rego) {
		r.compiler = c
	}
}

// Store returns an argument that sets the policy engine's data storage layer.
func Store(s storage.Store) func(r *Rego) {
	return func(r *Rego) {
		r.store = s
	}
}

// Transaction returns an argument that sets the transaction to use for storage
// layer operations.
func Transaction(txn storage.Transaction) func(r *Rego) {
	return func(r *Rego) {
		r.txn = txn
	}
}

// Metrics returns an argument that sets the metrics collection and enables instrumentation.
func Metrics(m metrics.Metrics) func(r *Rego) {
	return func(r *Rego) {
		r.metrics = m
	}
}

// Tracer returns an argument that sets the topdown Tracer.
func Tracer(t topdown.Tracer) func(r *Rego) {
	return func(r *Rego) {
		if t != nil {
			r.tracer = t
		}
	}
}

// New returns a new Rego object.
func New(options ...func(*Rego)) *Rego {
	r := &Rego{}

	for _, option := range options {
		option(r)
	}

	if r.compiler == nil {
		r.compiler = ast.NewCompiler()
	}

	if r.store == nil {
		r.store = inmem.New()
	}

	if r.metrics == nil {
		r.metrics = metrics.New()
	}

	return r
}

// Eval evaluates this Rego object and returns a ResultSet.
func (r *Rego) Eval(ctx context.Context) (ResultSet, error) {

	if len(r.query) == 0 && len(r.parsedQuery) == 0 {
		return nil, fmt.Errorf("cannot evaluate empty query")
	}

	parsed, query, err := r.parse()
	if err != nil {
		return nil, err
	}

	query = r.captureTerms(query)

	compiled, err := r.compile(parsed, query)
	if err != nil {
		return nil, err
	}

	txn := r.txn

	if txn == nil {
		txn, err = r.store.NewTransaction(ctx)
		if err != nil {
			return nil, err
		}
		defer r.store.Abort(ctx, txn)
	}

	return r.eval(ctx, compiled, txn)
}

// PartialEval partially evaluates this Rego object and returns a PartialResult.
func (r *Rego) PartialEval(ctx context.Context) (PartialResult, error) {

	if len(r.query) == 0 && len(r.parsedQuery) == 0 {
		return PartialResult{}, fmt.Errorf("cannot evaluate empty query")
	}

	parsed, query, err := r.parse()
	if err != nil {
		return PartialResult{}, err
	}

	query, outputVar, err := rewriteQueryForPartialEval(query)
	if err != nil {
		return PartialResult{}, err
	}

	compiled, err := r.compile(parsed, query)
	if err != nil {
		return PartialResult{}, err
	}

	txn := r.txn

	if txn == nil {
		txn, err = r.store.NewTransaction(ctx)
		if err != nil {
			return PartialResult{}, err
		}
		defer r.store.Abort(ctx, txn)
	}

	return r.partialEval(ctx, compiled, txn, outputVar)
}

func (r *Rego) parse() (map[string]*ast.Module, ast.Body, error) {

	r.metrics.Timer(metrics.RegoQueryParse).Start()
	defer r.metrics.Timer(metrics.RegoQueryParse).Stop()

	var errs Errors
	parsed := map[string]*ast.Module{}

	for _, module := range r.modules {
		p, err := module.Parse()
		if err != nil {
			errs = append(errs, err)
		}
		parsed[module.filename] = p
	}

	var query ast.Body

	if r.parsedQuery != nil {
		query = r.parsedQuery
	} else {
		var err error
		query, err = ast.ParseBody(r.query)
		if err != nil {
			errs = append(errs, err)
		}
		if len(errs) > 0 {
			return nil, nil, errs
		}
	}

	return parsed, query, nil
}

func (r *Rego) compile(modules map[string]*ast.Module, query ast.Body) (ast.Body, error) {

	r.metrics.Timer(metrics.RegoQueryCompile).Start()
	defer r.metrics.Timer(metrics.RegoQueryCompile).Stop()

	if len(modules) > 0 {
		r.compiler.Compile(modules)

		if r.compiler.Failed() {
			var errs Errors
			for _, err := range r.compiler.Errors {
				errs = append(errs, err)
			}
			return nil, errs
		}
	}

	var qctx *ast.QueryContext

	if r.pkg != "" {
		pkg, err := ast.ParsePackage(fmt.Sprintf("package %v", r.pkg))
		if err != nil {
			return nil, err
		}
		qctx = qctx.WithPackage(pkg)
	}

	if len(r.imports) > 0 {
		s := make([]string, len(r.imports))
		for i := range r.imports {
			s[i] = fmt.Sprintf("import %v", r.imports[i])
		}
		imports, err := ast.ParseImports(strings.Join(s, "\n"))
		if err != nil {
			return nil, err
		}
		qctx = qctx.WithImports(imports)
	}

	if r.rawInput != nil {
		val, err := ast.InterfaceToValue(*r.rawInput)
		if err != nil {
			return nil, err
		}
		qctx = qctx.WithInput(val)
		r.input = val
	}

	return r.compiler.QueryCompiler().WithContext(qctx).Compile(query)
}

func (r *Rego) eval(ctx context.Context, compiled ast.Body, txn storage.Transaction) (rs ResultSet, err error) {

	q := topdown.NewQuery(compiled).
		WithCompiler(r.compiler).
		WithStore(r.store).
		WithTransaction(txn).
		WithMetrics(r.metrics)

	if r.tracer != nil {
		q = q.WithTracer(r.tracer)
	}

	if r.input != nil {
		q = q.WithInput(ast.NewTerm(r.input))
	}

	// Cancel query if context is cancelled or deadline is reached.
	c := topdown.NewCancel()
	q = q.WithCancel(c)
	exit := make(chan struct{})
	defer close(exit)
	go waitForDone(ctx, exit, func() {
		c.Cancel()
	})

	exprs := map[*ast.Expr]struct{}{}

	err = q.Iter(ctx, func(qr topdown.QueryResult) error {
		result := newResult()
		for key, value := range qr {
			val, err := ast.JSON(value.Value)
			if err != nil {
				return err
			}
			if !isTermVar(key) {
				if !key.IsWildcard() && !key.IsGenerated() {
					result.Bindings[string(key)] = val
				}
			} else if expr := findExprForTermVar(compiled, key); expr != nil {
				result.Expressions = append(result.Expressions, newExpressionValue(expr, val))
				exprs[expr] = struct{}{}
			}
		}
		for _, expr := range compiled {
			// Don't include expressions without locations. Lack of location
			// indicates it was not parsed and so the caller should not be
			// shown it.
			if _, ok := exprs[expr]; !ok && expr.Location != nil && !expr.Generated {
				result.Expressions = append(result.Expressions, newExpressionValue(expr, true))
			}
		}
		rs = append(rs, result)
		return nil
	})

	if err != nil {
		return nil, err
	}

	if len(rs) == 0 {
		return nil, nil
	}

	return rs, nil
}

func (r *Rego) partialEval(ctx context.Context, compiled ast.Body, txn storage.Transaction, output *ast.Term) (PartialResult, error) {

	var unknowns []*ast.Term

	// Use input document as unknown if caller has not specified any.
	if r.unknowns == nil {
		unknowns = []*ast.Term{ast.InputRootDocument}
	} else {
		unknowns = make([]*ast.Term, len(r.unknowns))
		for i := range r.unknowns {
			var err error
			unknowns[i], err = ast.ParseTerm(r.unknowns[i])
			if err != nil {
				return PartialResult{}, err
			}
		}
	}

	partialNamespace := r.partialNamespace
	if partialNamespace == "" {
		partialNamespace = defaultPartialNamespace
	}

	// Check partial namespace to ensure it's valid.
	if term, err := ast.ParseTerm(partialNamespace); err != nil {
		return PartialResult{}, err
	} else if _, ok := term.Value.(ast.Var); !ok {
		return PartialResult{}, fmt.Errorf("bad partial namespace")
	}

	q := topdown.NewQuery(compiled).
		WithCompiler(r.compiler).
		WithStore(r.store).
		WithTransaction(txn).
		WithMetrics(r.metrics).
		WithUnknowns(unknowns).
		WithPartialNamespace(partialNamespace)

	if r.tracer != nil {
		q = q.WithTracer(r.tracer)
	}

	if r.input != nil {
		q = q.WithInput(ast.NewTerm(r.input))
	}

	// Cancel query if context is cancelled or deadline is reached.
	c := topdown.NewCancel()
	q = q.WithCancel(c)
	exit := make(chan struct{})
	defer close(exit)
	go waitForDone(ctx, exit, func() {
		c.Cancel()
	})

	partials, support, err := q.PartialRun(ctx)
	if err != nil {
		return PartialResult{}, err
	}

	// Construct module for queries.
	module := ast.MustParseModule("package " + partialNamespace)
	module.Rules = make([]*ast.Rule, len(partials))
	for i, body := range partials {
		module.Rules[i] = &ast.Rule{
			Head:   ast.NewHead(ast.Var("__result__"), nil, output),
			Body:   body,
			Module: module,
		}
	}

	// Update compiler with partial evaluation output.
	r.compiler.Modules["__partialresult__"] = module
	for i, module := range support {
		r.compiler.Modules[fmt.Sprintf("__partialsupport%d__", i)] = module
	}

	r.compiler.Compile(r.compiler.Modules)
	if r.compiler.Failed() {
		return PartialResult{}, r.compiler.Errors
	}

	result := PartialResult{
		compiler: r.compiler,
		store:    r.store,
		body:     ast.MustParseBody(fmt.Sprintf("data.%v.__result__", partialNamespace)),
	}

	return result, nil
}

func (r *Rego) captureTerms(query ast.Body) ast.Body {

	// If the query contains expressions that consist of a single term, rewrite
	// those expressions so that we capture the value of the term in a variable
	// that can be included in the result.
	extras := map[*ast.Expr]struct{}{}

	for i := range query {
		if !query[i].Negated {
			if term, ok := query[i].Terms.(*ast.Term); ok {

				// If len(query) > 1 we must still test that evaluated value is
				// not false.
				if len(query) > 1 {
					cpy := query[i].Copy()
					// Unset location so that this expression is not included
					// in the results.
					cpy.Location = nil
					extras[cpy] = struct{}{}
				}

				query[i].Terms = ast.Equality.Expr(term, r.generateTermVar()).Terms
			}
		}
	}

	for expr := range extras {
		query.Append(expr)
	}

	return query
}

func (r *Rego) generateTermVar() *ast.Term {
	r.termVarID++
	return ast.VarTerm(ast.WildcardPrefix + fmt.Sprintf("term%v", r.termVarID))
}

func isTermVar(v ast.Var) bool {
	return strings.HasPrefix(string(v), ast.WildcardPrefix+"term")
}

func findExprForTermVar(query ast.Body, v ast.Var) *ast.Expr {
	for i := range query {
		vis := ast.NewVarVisitor()
		ast.Walk(vis, query[i])
		if vis.Vars().Contains(v) {
			return query[i]
		}
	}
	return nil
}

func waitForDone(ctx context.Context, exit chan struct{}, f func()) {
	select {
	case <-exit:
		return
	case <-ctx.Done():
		f()
		return
	}
}

func rewriteQueryForPartialEval(query ast.Body) (ast.Body, *ast.Term, error) {
	if len(query) != 1 {
		return nil, nil, fmt.Errorf("partial evaluation requires single %v (not multiple %v)", ast.RefTypeName, ast.ExprTypeName)
	}

	term, ok := query[0].Terms.(*ast.Term)
	if !ok {
		return nil, nil, fmt.Errorf("partial evaluation requires %v (not call %v)", ast.RefTypeName, ast.TypeName(query[0]))
	}

	ref, ok := term.Value.(ast.Ref)
	if !ok {
		return nil, nil, fmt.Errorf("partial evaluation requires %v (not %v)", ast.RefTypeName, ast.TypeName(term))
	}

	if !ref.IsGround() {
		return nil, nil, fmt.Errorf("partial evaluation requires ground %v", ast.RefTypeName)
	}

	return ast.NewBody(ast.Equality.Expr(ast.Wildcard, term)), ast.Wildcard, nil
}

type rawModule struct {
	filename string
	module   string
}

func (m rawModule) Parse() (*ast.Module, error) {
	return ast.ParseModule(m.filename, m.module)
}
