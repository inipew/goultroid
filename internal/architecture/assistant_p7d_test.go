package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"testing"
)

func interfaceMethodNames(t *testing.T, path, typeName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var names []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok || typeSpec.Name.Name != typeName {
				continue
			}
			iface, ok := typeSpec.Type.(*ast.InterfaceType)
			if !ok {
				t.Fatalf("%s in %s is not an interface", typeName, path)
			}
			for _, field := range iface.Methods.List {
				for _, name := range field.Names {
					names = append(names, name.Name)
				}
			}
		}
	}
	sort.Strings(names)
	return names
}

func TestP7DManagerQueryBoundariesRemainReadOnly(t *testing.T) {
	root := repositoryRoot(t)

	commandMethods := interfaceMethodNames(
		t,
		filepath.Join(root, "internal", "assistant", "command", "servicer.go"),
		"GroupQueryReader",
	)
	if len(commandMethods) != 1 || commandMethods[0] != "GetFullChat" {
		t.Fatalf("GroupQueryReader surface=%v, want read-only [GetFullChat]", commandMethods)
	}

	transportMethods := interfaceMethodNames(
		t,
		filepath.Join(root, "internal", "assistant", "client", "group_query.go"),
		"groupQueryAPI",
	)
	want := []string{"ChannelsGetFullChannel", "MessagesGetFullChat"}
	if len(transportMethods) != len(want) {
		t.Fatalf("groupQueryAPI surface=%v, want %v", transportMethods, want)
	}
	for i := range want {
		if transportMethods[i] != want[i] {
			t.Fatalf("groupQueryAPI surface=%v, want %v", transportMethods, want)
		}
	}
}
