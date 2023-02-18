package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/golangci/golangci-lint/pkg/logutils"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/alexflint/go-arg"
	"github.com/golangci/golangci-lint/pkg/printers"
	"github.com/golangci/golangci-lint/pkg/result"
)

var args struct {
	Pwd        string `arg:"--pwd" default:"."                        help:"pwd to run linter"`       // := "/home/shane/workspace/manabie/backend"
	Cmd        string `arg:"-c"    default:"git diff"                 help:"command to find changes"` // := "git show 7b1e126d54a"
	JsonFile   string `arg:"-f"    default:"/tmp/golang_ci_lint.json" help:"json file output"`        // := "/tmp/golang_ci_lint.json"
	InspectDes string `arg:"-d"    default:"./..."                    help:"path to inspect"`         // := "internal/usermgmt/..."
}

func main() {
	arg.MustParse(&args)

	pwd := args.Pwd
	cmd := args.Cmd
	jsonFile := args.JsonFile
	inspectDes := args.InspectDes

	lint := NewGolangCILint().
		SetPwd(pwd).
		SetOutputJSON(jsonFile).
		SetInspectDes(inspectDes)
	_ = lint.Execute()
	issues, err := lint.FindJSONIssues()
	if err != nil {
		log.Panicln(err)
	}

	changes, err := findChanges(pwd, cmd)
	if err != nil {
		log.Panicln(err)
	}

	changesByFileName := getChangesByFileName(changes)
	for _, issue := range issues.Issues {
		if _, ok := changesByFileName[issue.FilePath()]; !ok {
			continue
		}

		changes := changesByFileName[issue.FilePath()]
		for _, change := range changes.Changes {
			if change.Start <= issue.Pos.Line && issue.Pos.Line <= change.End {
				printIssue(issue)
			}
		}
	}
}

type Changes struct {
	Start, End int
}

type FileChange struct {
	Changes []*Changes
	Path    string
}

type GolangCILint struct {
	binPath      string
	pwdPath      string
	outputFormat string
	outputFile   string
	checkingPath string
}

func NewGolangCILint() *GolangCILint {
	return &GolangCILint{
		binPath: "/home/shane/go/bin/golangci-lint",
		pwdPath: ".",
	}
}

func (g *GolangCILint) SetBin(path string) *GolangCILint {
	g.binPath = path
	return g
}

func (g *GolangCILint) SetPwd(path string) *GolangCILint {
	g.pwdPath = path
	return g
}

func (g *GolangCILint) SetOutputJSON(filename string) *GolangCILint {
	g.outputFormat = fmt.Sprintf("json:%s", filename)
	g.outputFile = filename
	return g
}

func (g *GolangCILint) SetInspectDes(path string) *GolangCILint {
	g.checkingPath = path
	return g
}

func (g *GolangCILint) Execute() error {
	return exec.Command(
		"sh", "-c",
		fmt.Sprintf(
			`cd %s; %s run --out-format %s %s`,
			g.pwdPath, g.binPath, g.outputFormat, g.checkingPath,
		),
	).Run()
}

func (g *GolangCILint) FindJSONIssues() (*printers.JSONResult, error) {
	file, err := os.Open(g.outputFile)
	if err != nil {
		return nil, err
	}

	bytes, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}

	var jsonResult printers.JSONResult
	if err := json.Unmarshal(bytes, &jsonResult); err != nil {
		return nil, err
	}

	return &jsonResult, nil
}

func printIssue(issue result.Issue) {
	p := printers.NewText(
		true, true,
		true, nil, logutils.StdOut,
	)
	if err := p.Print(context.Background(), []result.Issue{issue}); err != nil {
		log.Fatal(err)
	}
}

func findChangesByHunkHeader(hunkHeader string) ([][]int, error) {
	matches := regexp.
		MustCompile(`[+](\d+),(\d+)`).
		FindAllStringSubmatch(hunkHeader, -1)

	ranges := make([][]int, 0, len(matches))
	for _, match := range matches {
		start, err := strconv.ParseInt(match[1], 10, 64)
		if err != nil {
			return nil, err
		}

		amount, err := strconv.ParseInt(match[2], 10, 64)
		if err != nil {
			return nil, err
		}

		ranges = append(ranges, []int{int(start), int(start + amount)})
	}

	return ranges, nil
}

func listChangedFiles(pwd string, command string) ([]string, error) {
	output, err := exec.Command(
		"sh", "-c",
		fmt.Sprintf(` cd %s; %s --no-commit-id --name-only `, pwd, command),
	).Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(output), "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(line, "commit ") {
			break
		}
		files = append(files, line)
	}
	return files, nil
}

func findHunkHeadersOfFile(pwd string, cmd string, file string) ([]string, error) {
	output, err := exec.Command(
		"sh", "-c",
		fmt.Sprintf(`cd %s; %s -- %s`, pwd, cmd, file),
	).Output()
	if err != nil {
		return nil, err
	}

	hunkHeaders := regexp.
		MustCompile(`(@@[ \-+\d,]+@@)`).
		FindAllString(string(output), -1)

	return hunkHeaders, nil
}

func findChanges(pwd, cmd string) ([]FileChange, error) {
	files, err := listChangedFiles(pwd, cmd)
	if err != nil {
		return nil, err
	}

	fileChanges := make([]FileChange, 0, len(files))
	for _, file := range files {
		hunkHeaders, err := findHunkHeadersOfFile(pwd, cmd, file)
		if err != nil {
			return nil, err
		}

		changes := make([]*Changes, 0)
		for _, hunkHeader := range hunkHeaders {
			changesPositions, err := findChangesByHunkHeader(hunkHeader)
			if err != nil {
				return nil, err
			}

			for _, changesPosition := range changesPositions {
				changes = append(changes, &Changes{
					Start: changesPosition[0],
					End:   changesPosition[1],
				})
			}
		}

		if len(changes) == 0 {
			continue
		}

		fileChanges = append(fileChanges, FileChange{
			Path:    file,
			Changes: changes,
		})
	}
	return fileChanges, nil
}

func getChangesByFileName(changes []FileChange) map[string]FileChange {
	changesByFileName := make(map[string]FileChange)
	for _, change := range changes {
		changesByFileName[change.Path] = change
	}
	return changesByFileName
}
