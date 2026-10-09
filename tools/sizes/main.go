// Command sizes prints how much machine code each Go module contributes to a
// Go shared library. It reads the function table from .gopclntab, so it also
// works on stripped Android libraries.
package main

import (
	"debug/elf"
	"debug/gosym"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
)

func main() {
	top := flag.Int("top", 12, "number of rows to print")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: sizes [-top N] libgojni.so")
		os.Exit(2)
	}
	if err := run(flag.Arg(0), *top); err != nil {
		fmt.Fprintln(os.Stderr, "sizes:", err)
		os.Exit(1)
	}
}

func run(path string, top int) error {
	file, err := elf.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	text, pcln := file.Section(".text"), file.Section(".gopclntab")
	if text == nil || pcln == nil {
		return fmt.Errorf("%s has no .text or .gopclntab", path)
	}
	data, err := pcln.Data()
	if err != nil {
		return err
	}
	table, err := gosym.NewTable(nil, gosym.NewLineTable(data, text.Addr))
	if err != nil {
		return err
	}
	sizes := map[string]uint64{}
	var total uint64
	for _, fn := range table.Funcs {
		size := fn.End - fn.Entry
		if size == 0 || size > text.Size {
			continue
		}
		sizes[group(fn.Sym.PackageName())] += size
		total += size
	}
	type row struct {
		name string
		size uint64
	}
	var rows []row
	for name, size := range sizes {
		rows = append(rows, row{name, size})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].size > rows[j].size })
	fmt.Println("| package | code | share |")
	fmt.Println("|---|---|---|")
	for i, r := range rows {
		if i == top {
			break
		}
		fmt.Printf("| `%s` | %.2f MB | %.1f%% |\n", r.name, float64(r.size)/(1<<20), 100*float64(r.size)/float64(total))
	}
	fmt.Printf("\nTotal code: %.2f MB in %d functions.\n", float64(total)/(1<<20), len(table.Funcs))
	return nil
}

func group(pkg string) string {
	parts := strings.Split(pkg, "/")
	switch {
	case strings.HasPrefix(pkg, "github.com/xtls/xray-core/"):
		return strings.Join(parts[:min(len(parts), 5)], "/")
	case parts[0] == "github.com", parts[0] == "golang.org":
		return strings.Join(parts[:min(len(parts), 3)], "/")
	case parts[0] == "google.golang.org", parts[0] == "gvisor.dev":
		return strings.Join(parts[:min(len(parts), 2)], "/")
	case !strings.Contains(parts[0], "."):
		return strings.Join(parts[:min(len(parts), 2)], "/")
	}
	return pkg
}
