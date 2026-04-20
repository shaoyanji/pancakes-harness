// Go AST symbol extraction for search index enrichment.
//
// Attempts to parse event text as Go source. On success, extracts function names,
// type names, method names (with receivers), and package names as searchable symbols.
// On parse failure, returns empty — caller falls back to plain text tokenization.
package memory

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
)

// extractSymbols attempts to parse src as Go source and extract symbol names.
// Returns (symbols, nil) on success, or (nil, err) if parsing fails.
// Symbols are returned in original case (not lowered) — the caller handles casing.
func extractSymbols(src string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "input.go", src, parser.ParseComments)
	if err != nil {
		return nil, err
	}

	var symbols []string

	// Package name
	if f.Name != nil {
		symbols = append(symbols, f.Name.Name)
	}

	// Walk top-level declarations
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			symbols = append(symbols, extractFuncSymbols(d)...)
		case *ast.GenDecl:
			symbols = append(symbols, extractGenDeclSymbols(d)...)
		}
	}

	// Walk comments for doc terms (function/type doc often contains key terms)
	for _, cg := range f.Comments {
		for _, c := range cg.List {
			comments := tokenize(c.Text)
			// Only include comment tokens that look like identifiers (>= 3 chars)
			for _, t := range comments {
				if len(t) >= 3 {
					symbols = append(symbols, t)
				}
			}
		}
	}

	return symbols, nil
}

// extractFuncSymbols extracts names from function declarations.
// For methods, includes the receiver type name as context.
func extractFuncSymbols(fn *ast.FuncDecl) []string {
	var symbols []string

	if fn.Name != nil {
		symbols = append(symbols, fn.Name.Name)
	}

	// Method receiver type
	if fn.Recv != nil {
		for _, field := range fn.Recv.List {
			if star, ok := field.Type.(*ast.StarExpr); ok {
				if ident, ok := star.X.(*ast.Ident); ok {
					symbols = append(symbols, ident.Name)
				}
			} else if ident, ok := field.Type.(*ast.Ident); ok {
				symbols = append(symbols, ident.Name)
			}
		}
	}

	// Parameter type names (useful for "find methods that take Context" queries)
	if fn.Type.Params != nil {
		for _, field := range fn.Type.Params.List {
			symbols = append(symbols, extractTypeNames(field.Type)...)
		}
	}

	// Return type names
	if fn.Type.Results != nil {
		for _, field := range fn.Type.Results.List {
			symbols = append(symbols, extractTypeNames(field.Type)...)
		}
	}

	return symbols
}

// extractGenDeclSymbols extracts names from general declarations (type, var, const, import).
func extractGenDeclSymbols(decl *ast.GenDecl) []string {
	var symbols []string

	for _, spec := range decl.Specs {
		switch s := spec.(type) {
		case *ast.TypeSpec:
			if s.Name != nil {
				symbols = append(symbols, s.Name.Name)
			}
			// Extract embedded type names from struct fields
			if st, ok := s.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					symbols = append(symbols, extractTypeNames(field.Type)...)
					// Named fields
					for _, name := range field.Names {
						symbols = append(symbols, name.Name)
					}
				}
			}
			// Extract method names from interface types
			if it, ok := s.Type.(*ast.InterfaceType); ok {
				for _, method := range it.Methods.List {
					for _, name := range method.Names {
						symbols = append(symbols, name.Name)
					}
				}
			}
		case *ast.ValueSpec:
			for _, name := range s.Names {
				symbols = append(symbols, name.Name)
			}
		case *ast.ImportSpec:
			if s.Path != nil {
				// Extract last path component as a searchable term
				path := strings.Trim(s.Path.Value, `"`)
				parts := strings.Split(path, "/")
				if len(parts) > 0 {
					symbols = append(symbols, parts[len(parts)-1])
				}
			}
		}
	}

	return symbols
}

// extractTypeNames recursively pulls identifier names from a type expression.
func extractTypeNames(expr ast.Expr) []string {
	var names []string

	switch t := expr.(type) {
	case *ast.Ident:
		names = append(names, t.Name)
	case *ast.StarExpr:
		names = append(names, extractTypeNames(t.X)...)
	case *ast.SelectorExpr:
		// qualified name like bytes.Buffer
		if x, ok := t.X.(*ast.Ident); ok {
			names = append(names, x.Name)
		}
		if t.Sel != nil {
			names = append(names, t.Sel.Name)
		}
	case *ast.ArrayType:
		names = append(names, extractTypeNames(t.Elt)...)
	case *ast.MapType:
		names = append(names, extractTypeNames(t.Key)...)
		names = append(names, extractTypeNames(t.Value)...)
	case *ast.FuncType:
		// Don't recurse into func type params — too noisy
	case *ast.InterfaceType:
		// Anonymous interface — don't recurse
	case *ast.ChanType:
		names = append(names, extractTypeNames(t.Value)...)
	}

	return names
}
