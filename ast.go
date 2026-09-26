package jqgo

// AST nodes. The evaluator walks these directly.

type node interface{ isNode() }

type (
	identityNode   struct{}
	recurseAllNode struct{} // ..
	literalNode    struct{ v any }

	// indexNode is term[key]; key is evaluated against the term's input,
	// not against the term's output (jq semantics).
	indexNode struct {
		term   node
		key    node
		name   string // fast path for .foo (key == nil)
		simple bool   // see isSimple
	}
	sliceNode struct {
		term     node
		from, to node // either may be nil
		simple   bool
	}
	iterateNode struct{ term node }
	pipeNode    struct {
		left, right node
		simple      bool
	}
	commaNode struct{ left, right node }
	negNode   struct {
		x      node
		simple bool
	}
	binopNode struct {
		op          string // + - * / % == != < <= > >=
		left, right node
		simple      bool
	}
	andNode struct {
		left, right node
		simple      bool
	}
	orNode struct {
		left, right node
		simple      bool
	}
	altNode    struct{ left, right node } // //
	assignNode struct {
		op          string // = |= += -= *= /= %= //=
		left, right node
	}
	ifNode struct {
		cond, then node
		els        node // nil means identity
		simple     bool
	}
	tryNode struct {
		body  node
		catch node // nil: suppress errors
	}
	reduceNode struct {
		src          node
		pat          pattern
		init, update node
		inPlace      bool // update may modify the accumulator in place
	}
	foreachNode struct {
		src                   node
		pat                   pattern
		init, update, extract node // extract may be nil
	}
	funcDefNode struct {
		def  *funcDef
		rest node
	}
	callNode struct {
		name    string
		args    []node
		argDefs []*funcDef  // args wrapped as zero-arity closures, built once
		lexical bool        // bound by an enclosing def or parameter
		global  *globalFunc // otherwise, resolved at compile time
		simple  bool
	}
	varNode  struct{ name string }
	bindNode struct {
		src  node
		pats []pattern // several with ?// alternatives
		body node
	}
	labelNode struct {
		name string
		body node
	}
	breakNode  struct{ name string }
	arrayNode  struct{ x node } // x nil for []
	objectNode struct {
		entries []objEntry
		simple  bool
	}
	textNode   struct{ s string } // literal text inside a string template
	stringNode struct {
		parts  []node // textNodes and interpolated expressions
		format string // "" or e.g. "base64" for @base64 "..."
		simple bool
	}
	formatNode struct{ name string }
)

type objEntry struct {
	key   node // evaluates to the key string
	value node
}

type funcDef struct {
	name   string
	params []param
	body   node
}

type param struct {
	name  string
	isVar bool
}

// pattern is a destructuring target: $name, [p, ...] or {key: p, ...}.
type pattern struct {
	name  string // set for a plain $name
	elems []pattern
	obj   []objPattern
	kind  byte // 'v' variable, 'a' array, 'o' object
}

type objPattern struct {
	keyVar string // {$name} or {$name: pattern}
	key    node   // literal or computed key
	val    *pattern
}

func (identityNode) isNode()   {}
func (recurseAllNode) isNode() {}
func (literalNode) isNode()    {}
func (*indexNode) isNode()     {}
func (*sliceNode) isNode()     {}
func (*iterateNode) isNode()   {}
func (*pipeNode) isNode()      {}
func (*commaNode) isNode()     {}
func (*negNode) isNode()       {}
func (*binopNode) isNode()     {}
func (*andNode) isNode()       {}
func (*orNode) isNode()        {}
func (*altNode) isNode()       {}
func (*assignNode) isNode()    {}
func (*ifNode) isNode()        {}
func (*tryNode) isNode()       {}
func (*reduceNode) isNode()    {}
func (*foreachNode) isNode()   {}
func (*funcDefNode) isNode()   {}
func (*callNode) isNode()      {}
func (*varNode) isNode()       {}
func (*bindNode) isNode()      {}
func (*labelNode) isNode()     {}
func (*breakNode) isNode()     {}
func (*arrayNode) isNode()     {}
func (*objectNode) isNode()    {}
func (*stringNode) isNode()    {}
func (*formatNode) isNode()    {}
func (textNode) isNode()       {}
