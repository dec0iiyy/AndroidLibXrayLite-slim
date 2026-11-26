// Command slim turns Xray-core into the slim variant. It copies the module
// that the checkout requires, hides the implementation files of the packages
// listed in hollow.txt, rewrites main/distro/all without the imports listed in
// excluded.txt and points the checkout at the copy with a go.mod replace
// directive. Then it compiles for android/arm64, restores every hidden file
// that the remaining code still needs, and finally checks that none of the
// packages in forbidden.txt is linked.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	xrayModule    = "github.com/xtls/xray-core"
	maxIterations = 25
)

var buildTargets = []string{
	xrayModule + "/core",
	xrayModule + "/infra/conf/...",
	xrayModule + "/main/distro/all",
}

var (
	undefinedName = regexp.MustCompile(`undefined: (?:(\w+)\.)?(\w+)`)
	missingMember = regexp.MustCompile(`\.\w+ undefined \(type \*?(?:(\w+)\.)?\w+ has no field or method (\w+)\)`)
	packageHeader = regexp.MustCompile(`^# (\S+)`)
	errorLine     = regexp.MustCompile(`^\S+\.go:\d+:\d+: (.*)$`)
)

type symbol struct {
	rel     string
	pkgName string
}

type slim struct {
	src, config, copyDir string
	xrayDir              string
	tags                 string
	hidden               map[string]bool
	index                map[string][]symbol
}

func main() {
	src := flag.String("src", "", "libv2ray checkout (a Go module that requires xray-core)")
	config := flag.String("config", "", "directory with hollow.txt, forbidden.txt and excluded.txt")
	out := flag.String("xray", "", "directory to write the patched copy of xray-core to")
	tags := flag.String("tags", "grpcnotrace", "Go build tags of the real build, so that the check compiles the same files")
	flag.Parse()
	if *src == "" || *config == "" || *out == "" {
		flag.Usage()
		os.Exit(2)
	}
	s := &slim{src: abs(*src), config: abs(*config), copyDir: abs(*out), tags: *tags, hidden: map[string]bool{}, index: map[string][]symbol{}}
	if err := s.run(); err != nil {
		fmt.Fprintln(os.Stderr, "slim:", err)
		os.Exit(1)
	}
}

func abs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		panic(err)
	}
	return a
}

func (s *slim) run() error {
	if _, err := s.goOutput(nil, "mod", "edit", "-dropreplace", xrayModule); err != nil {
		return err
	}
	dir, err := s.goOutput(nil, "list", "-m", "-f", "{{.Dir}}", xrayModule)
	if err != nil {
		return fmt.Errorf("cannot locate %s: %s", xrayModule, dir)
	}
	s.xrayDir = strings.TrimSpace(dir)
	if err := copyTree(s.xrayDir, s.copyDir); err != nil {
		return err
	}
	if err := s.installDistro(); err != nil {
		return err
	}
	if err := s.hideHollowPackages(); err != nil {
		return err
	}
	if out, err := s.goOutput(nil, "mod", "edit", "-replace", xrayModule+"="+s.copyDir); err != nil {
		return fmt.Errorf("go mod edit: %s", out)
	}
	restored := map[string]bool{}
	for i := 0; i < maxIterations; i++ {
		output, err := s.build()
		if err == nil {
			return s.finish(restored)
		}
		files := s.filesToRestore(output)
		if len(files) == 0 {
			return fmt.Errorf("the slim build does not compile and nothing can be restored:\n%s", output)
		}
		for _, rel := range files {
			if err := copyFile(filepath.Join(s.xrayDir, rel), filepath.Join(s.copyDir, rel)); err != nil {
				return err
			}
			delete(s.hidden, rel)
			restored[rel] = true
		}
	}
	return fmt.Errorf("no fixed point after %d iterations", maxIterations)
}

// installDistro keeps every import of the upstream main/distro/all except the
// ones in excluded.txt. Whatever Xray-core adds, renames or drops there is
// followed automatically, so a config never fails with "not registered"
// because a feature was left out of a hand-kept list.
func (s *slim) installDistro() error {
	rel := filepath.Join("main", "distro", "all")
	files, err := goFiles(filepath.Join(s.copyDir, rel))
	if err != nil || len(files) == 0 {
		return fmt.Errorf("%s not found in %s", rel, s.xrayDir)
	}
	list, err := os.ReadFile(filepath.Join(s.config, "excluded.txt"))
	if err != nil {
		return err
	}
	excluded := strings.Fields(string(list))
	used := map[string]bool{}
	var kept, dropped []string
	pkgName := ""
	for _, f := range files {
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, parser.ImportsOnly)
		if err != nil {
			return fmt.Errorf("upstream distro: %w", err)
		}
		pkgName = file.Name.Name
		for _, imp := range file.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if entry := matchExcluded(strings.TrimPrefix(path, xrayModule+"/"), excluded); entry != "" {
				used[entry] = true
				dropped = append(dropped, path)
				continue
			}
			kept = append(kept, path)
		}
	}
	for _, entry := range excluded {
		if !used[entry] {
			fmt.Printf("slim: %s is no longer imported by the upstream distro, excluded.txt can drop it\n", entry)
		}
	}
	sort.Strings(kept)
	sort.Strings(dropped)
	fmt.Printf("slim: distro keeps %d imports, drops %d %v\n", len(kept), len(dropped), dropped)
	var b strings.Builder
	fmt.Fprintf(&b, "// Code generated by slim from the upstream main/distro/all. DO NOT EDIT.\n\npackage %s\n\nimport (\n", pkgName)
	for _, path := range kept {
		fmt.Fprintf(&b, "\t_ %q\n", path)
	}
	b.WriteString(")\n")
	for _, f := range files[1:] {
		if err := os.Remove(f); err != nil {
			return err
		}
	}
	return os.WriteFile(files[0], []byte(b.String()), 0o644)
}

