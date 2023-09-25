package resolvergen_v2

import (
	_ "embed"
	"fmt"
	"go/ast"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"

	"github.com/99designs/gqlgen/codegen"
	"github.com/99designs/gqlgen/codegen/config"
	"github.com/99designs/gqlgen/codegen/templates"
	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/internal/rewrite"
	"github.com/99designs/gqlgen/plugin"
)

//go:embed resolver_single_file.gotpl
var resolverSingleTemplate string

//go:embed resolver_schema.gotpl
var resolverSchemaTemplate string

//go:embed resolver_interface.gotpl
var resolverInterfaceTemplate string

func New() plugin.Plugin {
	return &Plugin{}
}

type Plugin struct{}

var _ plugin.CodeGenerator = &Plugin{}

func (m *Plugin) Name() string {
	return "resolvergen_v2"
}

func (m *Plugin) GenerateCode(data *codegen.Data) error {
	if !data.Config.Resolver.IsDefined() {
		return nil
	}

	switch data.Config.Resolver.Layout {
	case config.LayoutSingleFile:
		return m.generateSingleFile(data)
	case config.LayoutFollowSchema:
		return m.generatePerSchema(data)
	}

	return nil
}

func (m *Plugin) generateSingleFile(data *codegen.Data) error {
	file := File{}

	if _, err := os.Stat(data.Config.Resolver.Filename); err == nil {
		// file already exists and we do not support updating resolvers with layout = single so just return
		return nil
	}

	for _, o := range data.Objects {
		if o.HasResolvers() {
			file.Objects = append(file.Objects, o)
		}
		for _, f := range o.Fields {
			if !f.IsResolver {
				continue
			}

			resolver := Resolver{o, f, nil, "", `panic("not implemented")`}
			file.Resolvers = append(file.Resolvers, &resolver)
		}
	}

	resolverBuild := &ResolverBuild{
		File:                &file,
		PackageName:         data.Config.Resolver.Package,
		ResolverType:        data.Config.Resolver.Type,
		HasRoot:             true,
		OmitTemplateComment: data.Config.Resolver.OmitTemplateComment,
	}

	newResolverTemplate := resolverSingleTemplate
	//if data.Config.Resolver.ResolverTemplate != "" {
	//	newResolverTemplate = readResolverTemplate(data.Config.Resolver.ResolverTemplate)
	//}

	return templates.Render(templates.Options{
		PackageName: data.Config.Resolver.Package,
		FileNotice:  `// THIS CODE IS A STARTING POINT ONLY. IT WILL NOT BE UPDATED WITH SCHEMA CHANGES.`,
		Filename:    data.Config.Resolver.Filename,
		Data:        resolverBuild,
		Packages:    data.Config.Packages,
		Template:    newResolverTemplate,
	})
}

