package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	log "github.com/golang/glog"
	"github.com/golangq/q"
	"github.com/jpillora/opts"
)

var (
	version string = "dev"
	date    string = "na"
	commit  string = "na"
)

type root struct {
}

type versionCmd struct {
	help string
}

func main() {
	opts.New(&root{}).
		Name("godna").
		EmbedGlobalFlagSet().
		Complete().
		Version(version).
		AddCommand(
			opts.New(
				&regen{
					OutputDir: ".",
					// Pass: []string{
					// 	"protoc,modinit,modreplace",
					// 	"modtidy",
					// 	"gittag",
					// },
				}).
				Name("regen").
				ConfigPath("./.dna-cfg.json")).
		Parse().
		RunFatal()
}

func (r *versionCmd) Run() {
	fmt.Printf("version: %s\ndate: %s\ncommit: %s\n", version, date, commit)
	fmt.Println(r.help)
}

type regen struct {
	HostOwner string   `help:"host and owner of the repo of the generated code eg. github.com/microsoft"`
	RepoName  string   `help:"repo name of the generated code eg go-vscode"`
	Includes  []string `help:"protoc path -I eg [ '../microsoft-dna/vendor' ]"`
	RelImps   []string `help:"prefix of go.mod local replacements. eg [ 'services-' ] - todo remove"`
	Require   []string `help:"list of go mod edit -require= eg [ 'github.com/golangq/q@v1.0.7' ]"`
	SrcDir    string   `help:"source directory eg ../microsoft-dna/store/project - note not included as a -I"`
	OutputDir string   `help:"output directory eg ."`
	Pass      []string `help:"common separated list of commands to run. Possible commands are protoc,modinit,modrequire,modreplace,modtidy,gittag (default [\"protoc,modinit,modrequire,modreplace\", \"modtidy\", \"gittag\"])"`
	Plugin    []string `help:"Name and path of a pluging eg protoc-gen-NAME=path/to/mybinary. Much also specify --generator, does not imply it  is present"`
	Generator []string `help:"Name and params a generator. Form name[=key=value[,[key=value]]*]?[:out_dir]?. See defaut for an example. Turns into '--NAME_out=PARMAS:OUTPUT_DIR'. (default 'go=paths=source_relative')"`
	//
	packages     map[string]*pkage
	dir2pkg      map[string]*pkage
	pkgWalkOrder []string
	sems         map[string]map[int64]Semvers
	longestStr   int
	// localName    map[string]struct{}
	generators []generator
	relOutDir  map[string]struct{}
}

type generator struct {
	name   string
	params []keyval
	outdir string
}

type keyval struct {
	key   string
	value string
}

type pkage struct {
	gopkg        string
	files        []file
	replacements map[string]*pkage
	dirn         string
	// mymod        string
	gitDescribe string
	source      string
}

type file struct {
	name        string
	protoImport []string
}

