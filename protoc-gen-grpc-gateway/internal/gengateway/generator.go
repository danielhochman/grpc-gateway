package gengateway

import (
	"errors"
	"fmt"
	"go/format"
	"path/filepath"
	"strings"

	"github.com/golang/glog"
	"github.com/grpc-ecosystem/grpc-gateway/v2/internal/descriptor"
	gen "github.com/grpc-ecosystem/grpc-gateway/v2/internal/generator"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/pluginpb"
)

var (
	errNoTargetService = errors.New("no target service defined in the file")
)

type pathType int

const (
	pathTypeImport pathType = iota
	pathTypeSourceRelative
)

type generator struct {
	reg                *descriptor.Registry
	baseImports        []descriptor.GoPackage
	useRequestContext  bool
	registerFuncSuffix string
	pathType           pathType
	modulePath         string
	allowPatchFeature  bool
	standalone         bool
}

// New returns a new generator which generates grpc gateway files.
func New(reg *descriptor.Registry, baseImports []descriptor.GoPackage, useRequestContext bool, registerFuncSuffix, pathTypeString, modulePathString string,
	allowPatchFeature, standalone bool) gen.Generator {

	var pathType pathType
	switch pathTypeString {
	case "", "import":
		// paths=import is default
	case "source_relative":
		pathType = pathTypeSourceRelative
	default:
		glog.Fatalf(`Unknown path type %q: want "import" or "source_relative".`, pathTypeString)
	}

	return &generator{
		reg:                reg,
		baseImports:        baseImports,
		useRequestContext:  useRequestContext,
		registerFuncSuffix: registerFuncSuffix,
		pathType:           pathType,
		modulePath:         modulePathString,
		allowPatchFeature:  allowPatchFeature,
		standalone:         standalone,
	}
}

func (g *generator) Generate(targets []*descriptor.File) ([]*descriptor.ResponseFile, error) {
	var files []*descriptor.ResponseFile
	for _, file := range targets {
		glog.V(1).Infof("Processing %s", file.GetName())

		code, err := g.generate(file)
		if err == errNoTargetService {
			glog.V(1).Infof("%s: %v", file.GetName(), err)
			continue
		}
		if err != nil {
			return nil, err
		}
		formatted, err := format.Source([]byte(code))
		if err != nil {
			glog.Errorf("%v: %s", err, code)
			return nil, err
		}

		name, err := g.getFilePath(file)
		if err != nil {
			glog.Errorf("%v: %s", err, code)
			return nil, err
		}
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		filename := fmt.Sprintf("%s.pb.gw.go", base)
		files = append(files, &descriptor.ResponseFile{
			GoPkg: file.GoPkg,
			CodeGeneratorResponse_File: &pluginpb.CodeGeneratorResponse_File{
				Name:    proto.String(filename),
				Content: proto.String(string(formatted)),
			},
		})
	}
	return files, nil
}

func (g *generator) getFilePath(file *descriptor.File) (string, error) {
	name := file.GetName()
	switch {
	case g.modulePath != "" && g.pathType != pathTypeImport:
		return "", errors.New("cannot use module= with paths=")

	case g.modulePath != "":
		trimPath, pkgPath := g.modulePath+"/", file.GoPkg.Path+"/"
		if !strings.HasPrefix(pkgPath, trimPath) {
			return "", fmt.Errorf("%v: file go path does not match module prefix: %v", file.GoPkg.Path, trimPath)
		}
		return filepath.Join(strings.TrimPrefix(pkgPath, trimPath), filepath.Base(name)), nil

	case g.pathType == pathTypeImport && file.GoPkg.Path != "":
		return fmt.Sprintf("%s/%s", file.GoPkg.Path, filepath.Base(name)), nil

	default:
		return name, nil
	}
}

func (g *generator) generate(file *descriptor.File) (string, error) {
	pkgSeen := make(map[string]bool)
	var imports []descriptor.GoPackage
	for _, pkg := range g.baseImports {
		pkgSeen[pkg.Path] = true
		imports = append(imports, pkg)
	}

	if g.standalone {
		imports = append(imports, file.GoPkg)
	}

	for _, svc := range file.Services {
		for _, m := range svc.Methods {
			imports = append(imports, g.addEnumPathParamImports(file, m, pkgSeen)...)
			pkg := m.RequestType.File.GoPkg
			if len(m.Bindings) == 0 ||
				pkg == file.GoPkg || pkgSeen[pkg.Path] {
				continue
			}
			pkgSeen[pkg.Path] = true
			imports = append(imports, pkg)
		}
	}
	params := param{
		File:               file,
		Imports:            imports,
		UseRequestContext:  g.useRequestContext,
		RegisterFuncSuffix: g.registerFuncSuffix,
		AllowPatchFeature:  g.allowPatchFeature,
	}
	if g.reg != nil {
		params.OmitPackageDoc = g.reg.GetOmitPackageDoc()
	}
	return applyTemplate(params, g.reg)
}

// addEnumPathParamImports handles adding import of enum path parameter go packages
func (g *generator) addEnumPathParamImports(file *descriptor.File, m *descriptor.Method, pkgSeen map[string]bool) []descriptor.GoPackage {
	var imports []descriptor.GoPackage
	for _, b := range m.Bindings {
		for _, p := range b.PathParams {
			e, err := g.reg.LookupEnum("", p.Target.GetTypeName())
			if err != nil {
				continue
			}
			pkg := e.File.GoPkg
			if pkg == file.GoPkg || pkgSeen[pkg.Path] {
				continue
			}
			pkgSeen[pkg.Path] = true
			imports = append(imports, pkg)
		}
	}
	return imports
}