// matchExcluded returns the entry of excluded.txt that matches the package, a
// path relative to the module. An entry is either an exact path or "*/name",
// which matches every package whose last element is name.
func matchExcluded(pkg string, excluded []string) string {
	for _, entry := range excluded {
		if suffix, ok := strings.CutPrefix(entry, "*"); ok && strings.HasSuffix(pkg, suffix) || pkg == entry {
			return entry
		}
	}
	return ""
}

func (s *slim) hideHollowPackages() error {
	list, err := os.ReadFile(filepath.Join(s.config, "hollow.txt"))
	if err != nil {
		return err
	}
	for _, line := range strings.Fields(string(list)) {
		dir := filepath.Join(s.xrayDir, filepath.FromSlash(line))
		files, err := goFiles(dir)
		if errors.Is(err, fs.ErrNotExist) {
			fmt.Printf("slim: hollow package %s is gone from Xray-core, skipping it\n", line)
			continue
		}
		if err != nil {
			return fmt.Errorf("hollow package %s: %w", line, err)
		}
		for _, f := range files {
			if strings.HasSuffix(f, ".pb.go") {
				continue
			}
			rel, _ := filepath.Rel(s.xrayDir, f)
			s.hidden[rel] = true
			s.indexFile(f, rel)
			if err := os.Remove(filepath.Join(s.copyDir, rel)); err != nil {
				return err
			}
		}
	}
	return nil
}

func goFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(dir, name))
	}
	sort.Strings(files)
	return files, nil
}

func (s *slim) indexFile(path, rel string) {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return
	}
	add := func(name string) {
		s.index[name] = append(s.index[name], symbol{rel: rel, pkgName: file.Name.Name})
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			add(d.Name.Name)
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					add(sp.Name.Name)
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						add(n.Name)
					}
				}
			}
		}
	}
}

func (s *slim) goOutput(extraEnv []string, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd := exec.Command("go", args...)
	cmd.Dir = s.src
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Stdout, cmd.Stderr = &buf, &buf
	err := cmd.Run()
	return buf.String(), err
}

func (s *slim) build() (string, error) {
	args := append([]string{"build", "-tags=" + s.tags, "-o", os.DevNull}, buildTargets...)
	return s.goOutput([]string{"GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0"}, args...)
}

func (s *slim) filesToRestore(output string) []string {
	found := map[string]bool{}
	current := ""
	for _, line := range strings.Split(output, "\n") {
		if m := packageHeader.FindStringSubmatch(line); m != nil {
			current = strings.TrimPrefix(m[1], xrayModule+"/")
			continue
		}
		m := errorLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		qualifier, name := "", ""
		if mm := missingMember.FindStringSubmatch(m[1]); mm != nil {
			qualifier, name = mm[1], mm[2]
		} else if mm := undefinedName.FindStringSubmatch(m[1]); mm != nil {
			qualifier, name = mm[1], mm[2]
		} else {
			continue
		}
		for _, sym := range s.index[name] {
			if !s.hidden[sym.rel] {
				continue
			}
			if qualifier != "" && sym.pkgName != qualifier {
				continue
			}
			if qualifier == "" && filepath.ToSlash(filepath.Dir(sym.rel)) != current {
				continue
			}
			found[sym.rel] = true
		}
	}
	var files []string
	for f := range found {
		files = append(files, f)
	}
	sort.Strings(files)
	return files
}

func (s *slim) finish(restored map[string]bool) error {
	forbidden, err := os.ReadFile(filepath.Join(s.config, "forbidden.txt"))
	if err != nil {
		return err
	}
	args := append([]string{"list", "-tags=" + s.tags, "-deps", "-f", "{{.ImportPath}} {{join .Imports \" \"}}"}, buildTargets...)
	listing, err := s.goOutput([]string{"GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0"}, args...)
	if err != nil {
		return fmt.Errorf("go list failed:\n%s", listing)
	}
	var names []string
	for f := range restored {
		names = append(names, f)
	}
	sort.Strings(names)
	var violations []string
	for _, line := range strings.Split(listing, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		for _, imp := range fields[1:] {
			for _, bad := range strings.Fields(string(forbidden)) {
				if imp == bad || strings.HasPrefix(imp, bad+"/") {
					violations = append(violations, fmt.Sprintf("%s imports %s", fields[0], imp))
				}
			}
		}
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("forbidden packages are linked again, add the importer to hollow.txt:\n  %s\nfiles restored from hidden packages: %v", strings.Join(violations, "\n  "), names)
	}
	fmt.Printf("slim: %d files hidden, %d restored %v\n", len(s.hidden), len(names), names)
	return nil
}

func copyTree(from, to string) error {
	if err := os.RemoveAll(to); err != nil {
		return err
	}
	return filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, path)
		target := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(to, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