func (in *regen) Run() error {
	var err error
	in.SrcDir, err = filepath.Abs(in.SrcDir)
	if err != nil {
		return err
	}
	for i, inc := range in.Includes {
		in.Includes[i], err = filepath.Abs(inc)
		if err != nil {
			return err
		}
	}
	in.OutputDir, err = filepath.Abs(in.OutputDir)
	if err != nil {
		return err
	}
	os.Chdir(in.OutputDir)
	// fmt.Printf("%+v\n", in)
	in.packages = make(map[string]*pkage)
	in.dir2pkg = make(map[string]*pkage)
	// in.localName = make(map[string]struct{})
	in.relOutDir = make(map[string]struct{})
	if in.HostOwner == "" {
		log.Error("--host_owner not set")
		os.Exit(1)
	}
	if in.RepoName == "" {
		log.Errorf("--repo_name not set")
		os.Exit(1)
	}
	pkgExecs := map[string]pkgExec{
		"protoc":     in.protoc,
		"modinit":    in.modinit,
		"modrequire": in.modrequire,
		"modreplace": in.modreplace,
		"modtidy":    in.modtidy,
		"gittag":     in.gittag,
	}
	if len(in.Pass) == 0 {
		in.Pass = []string{
			"protoc,modinit,modrequire,modreplace",
			"modtidy",
			"gittag",
		}
	}
	if len(in.Generator) == 0 {
		in.Generator = []string{"go=paths=source_relative"}
	}
	for _, ges := range in.Generator {
		name := ges
		outdir := ""
		if i := strings.LastIndex(ges, ":"); i > -1 {
			outdir = ges[i+1:]
			ges = ges[:i]
			in.relOutDir[outdir] = struct{}{}
		} else {
			in.relOutDir["."] = struct{}{}
		}
		if i := strings.Index(ges, "="); i > -1 {
			name = ges[:i]
			gen := generator{name: name, outdir: outdir}
			paramstr := ges[i+1:]
			params := strings.Split(paramstr, ",")
			for _, param := range params {
				kv := strings.Split(param, "=")
				if len(kv) != 2 {
					fmt.Printf("Invalid generator, much be of the form 'name[=[key=value][,key=value]*]?' given '%s'", ges)
				}
				gen.params = append(gen.params, keyval{kv[0], kv[1]})
			}
			in.generators = append(in.generators, gen)
		} else {
			gen := generator{name: name, outdir: outdir}
			in.generators = append(in.generators, gen)
		}
	}
	for dir, _ := range in.relOutDir {
		out := filepath.Join(in.OutputDir, dir)
		err := os.MkdirAll(out, os.ModePerm)
		if err != nil {
			return err
		}
	}
	if err := filepath.Walk(in.SrcDir, in.walkFnSrcDir); err != nil {
		log.Error(err)
		os.Exit(1)
	}
	// q.Q(in.localName)
	for _, pkg := range in.pkgWalkOrder {
		// in.goModReplacements(pkg)
		in.pkgDir(pkg)
	}
	for _, pkg := range in.pkgWalkOrder {
		// in.goModReplacements(pkg)
		in.pkgImports(pkg)
	}
	in.sems = gitGetTagSemver()
	for pi, actions := range in.Pass {
		for _, pkg := range in.pkgWalkOrder {
			if !strings.HasPrefix(pkg, in.HostOwner) {
				continue
			}
			fmt.Printf("pass:%d %s %s", pi+1, pkg, strings.Repeat(" ", in.longestStr-len(pkg)))
			for _, ac := range strings.Split(actions, ",") {
				if acf, ex := pkgExecs[ac]; ex {
					if out, msg, err := acf(pkg); err != nil {
						log.Errorf("error executing %s: msg: %s %s\n%s", ac, msg, err, out)
						os.Exit(1)
					} else {
						fmt.Printf(" %s:%s", ac, msg)
					}
				} else {
					log.Errorf("action does not exist %s", ac)
					os.Exit(1)
				}
			}
			fmt.Printf("\n")
		}

	}
	return nil
}

type pkgExec func(pkg string) (out []byte, msg string, err error)

var goPkgOptRe = regexp.MustCompile(`(?m)^option go_package = (.*);`)
var protoImportRe = regexp.MustCompile(`(?m)^import "(.*)/[^/]+.proto";`)

func (in *regen) walkFnSrcDir(path string, info os.FileInfo, err error) error {
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || !strings.HasSuffix(path, ".proto") {
		return nil
	}
	if rel, err := filepath.Rel(in.SrcDir, path); err != nil {
		return err
	} else {
		// TODO make sure there are no v2 files in v1 (root) dir
		// TODO make sure the are no vX in vY directory
		content, err := ioutil.ReadFile(path)
		if err != nil {
			return err
		}
		var pkgName string
		if match := goPkgOptRe.FindSubmatch(content); len(match) > 0 {
			pn, err := strconv.Unquote(string(match[1]))
			if err != nil {
				return err
			}
			pkgName = pn
		}
		if p := strings.IndexRune(pkgName, ';'); p > 0 {
			pkgName = pkgName[:p]
		}
		if pkgName == "" {
			return fmt.Errorf("No package in file %s\n", path)
		}
		thisPkg, ex := in.packages[pkgName]
		if !ex {
			thisPkg = &pkage{
				gopkg:        pkgName,
				replacements: make(map[string]*pkage),
			}
			in.packages[pkgName] = thisPkg
			in.pkgWalkOrder = append(in.pkgWalkOrder, pkgName)
			if len(pkgName) > in.longestStr {
				in.longestStr = len(pkgName)
			}
		}
		//
		protoImportMatch := protoImportRe.FindAllSubmatch(content, -1)
		imps := []string{}
		for _, m := range protoImportMatch {
			imps = append(imps, string(m[1]))
		}
		fi := file{rel, imps}
		thisPkg.files = append(thisPkg.files, fi)
		return nil
	}
}