func getSubDirs(baseDir string) ([]string, error) {
	var subDirs []string
	err := filepath.Walk(baseDir, func(path string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			subDirs = append(subDirs, filepath.ToSlash(path))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return subDirs, nil
}

func (m *Plugin) generatePerSchema(data *codegen.Data) error {
	subDirs, err := getSubDirs(data.Config.Resolver.Dir())
	if err != nil {
		return err
	}
	multiRewriter, err := rewrite.NewMultiRewriter(subDirs)
	if err != nil {
		return err
	}

	//rewriter, err := rewrite.New(data.Config.Resolver.Dir())
	//if err != nil {
	//	return err
	//}

	files := map[string]*File{}

	objects := make(codegen.Objects, len(data.Objects)+len(data.Inputs))
	copy(objects, data.Objects)
	copy(objects[len(data.Objects):], data.Inputs)

	//1、生成resolver.go
	moduleMap := make(map[string]string) // {path: alias}
	for _, o := range objects {
		//fn默认为resolver.go文件绝对路径
		fn := data.Config.Resolver.Filename
		dirpath := filepath.ToSlash(filepath.Dir(fn))
		//type Mutation、type Query的object.HasResolvers()返回true
		//生成resolver.go的type mutationResolver、type queryResolver的定义
		if o.HasResolvers() {
			if files[fn] == nil {
				files[fn] = &File{}
			}
			caser := cases.Title(language.English, cases.NoLower)
			multiRewriter.MarkStructCopied(dirpath, templates.LcFirst(o.Name)+templates.UcFirst(data.Config.Resolver.Type))
			multiRewriter.GetMethodBody(dirpath, data.Config.Resolver.Type, caser.String(o.Name))
			files[fn].Objects = append(files[fn].Objects, o)
		}

		for _, f := range o.Fields {
			//生成resolver.go中各种自定义函数
			if !f.IsResolver {
				continue
			}
			if files[fn] == nil {
				files[fn] = &File{}
			}
			structName := templates.LcFirst(o.Name) + templates.UcFirst(data.Config.Resolver.Type)
			comment := strings.TrimSpace(strings.TrimLeft(multiRewriter.GetMethodComment(dirpath, structName, f.GoFieldName), `\`))

			gqlname := strings.TrimPrefix(f.Position.Src.Name, data.Config.Resolver.SchemaDirName)
			gqldir := filepath.Dir(gqlname) //resolver自定义函数所在子目录，相对于resolver根目录
			var modAlias string
			//生成的文件在resolver根目录时，resolver.go中可以直接使用，无需import
			if strings.Trim(gqldir, "/") != "" && strings.Trim(gqldir, "\\") != "" {
				var ok bool
				//resolver.go中需要调用的各子模块的自定义函数，记录module alias和module path关系，后面需要load module
				modPath := filepath.ToSlash(filepath.Join(data.Config.Resolver.ImportPath(), gqldir))
				modAlias, ok = moduleMap[modPath]
				if !ok {
					modAlias = fmt.Sprintf("mod%v", len(moduleMap)+1)
					moduleMap[modPath] = modAlias
				}
			}
			//加载已有文件的函数实现，避免覆盖
			implementation := strings.TrimSpace(multiRewriter.GetMethodBody(dirpath, structName, f.GoFieldName))
			if implementation == "" {
				// Check for Implementer Plugin
				var resolver_implementer plugin.ResolverImplementer
				var exists bool
				for _, p := range data.Plugins {
					if p_cast, ok := p.(plugin.ResolverImplementer); ok {
						resolver_implementer = p_cast
						exists = true
						break
					}
				}
				if exists {
					implementation = resolver_implementer.Implement(f)
				} else {
					var args []string
					args = append(args, "ctx")
					for _, arg := range f.Args {
						args = append(args, arg.Name)
					}
					if modAlias != "" {
						//子目录需使用alias，并在后面import包
						implementation = fmt.Sprintf("return %v.%v(%v)", modAlias, f.GoFieldName, strings.Join(args, ","))
					} else {
						//同级目录直接使用函数名，无需import
						implementation = fmt.Sprintf("return %v(%v)", f.GoFieldName, strings.Join(args, ","))
					}
				}
			}
			resolver := Resolver{o, f, multiRewriter.GetPrevDecl(dirpath, structName, f.GoFieldName), comment, implementation}
			files[fn].Resolvers = append(files[fn].Resolvers, &resolver)
		}
	}
	//2、生成schema自定义resolver
	for _, o := range objects {
		for _, f := range o.Fields {
			//生成子目录中各种自定义函数
			if !f.IsResolver {
				continue
			}
			fn := gqlToResolverName(data.Config.Resolver.Dir(), f.Position.Src.Name,
				data.Config.Resolver.FilenameTemplate, data.Config.Resolver.SchemaDirName)
			dirpath := filepath.ToSlash(filepath.Dir(fn))
			structName := ""
			comment := strings.TrimSpace(strings.TrimLeft(multiRewriter.GetMethodComment(dirpath, structName, f.GoFieldName), `\`))
			//加载已有文件的函数实现，避免覆盖
			implementation := strings.TrimSpace(multiRewriter.GetMethodBody(dirpath, structName, f.GoFieldName))
			if implementation == "" {
				// Check for Implementer Plugin
				var resolver_implementer plugin.ResolverImplementer
				var exists bool
				for _, p := range data.Plugins {
					if p_cast, ok := p.(plugin.ResolverImplementer); ok {
						resolver_implementer = p_cast
						exists = true
						break
					}
				}
				if exists {
					implementation = resolver_implementer.Implement(f)
				} else {
					implementation = fmt.Sprintf("panic(fmt.Errorf(\"not implemented: %v - %v\"))", f.GoFieldName, f.Name)
				}
			}
			resolver := Resolver{o, f, multiRewriter.GetPrevDecl(dirpath, structName, f.GoFieldName), comment, implementation}
			if files[fn] == nil {
				files[fn] = &File{}
			}
			files[fn].Resolvers = append(files[fn].Resolvers, &resolver)
		}
	}
	//3、加载module，保留已有代码
	for filename, file := range files {
		if filename != data.Config.Resolver.Filename {
			dirpath := filepath.ToSlash(filepath.Dir(filename))
			file.imports = multiRewriter.ExistingImports(dirpath, filename)
			file.RemainingSource = multiRewriter.RemainingSource(dirpath, filename)
		} else {
			for modPath, modAlias := range moduleMap {
				file.imports = append(file.imports, rewrite.Import{
					Alias:      modAlias,
					ImportPath: modPath,
				})
				data.Config.Packages.Load(modPath)
			}
		}
	}
	//4、生成所有resolver相关文件
	for filename, file := range files {
		resolverBuild := &ResolverBuild{
			File: file,
			//PackageName:         data.Config.Resolver.Package,
			PackageName:         filepath.Base(filepath.Dir(filename)), //使用目录作为包名
			ResolverType:        data.Config.Resolver.Type,
			OmitTemplateComment: data.Config.Resolver.OmitTemplateComment,
		}
		var fileNotice strings.Builder
		if !data.Config.OmitGQLGenFileNotice {
			fileNotice.WriteString(`
			// This file will be automatically regenerated based on the schema,
            // any custom resolver implementations will be reserved.
			// Code generated by github.com/99designs/gqlgen`,
			)
			if !data.Config.OmitGQLGenVersionInFileNotice {
				fileNotice.WriteString(` version `)
				fileNotice.WriteString(graphql.Version)
			}
		}
		//resolver.go和用户定义schema生成的resolver文件采用不同template
		var resolverTemplate string
		if filename == data.Config.Resolver.Filename {
			resolverTemplate = resolverInterfaceTemplate
		} else {
			resolverTemplate = resolverSchemaTemplate
		}
		err := templates.Render(templates.Options{
			//PackageName: data.Config.Resolver.Package,
			PackageName: filepath.Base(filepath.Dir(filename)), //使用目录作为包名
			FileNotice:  fileNotice.String(),
			Filename:    filename,
			Data:        resolverBuild,
			Packages:    data.Config.Packages,
			Template:    resolverTemplate,
		})
		if err != nil {
			return err
		}
	}
	return nil
}

type ResolverBuild struct {
	*File
	HasRoot             bool
	PackageName         string
	ResolverType        string
	OmitTemplateComment bool
}

type File struct {
	// These are separated because the type definition of the resolver object may live in a different file from the
	// resolver method implementations, for example when extending a type in a different graphql schema file
	Objects         []*codegen.Object
	Resolvers       []*Resolver
	imports         []rewrite.Import
	RemainingSource string
}

func (f *File) Imports() string {
	for _, imp := range f.imports {
		if imp.Alias == "" {
			_, _ = templates.CurrentImports.Reserve(imp.ImportPath)
		} else {
			_, _ = templates.CurrentImports.Reserve(imp.ImportPath, imp.Alias)
		}
	}
	return ""
}

type Resolver struct {
	Object         *codegen.Object
	Field          *codegen.Field
	PrevDecl       *ast.FuncDecl
	Comment        string
	Implementation string
}

// 获取resolver子目录中文件绝对路径名
func gqlToResolverName(base, gqlname, filenameTmpl, schemaDir string) string {
	gqlname = strings.TrimPrefix(gqlname, schemaDir)
	ext := filepath.Ext(gqlname)
	gqldir := filepath.Dir(gqlname) // /datamark/persist
	if filenameTmpl == "" {
		filenameTmpl = "{name}.go"
	}
	filename := strings.ReplaceAll(filenameTmpl, "{name}", strings.TrimSuffix(filepath.Base(gqlname), ext))
	return filepath.ToSlash(filepath.Join(base, gqldir, filename))
}

func readResolverTemplate(customResolverTemplate string) string {
	contentBytes, err := os.ReadFile(customResolverTemplate)
	if err != nil {
		panic(err)
	}
	return string(contentBytes)
}
