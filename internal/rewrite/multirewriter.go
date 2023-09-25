package rewrite

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/99designs/gqlgen/internal/code"
	"golang.org/x/tools/go/packages"
)

type MultiRewriter struct {
	//pkg    *packages.Package
	pkgs   map[string]*packages.Package // {dirpath: package}
	files  map[string]string
	copied map[ast.Decl]bool
}

func NewMultiRewriter(dirs []string) (*MultiRewriter, error) {
	//pkgs采用完整目录路径映射package，用于区分不同子目录
	pkgMap := make(map[string]*packages.Package)
	for _, dir := range dirs {
		importPath := code.ImportPathForDir(dir)
		if importPath == "" {
			return nil, fmt.Errorf("import path not found for directory: %q", dir)
		}
		pkgs, err := packages.Load(&packages.Config{
			Mode: packages.NeedSyntax | packages.NeedTypes,
		}, importPath)
		if err != nil {
			return nil, err
		}
		if len(pkgs) == 0 {
			return nil, fmt.Errorf("package not found for importPath: %s", importPath)
		}
		pkgMap[filepath.ToSlash(dir)] = pkgs[0]
	}
	return &MultiRewriter{
		pkgs:   pkgMap,
		files:  map[string]string{},
		copied: map[ast.Decl]bool{},
	}, nil
}

func (r *MultiRewriter) getSource(dirpath string, start, end token.Pos) string {
	startPos := r.pkgs[dirpath].Fset.Position(start)
	endPos := r.pkgs[dirpath].Fset.Position(end)

	if startPos.Filename != endPos.Filename {
		panic("cant get source spanning multiple files")
	}

	file := r.getFile(startPos.Filename)
	return file[startPos.Offset:endPos.Offset]
}

func (r *MultiRewriter) getFile(filename string) string {
	if _, ok := r.files[filename]; !ok {
		b, err := os.ReadFile(filename)
		if err != nil {
			panic(fmt.Errorf("unable to load file, already exists: %w", err))
		}

		r.files[filename] = string(b)

	}

	return r.files[filename]
}

func (r *MultiRewriter) GetPrevDecl(dirpath, structname, methodname string) *ast.FuncDecl {
	pkg, ok := r.pkgs[dirpath]
	if !ok {
		return nil
	}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			d, isFunc := d.(*ast.FuncDecl)
			if !isFunc {
				continue
			}
			if d.Name.Name != methodname {
				continue
			}
			//function/method
			//d为function时，Recv为nil。d为method时，Recv不为nil
			if d.Recv == nil {
				if structname != "" {
					continue
				}
			} else {
				if len(d.Recv.List) == 0 {
					continue
				}
				recv := d.Recv.List[0].Type
				if star, isStar := recv.(*ast.StarExpr); isStar {
					recv = star.X
				}
				ident, ok := recv.(*ast.Ident)
				if !ok {
					continue
				}
				if ident.Name != structname {
					continue
				}
			}

			r.copied[d] = true

			return d
		}
	}
	return nil
}

func (r *MultiRewriter) GetMethodComment(dirpath, structname, methodname string) string {
	d := r.GetPrevDecl(dirpath, structname, methodname)
	if d != nil {
		return d.Doc.Text()
	}
	return ""
}

func (r *MultiRewriter) GetMethodBody(dirpath, structname, methodname string) string {
	d := r.GetPrevDecl(dirpath, structname, methodname)
	if d != nil {
		return r.getSource(dirpath, d.Body.Pos()+1, d.Body.End()-1)
	}
	return ""
}

func (r *MultiRewriter) MarkStructCopied(dirpath, name string) {
	pkg, ok := r.pkgs[dirpath]
	if !ok {
		return
	}
	for _, f := range pkg.Syntax {
		for _, d := range f.Decls {
			d, isGen := d.(*ast.GenDecl)
			if !isGen {
				continue
			}
			if d.Tok != token.TYPE || len(d.Specs) == 0 {
				continue
			}

			spec, isTypeSpec := d.Specs[0].(*ast.TypeSpec)
			if !isTypeSpec {
				continue
			}

			if spec.Name.Name != name {
				continue
			}

			r.copied[d] = true
		}
	}
}

func (r *MultiRewriter) ExistingImports(dirpath, filename string) []Import {
	filename, err := filepath.Abs(filename)
	if err != nil {
		panic(err)
	}
	pkg, ok := r.pkgs[dirpath]
	if !ok {
		return nil
	}
	for _, f := range pkg.Syntax {
		pos := pkg.Fset.Position(f.Pos())

		if filename != pos.Filename {
			continue
		}

		var imps []Import
		for _, i := range f.Imports {
			name := ""
			if i.Name != nil {
				name = i.Name.Name
			}
			path, err := strconv.Unquote(i.Path.Value)
			if err != nil {
				panic(err)
			}
			imps = append(imps, Import{name, path})
		}
		return imps
	}
	return nil
}

func (r *MultiRewriter) RemainingSource(dirpath, filename string) string {
	filename, err := filepath.Abs(filename)
	if err != nil {
		panic(err)
	}
	pkg, ok := r.pkgs[dirpath]
	if !ok {
		return ""
	}
	for _, f := range pkg.Syntax {
		pos := pkg.Fset.Position(f.Pos())

		if filename != pos.Filename {
			continue
		}

		var buf bytes.Buffer

		for _, d := range f.Decls {
			if r.copied[d] {
				continue
			}

			if d, isGen := d.(*ast.GenDecl); isGen && d.Tok == token.IMPORT {
				continue
			}

			buf.WriteString(r.getSource(dirpath, d.Pos(), d.End()))
			buf.WriteString("\n")
		}

		return strings.TrimSpace(buf.String())
	}
	return ""
}

//type Import struct {
//	Alias      string
//	ImportPath string
//}