func (in *regen) pkgDir(pkg string) {
	tpkg := in.packages[pkg]
	fnames := tpkg.files
	dirns := make(map[string]struct{})
	dirn := ""
	for _, fn := range fnames {
		dirns[filepath.Dir(fn.name)] = struct{}{}
		dirn = filepath.Dir(fn.name)
	}
	if len(dirns) != 1 {
		log.Errorf("error files with same go package in more than one dir: %s\n", fnames)
		os.Exit(1)
	}
	tpkg.dirn = dirn
	in.dir2pkg[dirn] = tpkg
	{ //
		cmd := exec.Command("git")
		cmd.Dir = filepath.Join(in.SrcDir, dirn)
		args := []string{"remote", "get-url", "origin"}
		cmd.Args = append(cmd.Args, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			q.Q(err)
		} else {
			tpkg.source = strings.TrimSpace(string(out))
		}
	} //
	{ //
		cmd := exec.Command("git")
		cmd.Dir = filepath.Join(in.SrcDir, dirn)
		args := []string{"describe", "--always", "--dirty"}
		cmd.Args = append(cmd.Args, args...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			q.Q(err)
		} else {
			tpkg.gitDescribe = strings.TrimSpace(string(out))
		}
	} //
}

func (in *regen) pkgImports(pkg string) {
	tpkg := in.packages[pkg]
	for _, fn := range tpkg.files {
		for _, imp := range fn.protoImport {
			if strings.HasPrefix(imp, tpkg.dirn) {
				continue
			}
			for _, rel := range in.RelImps {
				if strings.HasPrefix(imp, rel) {
					if !strings.Contains(imp, "/") {
						dep := in.dir2pkg[imp]
						tpkg.replacements[imp] = dep
					} else {
					}
				} else {
				}
			}
		}
	}
}

// protoc executes the "protoc" command on files named in fnames,
// passing go_out and include flags specified in goOut and includes respectively.
// protoc returns combined output from stdout and stderr.
func (in *regen) protoc(pkg string) ([]byte, string, error) {
	cmd := exec.Command("protoc")
	args := []string{}
	for _, gen := range in.generators {
		arg := "--" + gen.name + "_out"
		if len(gen.params) > 0 {
			arg += "="
			for i, kv := range gen.params {
				if i != 0 {
					arg += ","
				}
				arg += kv.key + "=" + kv.value
			}
		}
		if gen.outdir != "" {
			out := filepath.Join(in.OutputDir, gen.outdir)
			arg += ":" + out
		} else {
			arg += ":" + in.OutputDir
		}
		args = append(args, arg)
	}
	// args := []string{"--go_out=plugins=micro,paths=source_relative:" + oAbs}
	cmd.Dir = in.SrcDir
	// args = append(args, "-I"+srcDir)
	for _, inc := range in.Includes {
		args = append(args, "-I"+inc)
	}
	args = append(args, "-I.")
	for _, fi := range in.packages[pkg].files {
		args = append(args, fi.name)
	}
	cmd.Args = append(cmd.Args, args...)
	// fmt.Printf("wd: %v, cmd %+v\n", src, cmd.Args)
	out, err := cmd.CombinedOutput()
	return out, fmt.Sprintf("files-%d", len(in.packages[pkg].files)), err
}

func (in *regen) modinit(pkgName string) ([]byte, string, error) {
	tp := in.packages[pkgName]
	majorVer := pkgModVersion(tp.dirn)
	if majorVer == -1 {
		return nil, "skipped", nil
	}
	created := 0
	for out, _ := range in.relOutDir {
		dir := filepath.Join(in.OutputDir, out, tp.dirn)
		if _, err := os.Open(dir); err != nil {
			continue
		}
		gm := filepath.Join(in.OutputDir, out, tp.dirn, "go.mod")
		if _, err := os.Open(gm); err == nil {
			continue
		}
		created++
		cmd := exec.Command("go")
		cmd.Dir = filepath.Join(in.OutputDir, out, tp.dirn)
		args := []string{
			"mod",
			"init",
			pkgName,
		}
		cmd.Args = append(cmd.Args, args...)
		// fmt.Printf("go mod init %+v\n", cmd.Args)
		if out, err := cmd.CombinedOutput(); err != nil {
			return out, "error", err
		}
	}
	return nil, fmt.Sprintf("created-%d", created), nil
}

func (tp pkage) collect(depSet map[string]struct{}) []*pkage {
	deps := []*pkage{}
	for _, dep := range tp.replacements {
		if _, ex := depSet[dep.gopkg]; ex {
			continue
		}
		deps = append(deps, dep)
		depSet[dep.gopkg] = struct{}{}
		deps = append(deps, dep.collect(depSet)...)
	}
	return deps
}

func (in *regen) modrequire(pkg string) ([]byte, string, error) {
	tp := in.packages[pkg]
	majorVer := pkgModVersion(tp.dirn)
	if majorVer == -1 {
		return nil, "skipped", nil
	}
	for out, _ := range in.relOutDir {
		dir := filepath.Join(in.OutputDir, out, tp.dirn)
		if _, err := os.Open(dir); err != nil {
			continue
		}
		for _, k := range in.Require {
			cmd := exec.Command("go")
			cmd.Dir = filepath.Join(in.OutputDir, out, tp.dirn)
			args := []string{
				"mod",
				"edit",
				"-require=" + k,
			}
			cmd.Args = append(cmd.Args, args...)
			if out, err := cmd.CombinedOutput(); err != nil {
				return out, "error", err
			}
		}
	}
	return nil, fmt.Sprintf("required-%d", len(in.Require)), nil
}

func (in *regen) modreplace(pkg string) ([]byte, string, error) {
	tp := in.packages[pkg]
	majorVer := pkgModVersion(tp.dirn)
	if majorVer == -1 {
		return nil, "skipped", nil
	}
	imports := tp.collect(map[string]struct{}{})
	q.Q("pkg: %s imports: %v\n", pkg, imports)
	for out, _ := range in.relOutDir {
		dir := filepath.Join(in.OutputDir, out, tp.dirn)
		if _, err := os.Open(dir); err != nil {
			continue
		}
		for _, k := range imports {
			relPath := strings.Repeat("../", strings.Count(tp.dirn, "/")+1)
			cmd := exec.Command("go")
			cmd.Dir = filepath.Join(in.OutputDir, out, tp.dirn)
			args := []string{
				"mod",
				"edit",
				"-replace=" + k.gopkg + "=" + relPath + k.dirn,
			}
			cmd.Args = append(cmd.Args, args...)
			// fmt.Printf("go mod edit %+v\n", cmd.Args)
			if out, err := cmd.CombinedOutput(); err != nil {
				return out, "error", err
			}
		}
	}
	return nil, fmt.Sprintf("replaced-%d", len(imports)), nil
}

func (in *regen) modtidy(pkg string) ([]byte, string, error) {
	tp := in.packages[pkg]
	majorVer := pkgModVersion(tp.dirn)
	if majorVer == -1 {
		return nil, "skipped", nil
	}
	for out, _ := range in.relOutDir {
		dir := filepath.Join(in.OutputDir, out, tp.dirn)
		if _, err := os.Open(dir); err != nil {
			continue
		}
		cmd := exec.Command("go")
		cmd.Dir = filepath.Join(in.OutputDir, out, tp.dirn)
		args := []string{
			"mod",
			"tidy",
			"-v",
		}
		cmd.Args = append(cmd.Args, args...)
		// fmt.Printf("wd: %s go %+v\n", cmd.Dir, cmd.Args)
		if out, err := cmd.CombinedOutput(); err != nil {
			log.Warningf("ERROR:\n  cmd:%v\n  out:%v   \n   err:%v\n", cmd.Args, string(out), err)
			return out, "error", err
		} else {
			q.Q(string(out))
		}
	}
	return nil, "tidied", nil
}

func (in *regen) gittag(pkg string) ([]byte, string, error) {
	tp := in.packages[pkg]
	majorVer := pkgModVersion(tp.dirn)
	if majorVer == -1 {
		return nil, "skipped", nil
	}
	tagged := 0
	for out, _ := range in.relOutDir {
		dir := filepath.Join(in.OutputDir, out, tp.dirn)
		if _, err := os.Open(dir); err != nil {
			continue
		}
		dirty, found := tp.gitModAddDirty(in, out, majorVer)
		if !dirty {
			return nil, "clean", nil
		}
		q.Q("  dirty - major ver %v %v\n", majorVer, found)
		{ //
			cmd := exec.Command("git")
			args := []string{"commit", "-m", tp.source + " (" + tp.gitDescribe + ")"}
			cmd.Args = append(cmd.Args, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Warningf("ERROR:\n  cwd:%v\n  cmd:%v\n  out:%v   \n   err:%v\n", cmd.Dir, cmd.Args, string(out), err)
				return out, "error", err
			}
		} //
		ds := strings.Split(tp.dirn, "/")
		tagLead := out + "/" + ds[0]
		if out == "." {
			tagLead = ds[0]
		}
		// sems map[string]map[int64]Semvers
		sems := in.sems[tagLead][majorVer]
		sort.Sort(sems)
		sem := Semver{Major: majorVer}
		if len(sems) == 0 {
			// sem.Minor = 0
		} else {
			// TODO proto check for consistency
			sem = sems[0]
			sem.Minor++
			sem.Patch = 0
		}
		tag := fmt.Sprintf("%s/%s", tagLead, sem)
		{ //
			cmd := exec.Command("git")
			args := []string{"tag", tag}
			cmd.Args = append(cmd.Args, args...)
			out, err := cmd.CombinedOutput()
			if err != nil {
				log.Warningf("ERROR:\n  cwd:%v\n  cmd:%v\n  out:%v   \n   err:%v\n", cmd.Dir, cmd.Args, string(out), err)
				return out, "error", err
			}
		} //
		tagged++
		q.Q("  next semver for %s %s\n", tp.dirn, tag)
	}
	return nil, fmt.Sprintf("commit&tagged-%d", tagged), nil
}

var vxMod = regexp.MustCompile(`^[^/]+/v(\d+)$`)

// get version from directory name
// some_dir => 1
// some_dir/vXX => XX
// some_dir/other => -1
// some_dir/vXX/other => -1
func pkgModVersion(dirname string) int64 {
	if match := vxMod.FindStringSubmatch(dirname); len(match) > 0 {
		if majorVer, err := strconv.ParseInt(match[1], 10, 32); err != nil {
			log.Errorf("keh %v", err)
			os.Exit(1)
		} else {
			return majorVer
		}
	}
	if !strings.Contains(dirname, "/") {
		return 1
	}
	return -1
}

type Semvers []Semver
type Semver struct {
	Major, Minor, Patch int64
}

func (a Semver) String() string {
	return fmt.Sprintf("v%d.%d.%d", a.Major, a.Minor, a.Patch)
}

func (a Semvers) Len() int      { return len(a) }
func (a Semvers) Swap(i, j int) { a[i], a[j] = a[j], a[i] }
func (a Semvers) Less(i, j int) bool {
	x, y := a[i], a[j]
	if x.Major > y.Major {
		return true
	}
	if x.Minor > y.Minor {
		return true
	}
	if x.Patch > y.Patch {
		return true
	}
	return false
}

var pathSemver = regexp.MustCompile(`^(.+)/v(\d+)\.(\d+)\.(\d+)$`)

func gitGetTagSemver() map[string]map[int64]Semvers {
	ret := map[string]map[int64]Semvers{}
	cmd := exec.Command("git")
	// cmd.Dir = filepath.Join(in.OutputDir, tp.dirn)
	args := []string{
		"tag",
	}
	cmd.Args = append(cmd.Args, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Error(err)
		os.Exit(1)
	}
	scan := bufio.NewScanner(bytes.NewBuffer(out))
	for scan.Scan() {
		line := scan.Text()
		match := pathSemver.FindStringSubmatch(line)
		if len(match) == 0 {
			log.Warningf("tag does look right %v\n", line)
			q.Q("tag does look right %v\n", line)
			continue
		}
		q.Q("tag %v\n", line)
		modName := match[1]
		ma, _ := strconv.ParseInt(match[2], 10, 64)
		mi, _ := strconv.ParseInt(match[3], 10, 64)
		pa, _ := strconv.ParseInt(match[4], 10, 64)
		sem := Semver{Major: ma, Minor: mi, Patch: pa}
		sems, ex := ret[modName]
		if !ex {
			sems = make(map[int64]Semvers)
			sems[ma] = Semvers{sem}
			ret[modName] = sems
		} else {
			sems[ma] = append(sems[ma], sem)
		}
		ret[modName] = sems
	}
	return ret
}

var ignoreGitStatus = regexp.MustCompile(`^.*v(\d+)/(.+)?$`)

func (tpkg pkage) gitModAddDirty(in *regen, outDir string, major int64) (dirty bool, found []string) {
	wordDir := filepath.Join(in.OutputDir, outDir, tpkg.dirn)
	walkFn := func(path string, info os.FileInfo, err error) error {
		if err != nil {
			log.Warning(err)
			return err
		}
		mma := pkgModVersion(tpkg.dirn)
		if mma == -1 || mma != major {
			return filepath.SkipDir
		}
		// if vDir.MatchString(dirn) {
		// 	return filepath.SkipDir
		// }
		if !info.IsDir() {
			return nil
		}
		// fmt.Printf("cur '%v'\n", cur)
		cmd := exec.Command("git")
		cmd.Dir = wordDir
		args := []string{
			"status",
			"--porcelain",
			".",
		}
		cmd.Args = append(cmd.Args, args...)
		// fmt.Printf("git %+v\n", cmd.Args)
		out, err := cmd.CombinedOutput()
		if err != nil {
			log.Warning(err)
			return err
		}
		//
		scan := bufio.NewScanner(bytes.NewBuffer(out))
		for scan.Scan() {
			line := scan.Text()
			//here add
			if len(line) > 3 {
				fname := line[3:]
				if line[0] == 'R' {
					fname = fname[strings.Index(fname, " -> ")+4:]
				}
				fname = fname[len(outDir)+1+len(tpkg.dirn)+1:]
				if ignoreGitStatus.MatchString(fname) {
					continue
				}
				cmd := exec.Command("git")
				cmd.Dir = wordDir
				args := []string{}
				if line[1] != ' ' {
					args = []string{
						"add",
						fname,
					}
				} else {
					continue
				}
				cmd.Args = append(cmd.Args, args...)
				// fmt.Printf("git %+v\n", cmd.Args)
				out, err := cmd.CombinedOutput()
				if err != nil {
					log.Warningf("ERROR:\n  cwd:%v\n  cmd:%v\n  out:%v   \n   err:%v\n", cmd.Dir, cmd.Args, string(out), err)
					return err
				}
			} else {
				q.Q(line)
				log.Warning("3?")
			}
			dirty = true
			found = append(found, line)
		}
		return nil
	}
	if err := filepath.Walk(wordDir, walkFn); err != nil {
		log.Error(err)
		os.Exit(1)
	}
	return
}
